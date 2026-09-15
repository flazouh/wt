package gitwt

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// These run against a real repository on purpose. The bug they exist for — a
// branch with nothing of its own reading as unpushed — was invisible to a fake
// git and only appeared the first time the tool was pointed at a directory.

func repo(t *testing.T) (*Git, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "init")

	g, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return g, dir
}

// The whole point of the fix: a repository with no remote must not report every
// worktree as holding unpushed work, or nothing is ever recyclable.
func TestABranchWithNothingOfItsOwnIsNotUnpushed(t *testing.T) {
	g, dir := repo(t)
	work := filepath.Join(t.TempDir(), "wt")

	if err := g.Add(work, "feature/x", "main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	t.Cleanup(func() { _ = g.Remove(work) })

	unpushed, err := g.Unpushed(work)
	if err != nil {
		t.Fatalf("unpushed: %v", err)
	}
	if unpushed {
		t.Fatal("a branch cut from main with no commits of its own reads as unpushed; the pool would wedge")
	}
	_ = dir
}

// The other half: work that exists nowhere else must be protected.
func TestACommitThatExistsNowhereElseIsUnpushed(t *testing.T) {
	g, _ := repo(t)
	work := filepath.Join(t.TempDir(), "wt")

	if err := g.Add(work, "feature/y", "main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	t.Cleanup(func() { _ = g.Remove(work) })

	write(t, work, "new.txt", "work\n")
	commit(t, work, "a commit only this branch has")

	unpushed, err := g.Unpushed(work)
	if err != nil {
		t.Fatalf("unpushed: %v", err)
	}
	if !unpushed {
		t.Fatal("a commit that exists on no other ref was reported as safe to destroy")
	}
}

func TestDirtySeesUntrackedFilesToo(t *testing.T) {
	g, _ := repo(t)
	work := filepath.Join(t.TempDir(), "wt")

	if err := g.Add(work, "feature/z", "main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	t.Cleanup(func() { _ = g.Remove(work) })

	clean, err := g.Dirty(work)
	if err != nil {
		t.Fatalf("dirty: %v", err)
	}
	if clean {
		t.Fatal("a fresh worktree reads as dirty")
	}

	// Untracked, not modified: a file an agent created and has not committed is
	// exactly the work that must not be deleted.
	write(t, work, "scratch.txt", "notes\n")

	dirty, err := g.Dirty(work)
	if err != nil {
		t.Fatalf("dirty: %v", err)
	}
	if !dirty {
		t.Fatal("an untracked file did not make the worktree dirty")
	}
}

func TestMissingPathsAreNeitherDirtyNorUnpushed(t *testing.T) {
	g, _ := repo(t)
	gone := filepath.Join(t.TempDir(), "never-existed")

	for name, ask := range map[string]func(string) (bool, error){
		"dirty":    g.Dirty,
		"unpushed": g.Unpushed,
	} {
		yes, err := ask(gone)
		if err != nil {
			t.Errorf("%s on a missing path: %v", name, err)
		}
		if yes {
			t.Errorf("%s reported true for a path that does not exist", name)
		}
	}
}

func TestListSeparatesTheMainCheckoutFromTheRest(t *testing.T) {
	g, _ := repo(t)
	work := filepath.Join(t.TempDir(), "wt")
	if err := g.Add(work, "feature/l", "main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	t.Cleanup(func() { _ = g.Remove(work) })

	trees, err := g.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(trees) != 2 {
		t.Fatalf("listed %d worktrees, want 2", len(trees))
	}
	if !trees[0].Main {
		t.Error("the first entry is not marked as the main checkout")
	}
	if trees[1].Main {
		t.Error("a linked worktree was marked as the main checkout")
	}
	if trees[1].Branch != "feature/l" {
		t.Errorf("branch %q, want feature/l", trees[1].Branch)
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, message string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", message}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}
