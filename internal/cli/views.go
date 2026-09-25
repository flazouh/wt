package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/flazouh/wt/internal/pool"
	"github.com/flazouh/wt/internal/registry"
	"github.com/flazouh/wt/internal/shim"
	"github.com/flazouh/wt/internal/toon"
)

// home is what `wt` with no arguments prints: the live pool for the repository
// the caller is standing in, so an agent can act without a second call.
func home(w io.Writer) int {
	var d toon.Doc
	d.Field("bin", collapseHome(binPath()))
	d.Field("description", "A capped, machine-wide pool of git worktrees")

	s, err := open()
	if err != nil {
		d.Field("error", err.Error())
		d.Help("Run `wt` from inside a git repository")
		_, _ = d.WriteTo(w)
		return Fail
	}

	p, released, err := reconciled(s)
	if err != nil {
		// The snapshot is still worth showing. It may count a slot that is
		// gone or a lease that is dead, and the warning says it was not
		// checked rather than letting it pass as current.
		d.Field("warning", "the pool was not reconciled, so this is the last saved state: "+err.Error())
	}
	d.Section("pool", map[string]any{
		"repo":  collapseHome(s.git.Repo),
		"used":  len(p.Slots),
		"limit": pool.Limit,
	})

	rows := make([][]any, 0, len(p.Slots))
	for _, slot := range p.Slots {
		rows = append(rows, []any{
			slot.Index,
			slot.Branch,
			slot.State,
			age(slot.Used),
			collapseHome(slot.Path),
		})
	}
	d.Table("slots", []string{"slot", "branch", "state", "idle", "path"}, rows)
	releasedTable(&d, released)

	// Strays are the part the cap cannot see, so the home view says how many
	// there are rather than leaving an agent to discover them.
	if strays, err := s.svc.Strays(); err == nil && len(strays) > 0 {
		d.Field("strays", fmt.Sprintf("%d worktrees this pool did not create", len(strays)))
	}

	if st, err := shim.Inspect(); err == nil && !st.Effective {
		d.Field("enforcement", "off: `git worktree add` still works outside the pool")
	}

	d.Help(homeHelp(p)...)
	_, _ = d.WriteTo(w)
	return OK
}

func homeHelp(p *pool.Pool) []string {
	if len(p.Slots) == 0 {
		return []string{
			`Run ` + "`wt take <branch>`" + ` to get a worktree`,
			"Run `wt strays` to see worktrees created outside the pool",
		}
	}
	return []string{
		`Run ` + "`wt take <branch>`" + ` to lease one`,
		"Run `wt done <slot>` to hand one back",
		"Run `wt strays` to see worktrees created outside the pool",
	}
}

// reconciled brings the pool up to date under the registry lock and returns it,
// with the leases that were taken back on the way.
//
// The listing writes, which a listing usually should not, because it is where a
// person looks when the pool is full. Showing six leases when one of them
// belongs to a session that died days ago sends them chasing an owner who is
// not there. An empty pool is left unwritten, so looking at a repository that
// has never used `wt` does not add it to the registry.
//
// On failure it returns the unlocked snapshot, so the caller can still show
// something.
func reconciled(s *session) (*pool.Pool, []pool.Released, error) {
	snapshot := s.reg.For(s.git.Repo)
	if len(snapshot.Slots) == 0 {
		return snapshot, nil, nil
	}
	var p *pool.Pool
	var released []pool.Released
	err := s.store.Update(func(reg *registry.Registry) error {
		s.svc.Pool = reg.For(s.git.Repo)
		r, err := s.svc.Reconcile()
		if err != nil {
			return err
		}
		p, released = s.svc.Pool, r.Released
		return nil
	})
	if err != nil {
		s.svc.Pool = snapshot
		return snapshot, nil, err
	}
	return p, released, nil
}

// releasedTable reports the leases the pool took back as abandoned, and writes
// nothing when there were none. An agent whose lease vanished without a word
// would take it for a bug; one that reads this knows who held it and why it
// was judged gone.
func releasedTable(d *toon.Doc, released []pool.Released) {
	if len(released) == 0 {
		return
	}
	rows := make([][]any, 0, len(released))
	for _, r := range released {
		rows = append(rows, []any{
			r.Index,
			r.Owner,
			"lease released: idle " + span(r.Idle) + ", no live process",
		})
	}
	d.Table("released", []string{"slot", "owner", "why"}, rows)
}

// age renders how long a slot has sat untouched, which is what decides who gets
// recycled first.
func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return span(time.Since(t))
}

// span renders a duration at the one unit a person reads it in.
func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// enforce reports or changes the git shim, which is the only thing that makes
// the cap binding rather than advisory.
func enforce(w io.Writer, flags map[string]string, _ []string) int {
	install := flags["install"] == "true"
	uninstall := flags["uninstall"] == "true"

	if install && uninstall {
		return errorf(w, Usage, "--install and --uninstall are opposites",
			"usage: wt enforce [--install|--uninstall]")
	}

	if uninstall {
		if err := shim.Uninstall(); err != nil {
			return errorf(w, Fail, err.Error())
		}
		var d toon.Doc
		d.Field("enforcement", "off")
		d.Field("effect", "`git worktree add` works again everywhere")
		_, _ = d.WriteTo(w)
		return OK
	}

	st, err := shim.Inspect()
	if err != nil {
		return errorf(w, Fail, err.Error())
	}

	if install {
		st, err = shim.Install()
		if err != nil {
			return errorf(w, Fail, err.Error(),
				"the shim is only written when nothing else owns that path")
		}
	}

	var d toon.Doc
	d.Section("enforcement", map[string]any{
		"installed": st.Installed,
		"active":    st.Effective,
		"shim":      collapseHome(st.Path),
		"git":       collapseHome(st.Real),
	})

	switch {
	case !st.Installed:
		d.Help(
			"Run `wt enforce --install` to refuse `git worktree add` outside the pool",
			"Until then the cap is advisory",
		)
	case !st.Effective:
		d.Field("warning", "the shim is installed but another git comes first on PATH")
		d.Help("put " + collapseHome(strings.TrimSuffix(st.Path, "/git")) + " earlier in PATH")
	default:
		d.Field("escape", "WT_POOL=1 git worktree add ... still works, for this tool's own use")
	}
	_, _ = d.WriteTo(w)
	return OK
}
