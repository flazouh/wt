package pool

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// ErrFull is returned when every slot is spoken for. It carries the per-slot
// reasons so the caller can tell the agent which lease to end rather than
// making it ask again.
type ErrFull struct {
	Blockers []string
}

func (e *ErrFull) Error() string {
	return fmt.Sprintf("all %d slots are in use: %v", Limit, e.Blockers)
}

// Safety answers the questions the pool cannot answer itself. A slot is only
// torn down when every one of these says it is safe.
//
// An implementation that cannot tell must return an error rather than false:
// "I do not know whether a process is in there" and "no process is in there"
// lead to opposite decisions, and conflating them is how an agent's working
// directory gets deleted underneath it.
type Safety interface {
	// InUse reports whether a live process is sitting in the path.
	InUse(path string) (bool, error)
	// Dirty reports whether the worktree has uncommitted changes.
	Dirty(path string) (bool, error)
	// Unpushed reports whether it holds commits that exist nowhere else.
	Unpushed(path string) (bool, error)
}

// Unsafe names the first reason a slot must be left alone, or empty if none.
func Unsafe(s Safety, path string) (string, error) {
	checks := []struct {
		reason string
		ask    func(string) (bool, error)
	}{
		{"a process is working in it", s.InUse},
		{"it has uncommitted changes", s.Dirty},
		{"it holds commits that are not pushed", s.Unpushed},
	}
	for _, check := range checks {
		yes, err := check.ask(path)
		if err != nil {
			return "", fmt.Errorf("checking whether %s: %w", check.reason, err)
		}
		if yes {
			return check.reason, nil
		}
	}
	return "", nil
}

// Pool is the set of slots belonging to one repository.
type Pool struct {
	Repo  string  `json:"repo"`
	Slots []*Slot `json:"slots"`
}

// Find returns the slot at an index, or nil.
func (p *Pool) Find(index int) *Slot {
	for _, s := range p.Slots {
		if s.Index == index {
			return s
		}
	}
	return nil
}

// OnBranch returns an idle slot already sitting on the branch, or nil.
//
// Reusing it is what makes the pool feel like a cache rather than a queue: an
// agent that comes back to the same branch finds its work still there, and
// nothing is torn down or rebuilt to give it back.
func (p *Pool) OnBranch(branch string) *Slot {
	for _, s := range p.Slots {
		if s.State == Idle && s.Branch == branch {
			return s
		}
	}
	return nil
}

// freeIndex returns the lowest index not yet taken, and whether there was one.
func (p *Pool) freeIndex() (int, bool) {
	taken := make(map[int]bool, len(p.Slots))
	for _, s := range p.Slots {
		taken[s.Index] = true
	}
	for i := 1; i <= Limit; i++ {
		if !taken[i] {
			return i, true
		}
	}
	return 0, false
}

// Plan is what Acquire decided, so the caller can carry it out and the decision
// itself stays free of side effects.
type Plan struct {
	// Slot is the slot to hand over. Always set when Acquire returns no error.
	Slot *Slot
	// Reuse: the slot already exists on the right branch, do nothing to disk.
	Reuse bool
	// Create: no worktree exists at this index yet.
	Create bool
	// Recycle: a worktree exists and must be torn down first.
	Recycle bool
	// Evicted is what Recycle is about to destroy, for the record.
	Evicted string
}

// Acquire decides which slot an owner should get, without touching anything.
//
// The order is deliberate: reuse before create, create before recycle. Recycle
// is last because it is the only one that destroys something, and the least
// recently used idle slot is the one whose loss costs least.
func (p *Pool) Acquire(branch, owner string, pid int, safe Safety, now time.Time) (*Plan, error) {
	if branch == "" {
		return nil, errors.New("a branch is required")
	}

	if s := p.OnBranch(branch); s != nil {
		lease(s, owner, pid, now)
		return &Plan{Slot: s, Reuse: true}, nil
	}

	if index, ok := p.freeIndex(); ok {
		s := &Slot{Index: index, Repo: p.Repo, Branch: branch, Created: now}
		lease(s, owner, pid, now)
		p.Slots = append(p.Slots, s)
		return &Plan{Slot: s, Create: true}, nil
	}

	victim, blockers, err := p.victim(safe)
	if err != nil {
		return nil, err
	}
	if victim == nil {
		return nil, &ErrFull{Blockers: blockers}
	}

	evicted := victim.Path
	victim.Branch = branch
	victim.Path = ""
	lease(victim, owner, pid, now)
	return &Plan{Slot: victim, Recycle: true, Evicted: evicted}, nil
}

// victim picks the least recently used slot that is safe to destroy. It returns
// the reasons the others were spared so a full pool can explain itself.
func (p *Pool) victim(safe Safety) (*Slot, []string, error) {
	idle := make([]*Slot, 0, len(p.Slots))
	blockers := make([]string, 0, len(p.Slots))

	for _, s := range p.Slots {
		if !s.Recyclable() {
			blockers = append(blockers, fmt.Sprintf("%d: %s", s.Index, s.Blocker()))
			continue
		}
		idle = append(idle, s)
	}

	// Oldest first, so the slot nobody has touched goes before one used a
	// minute ago.
	sort.Slice(idle, func(a, b int) bool { return idle[a].Used.Before(idle[b].Used) })

	for _, s := range idle {
		reason, err := Unsafe(safe, s.Path)
		if err != nil {
			// Not knowing is not the same as being safe. Refuse the whole
			// operation rather than guess about a directory we are about to
			// delete.
			return nil, nil, err
		}
		if reason == "" {
			return s, blockers, nil
		}
		blockers = append(blockers, fmt.Sprintf("%d: %s", s.Index, reason))
	}
	return nil, blockers, nil
}

func lease(s *Slot, owner string, pid int, now time.Time) {
	s.State = Leased
	s.Owner = owner
	s.OwnerPID = pid
	s.Used = now
}

// Release hands a slot back. The worktree stays on disk: that is the whole
// point of a pool, and the next acquire on the same branch will find it.
func (p *Pool) Release(index int, now time.Time) error {
	s := p.Find(index)
	if s == nil {
		return fmt.Errorf("no slot %d", index)
	}
	if s.State == Pinned {
		return nil
	}
	s.State = Idle
	s.Owner = ""
	s.OwnerPID = 0
	s.Used = now
	return nil
}
