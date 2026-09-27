package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flazouh/wt/internal/pool"
)

// `wt take` on a branch that was pushed from somewhere else, and that this
// checkout has not fetched yet. The take's own fetch has to bring it in, and the
// worktree has to stand on the pushed commit rather than on a new branch of the
// same name cut from main.
func TestTakeChecksOutABranchThatExistsOnlyOnTheRemote(t *testing.T) {
	t.Setenv("WT_STATE_DIR", t.TempDir())
	t.Setenv("WT_LIMIT", "")

	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	tmp := t.TempDir()
	bare := filepath.Join(tmp, "origin.git")
	git(tmp, "init", "--bare", "-q", "-b", "main", bare)

	repo := filepath.Join(tmp, "repo")
	git(tmp, "clone", "-q", bare, repo)
	git(repo, "commit", "-q", "--allow-empty", "-m", "init")
	git(repo, "push", "-q", "origin", "main")

	other := filepath.Join(tmp, "other")
	git(tmp, "clone", "-q", bare, other)
	git(other, "checkout", "-q", "-b", "feat/x")
	git(other, "commit", "-q", "--allow-empty", "-m", "pushed work")
	git(other, "push", "-q", "origin", "feat/x")
	pushed := git(other, "rev-parse", "HEAD")

	t.Chdir(repo)
	out, code := run(t, "take", "feat/x", "--owner", "codex")
	if code != OK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	t.Cleanup(func() { _ = os.RemoveAll(poolRoot(repo)) })

	path := pool.SlotPath(poolRoot(repo), 1)
	if got := git(path, "rev-parse", "HEAD"); got != pushed {
		t.Errorf("worktree is at %s, want the pushed commit %s", got, pushed)
	}
	if got := git(path, "rev-parse", "--abbrev-ref", "feat/x@{upstream}"); got != "origin/feat/x" {
		t.Errorf("feat/x tracks %q, want origin/feat/x", got)
	}
}
