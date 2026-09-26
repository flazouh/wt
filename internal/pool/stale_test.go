package pool

import (
	"errors"
	"testing"
	"time"
)

// activity is a scripted answer to the two questions a stale lease is judged
// by, and a record of which paths were asked. The record matters: the process
// probe costs about a second, and a rule that asks it about every lease on
// every `wt` would make the listing slow for no reason.
type activity struct {
	touched  map[string]time.Time
	touchErr map[string]error
	inUse    map[string]bool
	probeErr error
	asked    []string
}

func (a *activity) LastTouched(path string) (time.Time, error) {
	a.asked = append(a.asked, "touched:"+path)
	if err := a.touchErr[path]; err != nil {
		return time.Time{}, err
	}
	return a.touched[path], nil
}

func (a *activity) InUse(path string) (bool, error) {
	a.asked = append(a.asked, "inuse:"+path)
	if a.probeErr != nil {
		return false, a.probeErr
	}
	return a.inUse[path], nil
}

// leasedPool is a full pool where every slot is leased, and slot 1 was last
// used, and last touched in git, three days before now.
func leasedPool(t *testing.T, now time.Time) (*Pool, *activity) {
	t.Helper()
	p := full(t)
	world := &activity{touched: map[string]time.Time{}}
	for _, s := range p.Slots {
		s.State = Leased
		s.Owner = "alex"
		s.OwnerPID = 42
		s.Used = now.Add(-time.Hour)
		world.touched[s.Path] = now.Add(-time.Hour)
	}
	p.Slots[0].Used = now.Add(-72 * time.Hour)
	world.touched[p.Slots[0].Path] = now.Add(-72 * time.Hour)
	return p, world
}

func TestAStaleLeaseWithNothingAliveIsReleased(t *testing.T) {
	now := epoch
	p, world := leasedPool(t, now)

	released := p.ReleaseStale(world, now)

	if len(released) != 1 || released[0].Index != 1 {
		t.Fatalf("released %v, want only slot 1", released)
	}
	if released[0].Idle != 72*time.Hour {
		t.Errorf("reported idle %v, want 72h", released[0].Idle)
	}
	if released[0].Owner != "alex" {
		t.Errorf("reported owner %q, want the lease holder it was taken from", released[0].Owner)
	}
	s := p.Slots[0]
	if s.State != Idle || s.Owner != "" || s.OwnerPID != 0 {
		t.Fatalf("slot 1 is %s owned by %q pid %d, want idle and unowned", s.State, s.Owner, s.OwnerPID)
	}
	// The released slot keeps its old stamp so it is first in line to be
	// recycled. Stamping it now would put every other idle slot ahead of it.
	if !s.Used.Equal(now.Add(-72 * time.Hour)) {
		t.Errorf("Used is %v, want the last activity, not the moment of release", s.Used)
	}
	if s.Path == "" {
		t.Error("releasing a lease destroyed the worktree; it only frees the slot")
	}
	for _, other := range p.Slots[1:] {
		if other.State != Leased {
			t.Errorf("slot %d was released although it was used an hour ago", other.Index)
		}
	}
}

// An agent often works in a worktree through absolute paths from somewhere
// else, so the registry's stamp goes stale while the work goes on. The index
// and HEAD move whenever anyone stages or commits there.
func TestRecentGitActivityKeepsALease(t *testing.T) {
	now := epoch
	p, world := leasedPool(t, now)
	world.touched[p.Slots[0].Path] = now.Add(-2 * time.Hour)

	if released := p.ReleaseStale(world, now); len(released) != 0 {
		t.Fatalf("released %v although slot 1's index moved two hours ago", released)
	}
	if p.Slots[0].State != Leased {
		t.Fatal("slot 1 lost its lease")
	}
}

