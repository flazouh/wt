package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flazouh/wt/internal/gitwt"
	"github.com/flazouh/wt/internal/pool"
	"github.com/flazouh/wt/internal/registry"
)

// These drive the real command against a real repository, a real registry and
// the real process probe. The pool's rule is tested without any of those; what
// is tested here is that the wiring hands it true answers, and that the agent
// is told when a lease was taken back.

var threeDaysAgo = time.Now().Add(-72 * time.Hour)

// wedged builds the pool as it was found: six slots, every one leased, slot 1
// by a session that last did anything three days ago. It leaves the test
// standing in the main checkout, which is where an agent runs `wt` from.
func wedged(t *testing.T) (repo string, slots []string) {
	t.Helper()
	t.Setenv("WT_STATE_DIR", t.TempDir())
	dir := t.TempDir()
	stamp := threeDaysAgo.UTC().Format(time.RFC3339)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "T"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	g, err := gitwt.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	repo = g.Repo
	root := poolRoot(repo)
	for i := 1; i <= pool.Limit; i++ {
		path := pool.SlotPath(root, i)
		if err := g.Add(path, "work/"+string(rune('0'+i)), "main"); err != nil {
			t.Fatalf("add slot %d: %v", i, err)
		}
		slots = append(slots, path)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	// Slot 1's index goes back to when its session died, so git has seen
	// nothing happen there since.
	index, err := exec.Command("git", "-C", slots[0], "rev-parse", "--path-format=absolute", "--git-path", "index").Output()
	if err != nil {
		t.Fatalf("finding slot 1's index: %v", err)
	}
	if err := os.Chtimes(strings.TrimSpace(string(index)), threeDaysAgo, threeDaysAgo); err != nil {
		t.Fatal(err)
	}

	store, err := registry.Open()
	if err != nil {
		t.Fatal(err)
	}
	err = store.Update(func(reg *registry.Registry) error {
		p := reg.For(repo)
		for i, path := range slots {
			used := time.Now().Add(-time.Hour)
			if i == 0 {
				used = threeDaysAgo
			}
			p.Slots = append(p.Slots, &pool.Slot{
				Index: i + 1, Repo: repo, Path: path, Branch: "work/" + string(rune('1'+i)),
				State: pool.Leased, Owner: "alex", OwnerPID: 1, Created: threeDaysAgo, Used: used,
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Chdir(repo)
	return repo, slots
}

func slotState(t *testing.T, repo string, index int) pool.State {
	t.Helper()
	store, err := registry.Open()
	if err != nil {
		t.Fatal(err)
	}
	reg, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	s := reg.For(repo).Find(index)
	if s == nil {
		t.Fatalf("slot %d is gone from the registry", index)
	}
	return s.State
}

// The wedge, end to end: the take that was refused for days now succeeds, and
// says whose lease it took back and why.
func TestTakeRecyclesAnAbandonedLeaseAndSaysSo(t *testing.T) {
	repo, _ := wedged(t)

	out, code := run(t, "take", "feature/new", "--owner", "codex")

	if code != OK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"action: recycled", "slot: 1", "released[1]{slot,owner,why}", `1,alex,"lease released: idle 3d, no live process"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	if got := slotState(t, repo, 1); got != pool.Leased {
		t.Errorf("slot 1 is %s after the take, want leased to the new owner", got)
	}
}

// Releasing the lease does not excuse the worktree from the recycle checks. The
// take still fails, but the release itself is kept and reported: the slot really
// is abandoned, and the listing should say so.
func TestTakeStillRefusesAReleasedSlotWithUncommittedWork(t *testing.T) {
	repo, slots := wedged(t)
	if err := os.WriteFile(filepath.Join(slots[0], "notes.txt"), []byte("unsaved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Writing the file must not look like git activity; only staging does.
	out, code := run(t, "take", "feature/new", "--owner", "codex")

	if code != Fail {
		t.Fatalf("exit %d, want the pool to be full:\n%s", code, out)
	}
	for _, want := range []string{"the pool is full", "1: it has uncommitted changes", `1,alex,"lease released: idle 3d, no live process"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(slots[0], "notes.txt")); err != nil {
		t.Fatalf("the uncommitted work is gone: %v", err)
	}
	if got := slotState(t, repo, 1); got != pool.Idle {
		t.Errorf("slot 1 is %s, want the release kept although the take failed", got)
	}
}

// The listing is where a person looks when the pool is full, so it must show
// the abandoned lease for what it has become.
func TestTheListingReleasesAnAbandonedLease(t *testing.T) {
	repo, _ := wedged(t)

	out, code := run(t)

	if code != OK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, `1,alex,"lease released: idle 3d, no live process"`) {
		t.Errorf("the listing does not report the release:\n%s", out)
	}
	if !strings.Contains(out, "1,work/1,idle,3d,") {
		t.Errorf("the listing does not show slot 1 as idle:\n%s", out)
	}
	if got := slotState(t, repo, 1); got != pool.Idle {
		t.Errorf("slot 1 is %s, want idle", got)
	}
	if got := slotState(t, repo, 2); got != pool.Leased {
		t.Errorf("slot 2 is %s, but it was used an hour ago", got)
	}
}

// A process standing in the worktree keeps the lease however old it is. The
// real probe is asked, so a real process is put there.
func TestALiveProcessKeepsAnAbandonedLease(t *testing.T) {
	repo, slots := wedged(t)
	sleeper := exec.Command("sleep", "30")
	sleeper.Dir = slots[0]
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })

	out, code := run(t)

	if code != OK {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if strings.Contains(out, "lease released") {
		t.Errorf("released a lease with a process working in it:\n%s", out)
	}
	if got := slotState(t, repo, 1); got != pool.Leased {
		t.Errorf("slot 1 is %s, want still leased", got)
	}
}
