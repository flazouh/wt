package pool

import "time"

// StaleLease is how long a lease may sit with no sign of life before the pool
// takes it back.
//
// Forty-eight hours, because the failure it exists for was measured in days: the
// pool sat at six of six for more than two days with every slot "leased by
// alex", each held by a session that had long since died, and every take
// refused. A lease is a promise the holder never has to renew, so a holder that
// crashes, is killed, or simply forgets `wt done` holds its slot for ever. Two
// days is long past any working session and any overnight pause, and short
// enough that a wedged pool heals before anybody has to notice it.
//
// The threshold is judged against the latest sign of life, not the lease's own
// stamp, and it is only ever half of the decision. See ReleaseStale.
const StaleLease = 48 * time.Hour

// Activity answers the two questions a stale lease is judged by. The pool asks
// them and never answers them, for the same reason it never answers Safety.
type Activity interface {
	// LastTouched reports the last time anyone did something git can see in
	// the worktree: staged a file, committed. An implementation that cannot
	// tell must return an error, which keeps the lease.
	LastTouched(path string) (time.Time, error)
	// InUse reports whether a live process is sitting in the path. An error
	// keeps the lease: "the probe failed" is never "nothing is there".
	InUse(path string) (bool, error)
}

// Released is one lease the pool took back, for the caller to report. A lease
// that vanishes without a word looks, to the agent that held it, like a bug.
type Released struct {
	Index int
	Owner string
	// Idle is how long before now the last sign of life was.
	Idle time.Duration
}

// ReleaseStale takes back every lease whose holder has plainly gone away, and
// reports which.
//
// A lease is taken back only when all of these hold:
//
//   - it is a lease, not a pin: a pin is the explicit "keep this whatever its
//     age", so no amount of silence overrides it;
//   - it has a worktree: a lease with no path is an acquire that stopped part
//     way, and a slot whose directory is in an unknown state must not be handed
//     to anyone else;
//   - the latest sign of life is more than StaleLease ago, where the signs are
//     the lease's own stamp and whatever git last saw happen in the worktree;
//   - no live process is working in it.
//
// Git's view is asked because the stamp alone lies. An agent session often
// works in a worktree through absolute paths while its own working directory is
// somewhere else, so it never refreshes the lease and the process probe cannot
// see it either. The index and HEAD move whenever anyone stages or commits
// there, whoever they are and wherever they stand.
//
// Every unanswered question keeps the lease. Keeping one wrongly costs a slot
// until the next check; releasing one wrongly hands a live agent's worktree to
// someone else.
//
// Releasing is all this does. The slot becomes idle and nothing on disk is
// touched; being torn down or recycled still takes every Safety question,
// answered no, exactly as for a slot released by `wt done`. The stamp is set to
// the last sign of life rather than to now, so the abandoned slot is first in
// line to be recycled instead of last.
//
// The stamp is checked first, and alone, because it costs nothing: it is a
// lower bound on the last sign of life, so a lease used within the threshold is
// fresh whatever git says, and neither git nor the second-long process probe is
// asked about it.
func (p *Pool) ReleaseStale(world Activity, now time.Time) []Released {
	var released []Released
	for _, s := range p.Slots {
		if s.State != Leased || s.Path == "" {
			continue
		}
		last := s.Used
		if now.Sub(last) <= StaleLease {
			continue
		}

		touched, err := world.LastTouched(s.Path)
		if err != nil {
			continue
		}
		if touched.After(last) {
			last = touched
		}
		if now.Sub(last) <= StaleLease {
			continue
		}

		inUse, err := world.InUse(s.Path)
		if err != nil || inUse {
			continue
		}

		released = append(released, Released{Index: s.Index, Owner: s.Owner, Idle: now.Sub(last)})
		s.State = Idle
		s.Owner = ""
		s.OwnerPID = 0
		s.Used = last
	}
	return released
}
