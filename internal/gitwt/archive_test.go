package gitwt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These run against a real repository and a real remote, for the same reason
// the tests beside them do. The question this code answers — does the remote
// actually hold these commits now — has no meaning against a fake, and the
// second half of it, whether a local ref makes the work visible to `Unpushed`,
// is a fact about git's ref storage that no stub would have got right.

// origin gives a repository a bare remote and pushes main to it.
func origin(t *testing.T, g *Git, dir string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "origin.git")
	bareGit(t, filepath.Dir(bare), "init", "--bare", "-q", bare)
	if _, err := g.run(dir, "remote", "add", "origin", bare); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	if _, err := g.run(dir, "push", "-q", "origin", "main"); err != nil {
		t.Fatalf("push main: %v", err)
	}
	return bare
}

// bareGit runs git somewhere that is not a worktree, which is how the tests ask
// the remote what it holds without going through the code under test.
func bareGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// abandoned builds the thing the archive sweep exists for: a worktree holding
// commits that exist on no other ref.
func abandoned(t *testing.T, g *Git, branch string, commits int) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "wt")
	if err := g.Add(work, branch, "main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	t.Cleanup(func() { _ = g.Remove(work) })
	for i := 0; i < commits; i++ {
		write(t, work, "work.txt", strings.Repeat("x", i+1)+"\n")
		commit(t, work, "abandoned work")
	}
	return work
}

// plan is the two steps the sweep takes, as one call, so a test reads the way
// the caller does.
func plan(t *testing.T, g *Git, path string) ArchivePlan {
	t.Helper()
	p, err := g.PlanArchive(path)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return p
}

// The whole point: work that was protecting a worktree is preserved on the
// remote, and the worktree then reads as safe to remove.
func TestArchiveTurnsUnpushedWorkIntoAReclaimableWorktree(t *testing.T) {
	g, dir := repo(t)
	bare := origin(t, g, dir)
	work := abandoned(t, g, "codex/abandoned", 1)

	unpushed, err := g.Unpushed(work)
	if err != nil {
		t.Fatalf("unpushed: %v", err)
	}
	if !unpushed {
		t.Fatal("the fixture is not actually holding unique work")
	}

	p := plan(t, g, work)
	if p.Ref != "refs/archive/codex/abandoned" {
		t.Errorf("planned ref %q, want refs/archive/codex/abandoned", p.Ref)
	}
	if p.Commits != 1 {
		t.Errorf("planned %d commits, want 1", p.Commits)
	}

	existed, err := g.Archive(p)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if existed {
		t.Error("an empty remote reported it already held the commit")
	}

	// The remote is asked directly, not through the code that just claimed to
	// have written it.
	if landed := bareGit(t, bare, "rev-parse", p.Ref); landed != p.Commit {
		t.Errorf("the remote holds %s at %s, want %s", landed, p.Ref, p.Commit)
	}

	unpushed, err = g.Unpushed(work)
	if err != nil {
		t.Fatalf("unpushed after archive: %v", err)
	}
	if unpushed {
		t.Fatal("the worktree is still protected after its work was archived, so the sweep would never reclaim it")
	}
	if err := g.Remove(work); err != nil {
		t.Fatalf("remove after archive: %v", err)
	}
}

// Archiving must not put anything in the branch list. A namespace that shows up
// in `git branch -r` would trade forty-one gigabytes for thirty-two entries in
// every list a person reads to find a branch.
func TestArchivedWorkIsNotABranch(t *testing.T) {
	g, dir := repo(t)
	bare := origin(t, g, dir)
	work := abandoned(t, g, "codex/abandoned", 1)

	if _, err := g.Archive(plan(t, g, work)); err != nil {
		t.Fatalf("archive: %v", err)
	}

	heads := bareGit(t, bare, "for-each-ref", "--format=%(refname)", "refs/heads/")
	for _, ref := range strings.Fields(heads) {
		if strings.Contains(ref, "archive") {
			t.Errorf("the archive landed inside refs/heads: %q", ref)
		}
	}
	if !strings.HasPrefix(ArchiveNamespace, "refs/") || strings.HasPrefix(ArchiveNamespace, "refs/heads/") {
		t.Errorf("namespace %q is inside refs/heads", ArchiveNamespace)
	}
}

