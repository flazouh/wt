package pool

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

var epoch = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

// safeWorld says everything is safe to delete. The default for tests that are
// not about safety.
type safeWorld struct{}

func (safeWorld) InUse(string) (bool, error)    { return false, nil }
func (safeWorld) Dirty(string) (bool, error)    { return false, nil }
func (safeWorld) Unpushed(string) (bool, error) { return false, nil }

// guarded reports specific paths as unsafe for one reason.
type guarded struct {
	safeWorld
	inUse    map[string]bool
	dirty    map[string]bool
	unpushed map[string]bool
}

func (g guarded) InUse(p string) (bool, error)    { return g.inUse[p], nil }
func (g guarded) Dirty(p string) (bool, error)    { return g.dirty[p], nil }
func (g guarded) Unpushed(p string) (bool, error) { return g.unpushed[p], nil }

// blind cannot tell whether anything is in use, which is the state the machine
// is in when lsof fails.
type blind struct{ safeWorld }

func (blind) InUse(string) (bool, error) {
	return false, errors.New("lsof returned nothing")
}

func full(t *testing.T) *Pool {
	t.Helper()
	p := &Pool{Repo: "/repo"}
	for i := 1; i <= DefaultLimit; i++ {
		p.Slots = append(p.Slots, &Slot{
			Index: i,
			Repo:  "/repo",
			Path:  "/repo/.wt/" + string(rune('0'+i)),
			State: Idle,
			Used:  epoch.Add(time.Duration(i) * time.Hour),
		})
	}
	return p
}

func TestCreatesUpToTheLimitAndNoFurther(t *testing.T) {
	p := &Pool{Repo: "/repo"}
	for i := 1; i <= DefaultLimit; i++ {
		plan, err := p.Acquire("b"+string(rune('0'+i)), "agent", 1, safeWorld{}, epoch)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		if !plan.Create {
			t.Fatalf("acquire %d should have created a slot", i)
		}
		plan.Slot.Path = "/repo/.wt/" + string(rune('0'+i))
	}
	if len(p.Slots) != DefaultLimit {
		t.Fatalf("pool holds %d slots, want %d", len(p.Slots), DefaultLimit)
	}

	// Every agent finishes. The worktrees stay on disk, which is what makes
	// the sixth request a recycle rather than a refusal.
	for i := 1; i <= DefaultLimit; i++ {
		if err := p.Release(i, epoch.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("release %d: %v", i, err)
		}
	}

	// The sixth must recycle rather than grow the pool.
	plan, err := p.Acquire("b6", "agent", 1, safeWorld{}, epoch.Add(time.Hour))
	if err != nil {
		t.Fatalf("sixth acquire: %v", err)
	}
	if plan.Create {
		t.Fatal("the sixth acquire created a slot; the cap is not a cap")
	}
	if len(p.Slots) != DefaultLimit {
		t.Fatalf("pool grew to %d slots past the limit", len(p.Slots))
	}
}

func TestReusesAnIdleSlotAlreadyOnTheBranch(t *testing.T) {
	p := full(t)
	p.Slots[2].Branch = "feature/x"

	plan, err := p.Acquire("feature/x", "agent", 1, safeWorld{}, epoch)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !plan.Reuse {
		t.Fatal("an idle slot on the branch should be reused, not rebuilt")
	}
	if plan.Slot.Index != 3 {
		t.Fatalf("reused slot %d, want 3", plan.Slot.Index)
	}
}

func TestRecyclesTheLeastRecentlyUsed(t *testing.T) {
	p := full(t)

	plan, err := p.Acquire("new", "agent", 1, safeWorld{}, epoch)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !plan.Recycle {
		t.Fatal("a full pool should recycle")
	}
	// Slot 1 carries the oldest Used stamp.
	if plan.Slot.Index != 1 {
		t.Fatalf("recycled slot %d, want the least recently used, 1", plan.Slot.Index)
	}
	if plan.Evicted != "/repo/.wt/1" {
		t.Fatalf("evicted %q, want the old path recorded", plan.Evicted)
	}
}