func TestALiveProcessKeepsAStaleLease(t *testing.T) {
	now := epoch
	p, world := leasedPool(t, now)
	world.inUse = map[string]bool{p.Slots[0].Path: true}

	if released := p.ReleaseStale(world, now); len(released) != 0 {
		t.Fatalf("released %v with a process working in it", released)
	}
}

// The probe failing means nobody knows whether a process is in there, and that
// must read as "there is", never as "there is not".
func TestAnUnanswerableProbeKeepsAStaleLease(t *testing.T) {
	now := epoch
	p, world := leasedPool(t, now)
	world.probeErr = errors.New("lsof reported no process working directories")

	if released := p.ReleaseStale(world, now); len(released) != 0 {
		t.Fatalf("released %v while the liveness probe was broken", released)
	}
	if p.Slots[0].State != Leased {
		t.Fatal("slot 1 lost its lease on an unanswered question")
	}
}

func TestUnreadableGitActivityKeepsAStaleLease(t *testing.T) {
	now := epoch
	p, world := leasedPool(t, now)
	world.touchErr = map[string]error{p.Slots[0].Path: errors.New("index unreadable")}

	if released := p.ReleaseStale(world, now); len(released) != 0 {
		t.Fatalf("released %v without knowing when it was last touched", released)
	}
}

func TestPinnedAndIdleSlotsAreNeverReleased(t *testing.T) {
	now := epoch
	p, world := leasedPool(t, now)
	for i, s := range p.Slots {
		s.Used = now.Add(-30 * 24 * time.Hour)
		world.touched[s.Path] = s.Used
		if i%2 == 0 {
			s.State = Pinned
		} else {
			s.State = Idle
			s.Owner = ""
		}
	}

	if released := p.ReleaseStale(world, now); len(released) != 0 {
		t.Fatalf("released %v; only a lease can be released", released)
	}
	for _, s := range p.Slots {
		if s.State == Leased {
			t.Fatalf("slot %d became leased", s.Index)
		}
	}
	if p.Slots[0].State != Pinned {
		t.Fatal("a pinned slot lost its pin")
	}
}

// "More than forty-eight hours" is the rule, so forty-eight exactly is not.
//
// The lease's own stamp is far older than the threshold, so the git answer is
// the one that decides. With the stamp at the threshold instead, the cheap
// check alone would decide and the second comparison would go untested.
func TestTheStaleThresholdIsStrict(t *testing.T) {
	for _, tc := range []struct {
		name  string
		idle  time.Duration
		freed bool
	}{
		{"exactly the threshold", StaleLease, false},
		{"one second past it", StaleLease + time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := epoch
			p, world := leasedPool(t, now)
			p.Slots[0].Used = now.Add(-10 * 24 * time.Hour)
			world.touched[p.Slots[0].Path] = now.Add(-tc.idle)

			released := p.ReleaseStale(world, now)

			if freed := len(released) == 1; freed != tc.freed {
				t.Fatalf("idle %v: released %v, want freed=%v", tc.idle, released, tc.freed)
			}
		})
	}
}

// The registry's stamp is a lower bound on the last activity, so a lease used
// within the threshold is fresh whatever git says, and asking git or lsof about
// it is wasted time.
func TestAFreshLeaseIsNeverProbed(t *testing.T) {
	now := epoch
	p, world := leasedPool(t, now)
	p.Slots[0].Used = now.Add(-time.Hour)

	p.ReleaseStale(world, now)

	if len(world.asked) != 0 {
		t.Fatalf("asked %v about leases used within the hour", world.asked)
	}
}

// A lease with no path is an acquire that stopped part way. Its directory is in
// an unknown state, which is exactly the slot that must not be handed on.
func TestALeaseWithNoWorktreeYetIsKept(t *testing.T) {
	now := epoch
	p, world := leasedPool(t, now)
	p.Slots[0].Path = ""

	if released := p.ReleaseStale(world, now); len(released) != 0 {
		t.Fatalf("released %v, a lease whose worktree was never built", released)
	}
}
