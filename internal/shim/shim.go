// Package shim installs the wrapper that makes the cap real.
//
// A CLI can only police worktrees it was asked to create. `git worktree add`
// is always there, and an agent that reaches for it walks straight past the
// pool. The shim is a small script named `git`, placed earlier on PATH, that
// refuses exactly one subcommand and passes everything else through untouched.
//
// It sits in front of every git call on the machine, so it is written to fail
// open: any condition it does not understand ends in exec of the real git. The
// only path that refuses is the one it positively recognises.
package shim

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flazouh/wt/internal/gitwt"
)

// Script is the shim, with %s replaced by the resolved real git.
//
// exec, not a subshell, so signals, exit codes, stdin and the process tree are
// the real git's. Anything else would be visible to every tool on the machine
// that shells out to git.
const Script = `#!/bin/sh
` + gitwt.ShimSentinel + `
# Installed by wt. Refuses 'git worktree add' so the pool stays the only way in.
# Remove with: wt enforce --uninstall
REAL=%s

if [ "$1" = "worktree" ] && [ "$2" = "add" ]; then
  if [ -n "$WT_POOL" ]; then
    exec "$REAL" "$@"
  fi
  echo "error: 'git worktree add' is disabled on this machine"
  echo "help: worktrees come from the pool, capped at ${WT_LIMIT:-6} per repository"
  echo "help: run 'wt take <branch>' to get one"
  echo "help: run 'wt' to see what the pool is holding"
  exit 2
fi

exec "$REAL" "$@"
`

// Path is where the shim goes: the first PATH entry the user owns, so it wins
// over the real git without touching anything the system manages.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "bin", "git"), nil
}

// Status is what the shim looks like right now.
type Status struct {
	Installed bool
	Path      string
	Real      string
	Effective bool // whether the shim is the git that PATH actually finds first
}

// Inspect reports the shim's state without changing anything.
func Inspect() (Status, error) {
	path, err := Path()
	if err != nil {
		return Status{}, err
	}
	s := Status{Path: path}

	data, err := os.ReadFile(path)
	if err == nil && strings.Contains(string(data), gitwt.ShimSentinel) {
		s.Installed = true
	}

	real, err := gitwt.RealBinary()
	if err != nil {
		return s, err
	}
	s.Real = real

	// Installed is not the same as working: another directory earlier on PATH
	// could hold a real git, and then the shim is decoration.
	s.Effective = s.Installed && firstGitOnPath() == path
	return s, nil
}

func firstGitOnPath() string {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		candidate := filepath.Join(dir, "git")
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		return candidate
	}
	return ""
}

// Install writes the shim. It is idempotent: installing twice rewrites the same
// file, which is also how the real git's path is repaired after a brew upgrade
// moves it.
func Install() (Status, error) {
	path, err := Path()
	if err != nil {
		return Status{}, err
	}
	real, err := gitwt.RealBinary()
	if err != nil {
		return Status{}, err
	}
	// Refusing to install over a real git is the one check that matters here:
	// overwriting it would leave the machine with no git at all.
	if existing, err := os.Stat(path); err == nil && !existing.IsDir() {
		data, _ := os.ReadFile(path)
		if !strings.Contains(string(data), gitwt.ShimSentinel) {
			return Status{}, fmt.Errorf("%s already exists and is not the wt shim; refusing to overwrite it", path)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Status{}, err
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(Script, real)), 0o755); err != nil {
		return Status{}, err
	}
	return Inspect()
}

// Uninstall removes the shim, and only the shim.
func Uninstall() error {
	path, err := Path()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.Contains(string(data), gitwt.ShimSentinel) {
		return fmt.Errorf("%s is not the wt shim; leaving it alone", path)
	}
	return os.Remove(path)
}
