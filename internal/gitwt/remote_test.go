package gitwt

import (
	"path/filepath"
	"testing"
)

// A branch someone pushed and this checkout has never had locally. Taking it
// must land on their work, not on a fresh branch of the same name cut from
// main: that is what `wt take feat/flu-72-i18n-welcome` did, and the worktree
// came up at origin/main with none of the pushed commits in it.
func TestAddTracksABranchThatExistsOnlyOnTheRemote(t *testing.T) {
	g, dir := repo(t)
	bare := origin(t, g, dir)

	// Someone else pushes feat/x from their own clone.
	other := filepath.Join(t.TempDir(), "other")
	bareGit(t, filepath.Dir(other), "clone", "-q", bare, other)
	bareGit(t, other, "config", "user.email", "o@example.com")
	bareGit(t, other, "config", "user.name", "O")
	bareGit(t, other, "checkout", "-q", "-b", "feat/x")
	write(t, other, "x.txt", "pushed work\n")
	bareGit(t, other, "add", "-A")
	bareGit(t, other, "commit", "-qm", "pushed work")
	bareGit(t, other, "push", "-q", "origin", "feat/x")
	pushed := bareGit(t, other, "rev-parse", "HEAD")

	if err := g.Fetch(); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	path := filepath.Join(t.TempDir(), "slot")
	if err := g.Add(path, "feat/x", g.DefaultBase()); err != nil {
		t.Fatalf("add: %v", err)
	}

	if got := bareGit(t, path, "rev-parse", "HEAD"); got != pushed {
		t.Errorf("worktree is at %s, want the pushed commit %s", got, pushed)
	}
	if got := bareGit(t, path, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat/x" {
		t.Errorf("worktree is on %q, want a local feat/x", got)
	}
	if got := bareGit(t, path, "rev-parse", "--abbrev-ref", "feat/x@{upstream}"); got != "origin/feat/x" {
		t.Errorf("feat/x tracks %q, want origin/feat/x", got)
	}
}

// A branch nobody has pushed is still cut from the base, as before.
func TestAddCutsANewBranchFromTheBaseWhenTheRemoteLacksIt(t *testing.T) {
	g, dir := repo(t)
	origin(t, g, dir)
	if err := g.Fetch(); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	base := bareGit(t, dir, "rev-parse", "origin/main")

	path := filepath.Join(t.TempDir(), "slot")
	if err := g.Add(path, "feat/new", g.DefaultBase()); err != nil {
		t.Fatalf("add: %v", err)
	}

	if got := bareGit(t, path, "rev-parse", "HEAD"); got != base {
		t.Errorf("worktree is at %s, want origin/main %s", got, base)
	}
	if got := bareGit(t, path, "rev-parse", "--abbrev-ref", "HEAD"); got != "feat/new" {
		t.Errorf("worktree is on %q, want feat/new", got)
	}
}