// The three reasons a worktree must survive, each on its own, because an
// implementation that checks only the first two still destroys work.
func TestNeverRecyclesWorkThatWouldBeLost(t *testing.T) {
	for _, tc := range []struct {
		name  string
		world guarded
	}{
		{"a process is in it", guarded{inUse: map[string]bool{"/repo/.wt/1": true}}},
		{"it is dirty", guarded{dirty: map[string]bool{"/repo/.wt/1": true}}},
		{"it is unpushed", guarded{unpushed: map[string]bool{"/repo/.wt/1": true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := full(t)

			plan, err := p.Acquire("new", "agent", 1, tc.world, epoch)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			if plan.Slot.Index == 1 {
				t.Fatalf("recycled slot 1 even though %s", tc.name)
			}
			// It should fall through to the next oldest.
			if plan.Slot.Index != 2 {
				t.Fatalf("recycled slot %d, want 2", plan.Slot.Index)
			}
		})
	}
}

func TestNeverRecyclesALeasedOrPinnedSlot(t *testing.T) {
	p := full(t)
	p.Slots[0].State = Leased
	p.Slots[0].Owner = "codex"
	p.Slots[1].State = Pinned

	plan, err := p.Acquire("new", "agent", 1, safeWorld{}, epoch)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if plan.Slot.Index == 1 || plan.Slot.Index == 2 {
		t.Fatalf("recycled slot %d, which was leased or pinned", plan.Slot.Index)
	}
}

// Not knowing whether a process is inside must stop the whole operation. The
// prune script this replaces aborts when lsof comes back empty, for the same
// reason: an unanswered question is not a "no".
func TestRefusesToGuessWhenSafetyCannotBeDetermined(t *testing.T) {
	p := full(t)

	_, err := p.Acquire("new", "agent", 1, blind{}, epoch)

	if err == nil {
		t.Fatal("acquire succeeded while the liveness probe was broken")
	}
	var isFull *ErrFull
	if errors.As(err, &isFull) {
		t.Fatal("a broken probe was reported as a full pool; it is a failure, not a verdict")
	}
}

func TestAFullPoolSaysWhichLeaseToEnd(t *testing.T) {
	p := full(t)
	for i, s := range p.Slots {
		s.State = Leased
		s.Owner = "agent-" + string(rune('a'+i))
	}

	_, err := p.Acquire("new", "agent", 1, safeWorld{}, epoch)

	var isFull *ErrFull
	if !errors.As(err, &isFull) {
		t.Fatalf("got %v, want ErrFull", err)
	}
	if len(isFull.Blockers) != DefaultLimit {
		t.Fatalf("named %d blockers, want one per slot", len(isFull.Blockers))
	}
	if isFull.Blockers[0] != "1: leased by agent-a" {
		t.Fatalf("blocker %q does not name the owner to chase", isFull.Blockers[0])
	}
}

func TestReleaseKeepsTheWorktreeForTheNextAcquire(t *testing.T) {
	p := full(t)
	p.Slots[0].State = Leased
	p.Slots[0].Branch = "feature/x"

	if err := p.Release(1, epoch); err != nil {
		t.Fatalf("release: %v", err)
	}

	if p.Slots[0].State != Idle {
		t.Fatalf("slot state %q after release, want idle", p.Slots[0].State)
	}
	if p.Slots[0].Path == "" {
		t.Fatal("release destroyed the worktree; a pool that rebuilds every time is a queue")
	}
	if p.OnBranch("feature/x") == nil {
		t.Fatal("the released slot is not offered back for its own branch")
	}
}

func TestReleasingAPinnedSlotDoesNothing(t *testing.T) {
	p := full(t)
	p.Slots[0].State = Pinned

	if err := p.Release(1, epoch); err != nil {
		t.Fatalf("release: %v", err)
	}

	if p.Slots[0].State != Pinned {
		t.Fatal("release unpinned a pinned slot")
	}
}

func TestAcquireRequiresABranch(t *testing.T) {
	p := &Pool{Repo: "/repo"}

	if _, err := p.Acquire("", "agent", 1, safeWorld{}, epoch); err == nil {
		t.Fatal("acquire accepted an empty branch")
	}
}

func TestLimitOverridesTheDefault(t *testing.T) {
	p := &Pool{Repo: "/repo", Limit: DefaultLimit + 3}
	for i := 1; i <= p.Cap(); i++ {
		plan, err := p.Acquire(fmt.Sprintf("b%d", i), "agent", 1, safeWorld{}, epoch)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		if !plan.Create {
			t.Fatalf("acquire %d did not create", i)
		}
	}
	if len(p.Slots) != DefaultLimit+3 {
		t.Fatalf("pool holds %d slots, want %d", len(p.Slots), DefaultLimit+3)
	}

	for _, s := range p.Slots {
		s.State = Pinned
	}
	_, err := p.Acquire("one-more", "agent", 1, safeWorld{}, epoch)
	var full *ErrFull
	if !errors.As(err, &full) {
		t.Fatalf("got %v, want ErrFull", err)
	}
	if full.Limit != DefaultLimit+3 {
		t.Fatalf("ErrFull reports limit %d, want %d", full.Limit, DefaultLimit+3)
	}
}