// A detached worktree has no branch name to borrow. Six of the forty-eight on
// this machine were detached, so skipping them was never an option.
func TestADetachedWorktreeIsArchivedUnderItsOwnName(t *testing.T) {
	g, dir := repo(t)
	bare := origin(t, g, dir)
	work := abandoned(t, g, "codex/detached-source", 1)

	head := plan(t, g, work).Commit
	if _, err := g.run(work, "checkout", "--detach", "--quiet", head); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if branch := g.Branch(work); branch != "" {
		t.Fatalf("the fixture is not detached: branch %q", branch)
	}

	p := plan(t, g, work)
	if !strings.HasPrefix(p.Ref, ArchiveNamespace+"detached/") {
		t.Errorf("ref %q does not name a detached archive", p.Ref)
	}
	if !strings.Contains(p.Ref, filepath.Base(work)) {
		t.Errorf("ref %q does not say which worktree it came from", p.Ref)
	}
	if _, err := g.Archive(p); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if landed := bareGit(t, bare, "rev-parse", p.Ref); landed != p.Commit {
		t.Errorf("the remote holds %s, want %s", landed, p.Commit)
	}
}

// Two worktrees, same branch name, different work. Overwriting the first
// archive would destroy exactly what this mode exists to preserve, so the
// second is refused and its directory stays where it is.
func TestArchiveRefusesToOverwriteSomebodyElsesArchive(t *testing.T) {
	g, dir := repo(t)
	bare := origin(t, g, dir)

	first := abandoned(t, g, "codex/collision", 1)
	kept := plan(t, g, first)
	if _, err := g.Archive(kept); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if err := g.Remove(first); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// Clear every local trace, so the second worktree's work really is unique
	// and the only thing in the way is the archive on the remote.
	if _, err := g.run(dir, "update-ref", "-d", kept.Ref); err != nil {
		t.Fatalf("clearing the local archive ref: %v", err)
	}
	if _, err := g.run(dir, "branch", "-D", "codex/collision"); err != nil {
		t.Fatalf("clearing the branch: %v", err)
	}

	second := abandoned(t, g, "codex/collision", 1)
	write(t, second, "different.txt", "other work\n")
	commit(t, second, "work the first worktree never had")

	_, err := g.Archive(plan(t, g, second))
	if err == nil {
		t.Fatal("the second worktree overwrote the first one's archive")
	}
	if !strings.Contains(err.Error(), kept.Ref) {
		t.Errorf("the refusal does not name the ref in the way: %v", err)
	}
	if landed := bareGit(t, bare, "rev-parse", kept.Ref); landed != kept.Commit {
		t.Errorf("the first archive was changed: %s, want %s", landed, kept.Commit)
	}
	unpushed, err := g.Unpushed(second)
	if err != nil {
		t.Fatalf("unpushed: %v", err)
	}
	if !unpushed {
		t.Fatal("a worktree whose archive failed reads as safe to delete")
	}
}

// Running the sweep twice must not report thirty-two pushes the second time.
func TestArchivingTwiceReportsTheSecondAsAlreadyThere(t *testing.T) {
	g, dir := repo(t)
	origin(t, g, dir)
	work := abandoned(t, g, "codex/twice", 1)

	p := plan(t, g, work)
	existed, err := g.Archive(p)
	if err != nil {
		t.Fatalf("first archive: %v", err)
	}
	if existed {
		t.Error("the first archive claims the remote already had it")
	}

	existed, err = g.Archive(p)
	if err != nil {
		t.Fatalf("second archive: %v", err)
	}
	if !existed {
		t.Error("archiving the same commit twice pushed twice")
	}
}

// The count is what a person triaging abandoned work reads, so it has to be the
// number of commits and not the number of refs or a boolean in disguise.
func TestUniqueCommitsCountsOnlyWhatThisBranchHasAlone(t *testing.T) {
	g, dir := repo(t)
	origin(t, g, dir)

	if got := plan(t, g, abandoned(t, g, "codex/three", 3)).Commits; got != 3 {
		t.Errorf("counted %d commits, want 3", got)
	}

	shared := filepath.Join(t.TempDir(), "shared")
	if err := g.Add(shared, "codex/none", "main"); err != nil {
		t.Fatalf("add: %v", err)
	}
	t.Cleanup(func() { _ = g.Remove(shared) })
	if got := plan(t, g, shared).Commits; got != 0 {
		t.Errorf("a branch cut from main with nothing of its own counted %d commits", got)
	}
}

func TestHasRemoteSeesOriginOnlyWhenItExists(t *testing.T) {
	g, dir := repo(t)
	if g.HasRemote() {
		t.Fatal("a repository with no remotes reports origin")
	}
	origin(t, g, dir)
	if !g.HasRemote() {
		t.Error("origin was added and is not seen")
	}
}

func TestReclaimCommandFetchesTheRefBackUnderItsOwnName(t *testing.T) {
	got := ReclaimCommand("refs/archive/codex/x")
	want := "git fetch origin refs/archive/codex/x:refs/archive/codex/x"
	if got != want {
		t.Errorf("reclaim command %q, want %q", got, want)
	}
}
