// Package proc reads the process table for two questions about a lease: which
// long-lived process took it, and whether that same process still runs.
//
// The PID of wt itself answers neither. wt exits the moment the lease is
// written, and so does the `zsh -c` an agent's shell tool wraps every command
// in. The holder is further up: the agent session, or the terminal shell of the
// person who typed the command.
//
// Like the liveness probe, it must never turn "I could not find out" into "no":
// a failed or empty reading is an error, never a table in which every holder
// is dead.
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
	"time"
)

// ErrUnavailable means the process table could not be read. Callers must keep
// every lease rather than read it as "every holder has exited".
var ErrUnavailable = errors.New("process table unavailable")

// StartTolerance is how far apart two readings of one process's start time may
// be. ps prints whole seconds, and on Linux it derives the start from the boot
// time, which moves by a second or so when the wall clock is stepped.
const StartTolerance = 2 * time.Second

// Process is one row of the table.
type Process struct {
	PID  int
	PPID int
	// Start is when the process started. With the PID it names one process: a
	// PID can be reused, a PID and a start time cannot.
	Start time.Time
	// Name is the executable's base name.
	Name string
	// Args is the full command line, empty when ps could not show it.
	Args string
}

// Table is a snapshot of the machine's processes, by PID.
type Table map[int]Process

// agents are the programs whose process is a session.
var agents = map[string]bool{
	"claude": true, "codex": true, "cursor-agent": true, "opencode": true,
	"gemini": true, "aider": true, "goose": true, "amp": true, "droid": true,
	"devin": true,
}

var shells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "fish": true, "ksh": true,
}

// Holder names the process that holds a lease taken by a process whose parent
// is from: the nearest agent session above it, or failing that the nearest
// interactive shell. False when neither is found, and the lease then records no
// holder and stays on the two-day rule.
//
// Only those two, because a wrong guess costs a live agent its worktree. A
// script, a make or an npm run between the agent and wt looks durable and is
// not: it exits, and the lease would go half an hour later while the agent
// waits on CI. An agent or a terminal shell outlives the work instead, which
// costs at most the two days the pool waited before holders were recorded.
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
		if interactive(p) {
			return p, true
		}
	}
	return Process{}, false
}

// Alive reports whether the process that started at start still runs as pid.
func (t Table) Alive(pid int, start time.Time) bool {
	p, ok := t[pid]
	if !ok {
		return false
	}
	gap := p.Start.Sub(start)
	return gap <= StartTolerance && gap >= -StartTolerance
}

// interactive reports whether a process is a shell waiting on a person: a shell
// given only flags, none of them -c. A shell with an operand runs a script, one
// with -c runs a single command, and one whose command line ps could not show
// is unknown; none of those holds a lease.
func interactive(p Process) bool {
	if !shells[strings.TrimPrefix(p.Name, "-")] {
		return false
	}
	fields := strings.Fields(p.Args)
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields[1:] {
		if !strings.HasPrefix(f, "-") || f == "-" {
			return false
		}
		if !strings.HasPrefix(f, "--") && strings.Contains(f, "c") {
			return false
		}
	}
	return true
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
func (s *Snapshot) Alive(pid int, start time.Time) (bool, error) {
	if err := s.load(); err != nil {
		return false, err
	}
	return s.table.Alive(pid, start), nil
}

// Holder is Table.Holder on the snapshot. An unreadable table fails, and the
// caller then records no holder.
func (s *Snapshot) Holder(from int) (Process, bool, error) {
	if err := s.load(); err != nil {
		return Process{}, false, err
	}
	p, ok := s.table.Holder(from)
	return p, ok, nil
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

// ps runs one reading in UTC and the C locale. lstart is printed in local
// time, so without TZ a holder recorded by an agent running in UTC would read
// as a different process to a person running in Paris.
func ps(format string) (string, error) {
	cmd := exec.Command("ps", "-A", "-o", format)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		// A partial table would read a missing holder as an exited one.
		return "", fmt.Errorf("%w: ps failed: %v", ErrUnavailable, err)
	}
	return string(out), nil
}

// lstart is the layout of ps's lstart column once its words are joined by
// single spaces.
const lstart = "Mon Jan 2 15:04:05 2006"

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
		start, err3 := time.Parse(lstart, strings.Join(f[2:7], " "))
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		table[pid] = Process{
			PID:   pid,
			PPID:  ppid,
			Start: start,
			Name:  filepath.Base(strings.Join(f[7:], " ")),
		}
	}
	if len(table) == 0 {
		return nil, fmt.Errorf("%w: ps listed no processes", ErrUnavailable)
	}
	for _, line := range strings.Split(args, "\n") {
		pidText, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
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
