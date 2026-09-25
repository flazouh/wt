package gitwt

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// LastTouched reports the last time anyone did something git can see in a
// worktree: the later of its index file's modification time and its HEAD
// commit's committer date.
//
// It exists because a lease's own stamp lies about abandonment. An agent often
// works in a worktree through absolute paths while its shell stands somewhere
// else, so it never refreshes the lease and the process probe never sees it.
// Whoever does the work, from wherever, the index moves when they stage and
// HEAD moves when they commit.
//
// Both, because each misses half. A commit made with `commit -a` or in another
// clone and fetched here moves HEAD further than the index; a day of staging
// without a commit moves only the index. The index is the worktree's own, found
// through `--git-path`: a linked worktree keeps it under the main checkout's
// .git, and reading the main checkout's instead would let one busy checkout keep
// every abandoned lease alive.
//
// The index can also move when nobody worked: a read-only `git status` in there
// may rewrite it to refresh cached stat data. That errs towards keeping a
// lease, which is the direction a mistake here should fall.
//
// Any failure is an error rather than a zero time. A zero time reads as "last
// touched at the dawn of computing", the most releasable answer there is, and
// it must not be what "could not tell" turns into.
func (g *Git) LastTouched(path string) (time.Time, error) {
	if path == "" {
		return time.Time{}, fmt.Errorf("no worktree path to inspect")
	}
	index, err := g.run(path, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return time.Time{}, err
	}
	info, err := os.Stat(strings.TrimSpace(index))
	if err != nil {
		return time.Time{}, fmt.Errorf("reading the index of %s: %w", path, err)
	}

	out, err := g.run(path, "log", "-1", "--format=%ct", "HEAD")
	if err != nil {
		return time.Time{}, err
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading HEAD's commit date in %s: %w", path, err)
	}

	latest := time.Unix(seconds, 0)
	if staged := info.ModTime(); staged.After(latest) {
		latest = staged
	}
	return latest, nil
}
