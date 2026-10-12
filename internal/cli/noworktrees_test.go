package cli

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A repository on the no-worktree list is refused before any slot is touched.
func TestTakeRefusesARepositoryThatTakesNoWorktrees(t *testing.T) {
	t.Setenv("WT_STATE_DIR", t.TempDir())
	t.Setenv("WT_LIMIT", "")

	repo := filepath.Join(t.TempDir(), "atelier")
	for _, args := range [][]string{{"init", "-q", "-b", "main", repo}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(repo)

	out, code := run(t, "take", "feat/x", "--owner", "codex")
	if code == OK {
		t.Fatalf("take succeeded in a no-worktree repository:\n%s", out)
	}
	if !strings.Contains(out, "takes no worktrees") {
		t.Errorf("refusal does not say why:\n%s", out)
	}
}
