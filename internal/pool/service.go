package pool

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Git is what the service needs from a repository. The interface lives here, at
// the consumer, so the pool's rules stay testable without one.
type Git interface {
	Add(path, branch, base string) error
	Remove(path string) error
	DefaultBase() string
	Fetch() error
	List() ([]Tree, error)
}

// Tree is a worktree as git reports it.
type Tree struct {
	Path   string
	Branch string
	Main   bool
}

// Service carries out what Acquire decides.
type Service struct {
	Pool   *Pool
	Git    Git
	Safety Safety
	// Activity judges whether a lease has been abandoned. Nil leaves every
	// lease standing until someone ends it, which is how the pool behaved
	// before leases could go stale.
	Activity Activity
	Root     string
	Now      func() time.Time
}

// SlotPath is where slot n lives. The index is in the directory name so a path
// on disk can be traced back to a registry entry by looking at it.
func SlotPath(root string, index int) string {
	return filepath.Join(root, fmt.Sprintf("slot-%d", index))
}

// Acquire hands over a worktree, building or recycling one as needed.
//
// Everything that touches disk happens after the decision, and a failure part
// way through leaves the slot marked leased rather than silently free: a slot
// whose directory is in an unknown state must not be handed to someone else.
func (s *Service) Acquire(branch, owner string, pid int) (*Slot, string, error) {
	now := s.Now()
	plan, err := s.Pool.Acquire(branch, owner, pid, s.Safety, now)
	if err != nil {
		return nil, "", err
	}

	switch {
	case plan.Reuse:
		return plan.Slot, "reused", nil

	case plan.Recycle:
		if err := s.Git.Remove(plan.Evicted); err != nil {
			return nil, "", fmt.Errorf("recycling %s: %w", plan.Evicted, err)
		}
		fallthrough

	default:
		path := SlotPath(s.Root, plan.Slot.Index)
		// A directory left behind by an interrupted run would make git refuse.
		if err := os.RemoveAll(path); err != nil {
			return nil, "", err
		}
		// Fetch before cutting a branch, so a new worktree starts from what the
		// remote has rather than from whatever was last pulled. A failure here
		// is not fatal: working offline is legitimate.
		_ = s.Git.Fetch()
		if err := s.Git.Add(path, branch, s.Git.DefaultBase()); err != nil {
			return nil, "", err
		}
		plan.Slot.Path = path
		if plan.Recycle {
			return plan.Slot, "recycled", nil
		}
		return plan.Slot, "created", nil
	}
}

// Reconcile brings the registry back in line with the world: it drops slots
// whose worktree has disappeared, then takes back leases whose holder has gone.
//
// Someone removing a worktree by hand is not an error, but a registry that
// still counts it holds a slot against the limit for nothing. A lease whose
// session died is the same waste with a different cause, and before it was
// handled here it wedged the pool at the cap for days.
//
// The drop runs first so the stale check never asks git about a directory that
// is no longer there. It runs on every take and on every `wt`, rather than only
// when the pool is full, so the listing shows the slot as idle, which is what it
// has become, and not as a lease nobody holds.
func (s *Service) Reconcile() (Reconciled, error) {
	trees, err := s.Git.List()
	if err != nil {
		return Reconciled{}, err
	}
	alive := make(map[string]bool, len(trees))
	for _, t := range trees {
		alive[t.Path] = true
	}

	kept := s.Pool.Slots[:0]
	dropped := 0
	for _, slot := range s.Pool.Slots {
		if slot.Path != "" && !alive[slot.Path] {
			dropped++
			continue
		}
		kept = append(kept, slot)
	}
	s.Pool.Slots = kept

	r := Reconciled{Dropped: dropped}
	if s.Activity != nil {
		r.Released = s.Pool.ReleaseStale(s.Activity, s.Now())
	}
	return r, nil
}

// Reconciled is what Reconcile changed, so the caller can say so.
type Reconciled struct {
	// Dropped counts slots whose worktree had disappeared.
	Dropped int
	// Released lists the leases taken back as abandoned.
	Released []Released
}

// Strays are worktrees git knows about that the pool never created. They are
// what the cap cannot see, so they are reported rather than touched.
func (s *Service) Strays() ([]Tree, error) {
	trees, err := s.Git.List()
	if err != nil {
		return nil, err
	}
	owned := make(map[string]bool, len(s.Pool.Slots))
	for _, slot := range s.Pool.Slots {
		owned[slot.Path] = true
	}

	var strays []Tree
	for _, t := range trees {
		if t.Main || owned[t.Path] {
			continue
		}
		strays = append(strays, t)
	}
	return strays, nil
}
