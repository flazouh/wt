// Package pool holds the rules about what a worktree pool is and what may be
// done to it. It knows nothing about git, about the filesystem, or about how
// state is stored, which is what makes the rules testable without a repository
// on disk.
package pool

import "time"

// Limit is how many worktrees one repository may have at once.
//
// Six, because the cost of a worktree is real. The forty-eight that prompted
// this tool averaged 850 MB each, and together they blocked three releases in
// one afternoon by holding the machine under the twelve gigabyte floor the
// build needs. It was five until parallel agent work kept every slot leased.
const Limit = 6

// State is what a slot is doing, which decides what may be done to it.
type State string

const (
	// Idle: created, nobody holds it, safe to hand out or to recycle.
	Idle State = "idle"
	// Leased: an agent or a person is working in it.
	Leased State = "leased"
	// Pinned: never recycled, whatever its age. For work in progress that
	// outlives a session.
	Pinned State = "pinned"
)

// Slot is one worktree the pool owns.
//
// Index is stable for the life of the slot and appears in the worktree's path,
// so a directory on disk can always be traced back to a registry entry without
// consulting anything else.
type Slot struct {
	Index    int       `json:"index"`
	Repo     string    `json:"repo"`
	Path     string    `json:"path"`
	Branch   string    `json:"branch"`
	State    State     `json:"state"`
	Owner    string    `json:"owner,omitempty"`
	OwnerPID int       `json:"ownerPid,omitempty"`
	Created  time.Time `json:"created"`
	Used     time.Time `json:"used"`
}

// Recyclable reports whether a slot may be torn down to make room.
//
// This is deliberately only the half of the question the pool can answer on its
// own. The other half — is a process sitting in it, is it dirty, does it hold
// commits nobody else has — is asked of the world by the caller, because
// getting those wrong destroys work and getting this wrong does not.
func (s Slot) Recyclable() bool {
	return s.State == Idle
}

// Blocker explains, in one phrase, why a slot cannot be recycled. Empty when it
// can. The wording is what an agent reads when the pool is full, so it names
// the thing to do about it rather than restating the state.
func (s Slot) Blocker() string {
	switch s.State {
	case Leased:
		return "leased by " + s.Owner
	case Pinned:
		return "pinned"
	default:
		return ""
	}
}
