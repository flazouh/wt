package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/flazouh/wt/internal/pool"
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

	p := s.reg.For(s.git.Repo)
	d.Section("pool", map[string]any{
		"repo":  collapseHome(s.git.Repo),
		"used":  len(p.Slots),
		"limit": p.Cap(),
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

// age renders how long a slot has sat untouched, which is what decides who gets
// recycled first.
func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
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
