// Package liveness answers one question: is anything alive inside this
// directory right now?
//
// The answer gates every deletion the pool performs, so the package is built
// around one rule: it must never turn "I could not find out" into "no". The
// shell script this replaces learned that the hard way and aborts when lsof
// comes back empty; the same guard lives here, as a typed error rather than an
// exit.
package liveness

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// ErrProbeUnavailable means the machine could not be asked. Callers must treat
// it as "leave everything alone", never as "nothing is running".
var ErrProbeUnavailable = errors.New("liveness probe unavailable")

// Probe reads the machine's open files and command lines once, then answers
// many questions from that snapshot.
//
// One snapshot rather than a call per slot, because six slots would otherwise
// mean six lsof invocations at roughly a second each, and because a set of
// answers taken at one instant is consistent where a sequence of them is not.
type Probe struct {
	once  sync.Once
	paths []string
	err   error
}

// New returns a probe that has not yet read anything.
func New() *Probe { return &Probe{} }

// InUse reports whether a live process is working inside path.
//
// It matches a process whose working directory is the path or below it, and any
// process whose command line mentions the path: an editor opened on a file
// inside a worktree never chdirs into it, and deleting that worktree is just as
// destructive.
func (p *Probe) InUse(path string) (bool, error) {
	if err := p.load(); err != nil {
		return false, err
	}
	if path == "" {
		return false, nil
	}
	for _, candidate := range p.paths {
		if strings.Contains(candidate, path) {
			return true, nil
		}
	}
	return false, nil
}

func (p *Probe) load() error {
	p.once.Do(func() {
		cwds, err := processCwds()
		if err != nil {
			p.err = err
			return
		}
		// An empty reading is the dangerous one. There is always at least this
		// process, so zero cwds means lsof was denied or is missing, not that
		// the machine is idle.
		if len(cwds) == 0 {
			p.err = fmt.Errorf("%w: lsof reported no process working directories", ErrProbeUnavailable)
			return
		}
		lines, err := processCommands()
		if err != nil {
			p.err = err
			return
		}
		p.paths = append(cwds, lines...)
	})
	return p.err
}

// processCwds returns the working directory of every process the user can see.
func processCwds() ([]string, error) {
	// -a: intersect the filters. -d cwd: working directories only. -Fn: print
	// one field per line, each prefixed by its type, which parses without
	// splitting on spaces that appear in real paths.
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "-Fn").Output()
	if err != nil {
		// lsof exits non-zero when some processes refuse inspection, which is
		// normal and not a reason to discard the output it did produce.
		if len(out) == 0 {
			return nil, fmt.Errorf("%w: %v", ErrProbeUnavailable, err)
		}
	}
	var cwds []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n/") {
			cwds = append(cwds, line[1:])
		}
	}
	return cwds, nil
}

// processCommands returns every process command line.
func processCommands() ([]string, error) {
	out, err := exec.Command("ps", "-axo", "command=").Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("%w: ps failed: %v", ErrProbeUnavailable, err)
	}
	return strings.Split(string(out), "\n"), nil
}
