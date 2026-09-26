package gitwt

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// commitAt commits everything in dir with a committer date of when, which is
// how a test makes HEAD look old without waiting.
func commitAt(t *testing.T, dir, message string, when time.Time) {
	t.Helper()
	stamp := when.UTC().Format(time.RFC3339)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", message}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// indexOf finds a worktree's own index file. A linked worktree keeps it under
// the main checkout's .git, not in the directory itself.
func indexOf(t *testing.T, work string) string {
	t.Helper()
	return bareGit(t, work, "rev-parse", "--path-format=absolute", "--git-path", "index")
}

// Last activity is the later of the two things that move when someone works in
// a worktree: the index when they stage, HEAD when they commit. Either one on
// its own misses half the cases.
func TestLastTouchedIsTheLaterOfTheIndexAndHead(t *testing.T) {
	g, _ := repo(t)
	work := filepath.Join(t.TempDir(), "wt")
	if err := g.Add(work, "feature/a", "main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	t.Cleanup(func() { _ = g.Remove(work) })

	committed := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	write(t, work, "a.txt", "changed\n")
	commitAt(t, work, "old work", committed)
	index := indexOf(t, work)

	// Staged long ago, committed later: HEAD is the last sign of life.
	staged := time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(index, staged, staged); err != nil {
		t.Fatal(err)
	}
	got, err := g.LastTouched(work)
	if err != nil {
		t.Fatalf("last touched: %v", err)
	}
	if !got.Equal(committed) {
		t.Errorf("last touched %v, want HEAD's committer date %v", got, committed)
	}

	// Staged after the commit: the index is.
	staged = time.Date(2021, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(index, staged, staged); err != nil {
		t.Fatal(err)
	}
	got, err = g.LastTouched(work)
	if err != nil {
		t.Fatalf("last touched: %v", err)
	}
	if !got.Equal(staged) {
		t.Errorf("last touched %v, want the index's modification time %v", got, staged)
	}
}

// Each worktree has its own index. Reading the main checkout's, or another
// worktree's, would let one busy slot keep every abandoned lease alive.
func TestLastTouchedReadsThisWorktreesOwnIndex(t *testing.T) {
	g, dir := repo(t)
	work := filepath.Join(t.TempDir(), "wt")
	if err := g.Add(work, "feature/own", "main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	t.Cleanup(func() { _ = g.Remove(work) })

	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	write(t, work, "own.txt", "x\n")
	commitAt(t, work, "old", old)
	if err := os.Chtimes(indexOf(t, work), old, old); err != nil {
		t.Fatal(err)
	}
	// The main checkout is busy right now.
	now := time.Now()
	if err := os.Chtimes(indexOf(t, dir), now, now); err != nil {
		t.Fatal(err)
	}

	got, err := g.LastTouched(work)
	if err != nil {
		t.Fatalf("last touched: %v", err)
	}
	if !got.Equal(old) {
		t.Fatalf("last touched %v, want %v; it read another worktree's index", got, old)
	}
}

// Not knowing must be an error, never a zero time. A zero time reads as "last
// touched at the dawn of computing", which is the most releasable answer there
// is.
func TestLastTouchedOnAMissingWorktreeIsAnError(t *testing.T) {
	g, _ := repo(t)

	got, err := g.LastTouched(filepath.Join(t.TempDir(), "gone"))
	if err == nil {
		t.Fatalf("last touched a directory that does not exist at %v", got)
	}
}
