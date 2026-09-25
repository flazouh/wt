package gitwt

import (
	"path/filepath"
	"strings"
	"testing"
)

// These are the other half of the unpushed question: commits whose hashes are
// on no other ref, but whose content is already on the remote's default branch.
// It is what a cherry-pick or a squash without a pull request leaves behind,
// and it held a slot hostage on this machine with four such commits.

// landOnRemote puts copies of commits on branch in the main checkout and pushes
// it. An unrelated commit goes first so each copy gets a new hash, which is
// what a real cherry-pick onto a moving main produces; picked onto the same
// parent in the same second, git would rebuild the identical commit and the
// test would prove nothing.
func landOnRemote(t *testing.T, dir, branch string, commits ...string) {
	t.Helper()
	current := bareGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if current != branch {
		bareGit(t, dir, "checkout", "-q", "-B", branch)
	}
	write(t, dir, "unrelated-"+strings.ReplaceAll(branch, "/", "-")+".txt", "moves the base\n")
	bareGit(t, dir, "add", "-A")
	bareGit(t, dir, "commit", "-qm", "someone else's work")
	for _, sha := range commits {
		bareGit(t, dir, "cherry-pick", sha)
	}
	bareGit(t, dir, "push", "-q", "origin", branch)
	if current != branch {
		bareGit(t, dir, "checkout", "-q", current)
	}
}

// branchWith cuts a worktree holding n commits of its own, and returns it with
// their hashes, oldest first.
func branchWith(t *testing.T, g *Git, branch string, n int) (string, []string) {
	t.Helper()
	work := abandoned(t, g, branch, 0)
	var shas []string
	for i := 0; i < n; i++ {
		write(t, work, filepath.Base(branch)+"-"+string(rune('a'+i))+".txt", "work\n")
		commit(t, work, "work "+string(rune('a'+i)))
		shas = append(shas, bareGit(t, work, "rev-parse", "HEAD"))
	}
	return work, shas
}

func unpushed(t *testing.T, g *Git, work string) bool {
	t.Helper()
	yes, err := g.Unpushed(work)
	if err != nil {
		t.Fatalf("unpushed: %v", err)
	}
	return yes
}

// The case that wedged slot 1: every commit is on origin/main by content.
func TestCommitsAlreadyOnTheRemoteByContentAreNotUnpushed(t *testing.T) {
	g, dir := repo(t)
	origin(t, g, dir)
	work, shas := branchWith(t, g, "feature/picked", 2)
	if !unpushed(t, g, work) {
		t.Fatal("the fixture is not holding unique commits before they land")
	}

	landOnRemote(t, dir, "main", shas...)

	if unpushed(t, g, work) {
		t.Fatal("every commit is on origin/main by content, yet the worktree is still protected")
	}
}

// One commit the remote lacks is enough to keep the whole worktree.
func TestOneCommitTheRemoteLacksKeepsTheWorktreeUnpushed(t *testing.T) {
	g, dir := repo(t)
	origin(t, g, dir)
	work, shas := branchWith(t, g, "feature/half", 2)

	landOnRemote(t, dir, "main", shas[0])

	if !unpushed(t, g, work) {
		t.Fatal("a commit that exists only here was reported as pushed")
	}
}

// The remote's own idea of its default branch wins over a guess at its name.
func TestTheRemotesDefaultBranchIsWhereContentIsLookedFor(t *testing.T) {
	g, dir := repo(t)
	origin(t, g, dir)
	work, shas := branchWith(t, g, "feature/trunk", 1)

	landOnRemote(t, dir, "trunk", shas...)
	bareGit(t, dir, "remote", "set-head", "origin", "trunk")

	if unpushed(t, g, work) {
		t.Fatal("the commit is on origin's default branch, trunk, but was looked for elsewhere")
	}
}

// git cherry skips merge commits entirely, so a merge carrying its own
// resolution, or an edit made during the merge, would never be compared at all.
// Its parents being upstream says nothing about it.
func TestAMergeCommitIsNeverTakenAsPushedByContent(t *testing.T) {
	g, dir := repo(t)
	origin(t, g, dir)
	work, shas := branchWith(t, g, "feature/merge", 1)

	bareGit(t, work, "checkout", "-q", "-b", "side")
	write(t, work, "side.txt", "side\n")
	bareGit(t, work, "add", "-A")
	bareGit(t, work, "commit", "-qm", "side work")
	side := bareGit(t, work, "rev-parse", "HEAD")
	bareGit(t, work, "checkout", "-q", "feature/merge")
	bareGit(t, work, "merge", "-q", "--no-ff", "--no-commit", "side")
	write(t, work, "only-in-the-merge.txt", "work nobody else has\n")
	bareGit(t, work, "add", "-A")
	bareGit(t, work, "commit", "-qm", "merge with an edit of its own")
	bareGit(t, work, "branch", "-D", "side")

	landOnRemote(t, dir, "main", shas[0], side)

	if !unpushed(t, g, work) {
		t.Fatal("a merge commit with content of its own was reported as pushed")
	}
}

// With nothing to compare against, the answer is the one reachability gave. Not
// being able to find out is not the same as finding out it is safe.
func TestWithNoUpstreamToCompareAgainstTheOldAnswerStands(t *testing.T) {
	g, dir := repo(t)
	work, shas := branchWith(t, g, "feature/alone", 1)
	bareGit(t, dir, "branch", "-m", "main", "trunk")

	bareGit(t, dir, "commit", "-q", "--allow-empty", "-m", "moves the base")
	bareGit(t, dir, "cherry-pick", shas[0])

	if !unpushed(t, g, work) {
		t.Fatal("no origin/main and no main exist, yet the worktree was cleared by content")
	}
}

// An empty comparison proves nothing. With no remote the fallback is the local
// main, and a worktree standing on main compares against itself, which lists no
// commits at all. Every line being "-" is vacuously true of no lines.
func TestAnEmptyComparisonIsNotProof(t *testing.T) {
	g, dir := repo(t)
	bareGit(t, dir, "checkout", "-q", "-b", "elsewhere")
	work := filepath.Join(t.TempDir(), "wt")
	if err := g.Add(work, "main", "main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	t.Cleanup(func() { _ = g.Remove(work) })
	write(t, work, "local.txt", "only here\n")
	commit(t, work, "a commit on main that was never pushed")

	if !unpushed(t, g, work) {
		t.Fatal("a local-only commit on main was reported as pushed")
	}
}
