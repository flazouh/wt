// Package proc reads the process table for two questions about a lease: which
// long-lived process took it, and whether that same process still runs.
//
// The PID of wt itself answers neither. wt exits the moment the lease is
// written, and so does the `zsh -c` an agent's shell tool wraps every command
// in. The holder is further up: the agent session, or the terminal shell of the
// person who typed the command.
//
// Like the liveness probe, it must never turn "I could not find out" into "no":
// an empty reading is an error, never a table in which every owner is dead.
package proc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// ErrUnavailable means the process table could not be read. Callers must keep
// every lease rather than read it as "every owner has exited".
var ErrUnavailable = errors.New("process table unavailable")

// Process is one row of the table.
type Process struct {
	PID  int
	PPID int
	// Start is when the process started, as ps prints it. With the PID it names
	// one process: a PID can be reused, a PID and a start time cannot.
	Start string
	// Name is the executable's base name.
	Name string
	// Args is the full command line.
	Args string
}

// Table is a snapshot of the machine's processes, by PID.
type Table map[int]Process

// agents are the programs whose process is a session. The nearest one above wt
// is the holder, even when a durable-looking launcher sits between it and the
// throwaway shell.
var agents = map[string]bool{
	"claude": true, "codex": true, "cursor-agent": true, "opencode": true,
	"gemini": true, "aider": true, "goose": true, "amp": true, "droid": true,
	"devin": true,
}

// wrappers exit with the one command they run, so they never hold a lease.
var wrappers = map[string]bool{
	"wt": true, "rtk": true, "env": true, "timeout": true, "gtimeout": true,
	"nohup": true, "xargs": true, "sudo": true, "time": true,
}

var shells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "fish": true, "ksh": true,
}

// Holder names the process that holds a lease taken by a process whose parent
// is from: the nearest agent session above it, or failing that the nearest
// process that is not a throwaway wrapper. False when neither is found, and the
// lease then records no owner at all.
func (t Table) Holder(from int) (Process, bool) {
	var line []Process
	seen := map[int]bool{}
	for pid := from; pid > 1 && !seen[pid]; {
		seen[pid] = true
		p, ok := t[pid]
		if !ok {
			break
		}
		line = append(line, p)
		pid = p.PPID
	}
	for _, p := range line {
		if agents[p.Name] {
			return p, true
		}
	}
	for _, p := range line {
		if !throwaway(p) {
			return p, true
		}
	}
	return Process{}, false
}

// Alive reports whether the process that started at start still runs as pid.
func (t Table) Alive(pid int, start string) bool {
	p, ok := t[pid]
	return ok && p.Start == start
}

// throwaway reports whether a process exits with the one command it was given:
// a known wrapper, or a shell run with -c.
func throwaway(p Process) bool {
	if wrappers[p.Name] {
		return true
	}
	if !shells[strings.TrimPrefix(p.Name, "-")] {
		return false
	}
	fields := strings.Fields(p.Args)
	for _, f := range fields[min(1, len(fields)):] {
		if f == "--" || !strings.HasPrefix(f, "-") || strings.HasPrefix(f, "--") {
			break
		}
		if strings.Contains(f, "c") {
			return true
		}
	}
	return false
}

// Snapshot reads the table on the first question and answers every later one
// from that reading, so one command runs ps once however many leases it asks
// about, and the answers cannot disagree with each other.
type Snapshot struct {
	once  sync.Once
	table Table
	err   error
}

// NewSnapshot returns a snapshot that has not yet read anything.
func NewSnapshot() *Snapshot { return &Snapshot{} }

func (s *Snapshot) load() error {
	s.once.Do(func() { s.table, s.err = Read() })
	return s.err
}

// Alive is Table.Alive on the snapshot. It fails, rather than answering no,
// when the table could not be read.
func (s *Snapshot) Alive(pid int, start string) (bool, error) {
	if err := s.load(); err != nil {
		return false, err
	}
	return s.table.Alive(pid, start), nil
}

// Holder is Table.Holder on the snapshot. An unreadable table names no holder.
func (s *Snapshot) Holder(from int) (Process, bool) {
	if s.load() != nil {
		return Process{}, false
	}
	return s.table.Holder(from)
}

// Read takes one snapshot of every process.
func Read() (Table, error) {
	// lstart is five words in the C locale on both macOS and Linux, which is
	// what lets the name after it contain spaces. The command line needs a
	// call of its own, since it has spaces too and must come last.
	starts, err := ps("pid=,ppid=,lstart=,ucomm=")
	if err != nil {
		return nil, err
	}
	args, err := ps("pid=,args=")
	if err != nil {
		return nil, err
	}
	table, err := parse(starts, args)
	if err != nil {
		return nil, err
	}
	if _, ok := table[os.Getpid()]; !ok {
		return nil, fmt.Errorf("%w: ps did not list this process", ErrUnavailable)
	}
	return table, nil
}

func ps(format string) (string, error) {
	cmd := exec.Command("ps", "-A", "-o", format)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return "", fmt.Errorf("%w: ps failed: %v", ErrUnavailable, err)
	}
	return string(out), nil
}

// parse joins the two ps readings. Rows it cannot read are skipped; a reading
// with no rows at all is an error.
func parse(starts, args string) (Table, error) {
	table := Table{}
	for _, line := range strings.Split(starts, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		table[pid] = Process{
			PID:   pid,
			PPID:  ppid,
			Start: strings.Join(f[2:7], " "),
			Name:  filepath.Base(strings.Join(f[7:], " ")),
		}
	}
	if len(table) == 0 {
		return nil, fmt.Errorf("%w: ps listed no processes", ErrUnavailable)
	}
	for _, line := range strings.Split(args, "\n") {
		line = strings.TrimSpace(line)
		pidText, rest, _ := strings.Cut(line, " ")
		pid, err := strconv.Atoi(pidText)
		if err != nil {
			continue
		}
		if p, ok := table[pid]; ok {
			p.Args = strings.TrimSpace(rest)
			table[pid] = p
		}
	}
	return table, nil
}
