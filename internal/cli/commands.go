package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alexdepape/wt/internal/gitwt"
	"github.com/alexdepape/wt/internal/liveness"
	"github.com/alexdepape/wt/internal/pool"
	"github.com/alexdepape/wt/internal/registry"
	"github.com/alexdepape/wt/internal/toon"
)

var commands = map[string]command{}

func init() {
	commands["take"] = command{
		name:    "take",
		summary: "Lease a worktree for a branch, creating or recycling one",
		usage:   "wt take <branch> [--owner <name>] [--pin]",
		flags:   map[string]flagKind{"owner": valueFlag, "pin": boolFlag},
		examples: []string{
			`wt take feature/login`,
			`wt take fix/crash --owner codex`,
		},
		run: take,
	}
	commands["done"] = command{
		name:     "done",
		summary:  "Hand a worktree back, keeping it on disk for reuse",
		usage:    "wt done [<index>]",
		flags:    map[string]flagKind{},
		examples: []string{`wt done`, `wt done 3`},
		run:      done,
	}
	commands["drop"] = command{
		name:     "drop",
		summary:  "Remove a worktree and free its slot",
		usage:    "wt drop <index>",
		flags:    map[string]flagKind{},
		examples: []string{`wt drop 2`},
		run:      drop,
	}
	commands["pin"] = command{
		name:     "pin",
		summary:  "Protect a slot from recycling, or release that protection",
		usage:    "wt pin <index> [--off]",
		flags:    map[string]flagKind{"off": boolFlag},
		examples: []string{`wt pin 1`, `wt pin 1 --off`},
		run:      pin,
	}
	commands["strays"] = command{
		name:    "strays",
		summary: "List worktrees the pool did not create, and reclaim them",
		usage:   "wt strays [--archive] [--apply]",
		flags:   map[string]flagKind{"apply": boolFlag, "archive": boolFlag},
		examples: []string{
			`wt strays`,
			`wt strays --apply`,
			`wt strays --archive`,
			`wt strays --archive --apply`,
		},
		run: strays,
	}
	commands["enforce"] = command{
		name:    "enforce",
		summary: "Install the git shim that makes the cap real",
		usage:   "wt enforce [--install|--uninstall]",
		flags:   map[string]flagKind{"install": boolFlag, "uninstall": boolFlag},
		examples: []string{
			`wt enforce`,
			`wt enforce --install`,
		},
		run: enforce,
	}
}

// session builds everything a command needs against the repository the caller
// is standing in.
type session struct {
	store *registry.Store
	git   *gitwt.Git
	svc   *pool.Service
	reg   *registry.Registry
}

func open() (*session, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	g, err := gitwt.Open(cwd)
	if err != nil {
		return nil, err
	}
	store, err := registry.Open()
	if err != nil {
		return nil, err
	}
	reg, err := store.Read()
	if err != nil {
		return nil, err
	}
	p := reg.For(g.Repo)
	return &session{
		store: store,
		git:   g,
		reg:   reg,
		svc: &pool.Service{
			Pool:   p,
			Git:    adapter{g},
			Safety: safety{g, liveness.New(), gitwt.NewMerged()},
			Root:   poolRoot(g.Repo),
			Now:    time.Now,
		},
	}, nil
}

// poolRoot keeps every pooled worktree in one directory beside the checkout, so
// the pool's footprint is one `du` away and never interleaves with the strays
// already scattered under .worktrees.
func poolRoot(repo string) string {
	return filepath.Join(filepath.Dir(repo), filepath.Base(repo)+".wt")
}

// adapter narrows *gitwt.Git to what the pool needs.
type adapter struct{ g *gitwt.Git }

func (a adapter) Add(path, branch, base string) error { return a.g.Add(path, branch, base) }
func (a adapter) Remove(path string) error            { return a.g.Remove(path) }
func (a adapter) DefaultBase() string                 { return a.g.DefaultBase() }
func (a adapter) Fetch() error                        { return a.g.Fetch() }
func (a adapter) List() ([]pool.Tree, error) {
	trees, err := a.g.List()
	if err != nil {
		return nil, err
	}
	out := make([]pool.Tree, len(trees))
	for i, t := range trees {
		out[i] = pool.Tree{Path: t.Path, Branch: t.Branch, Main: t.Main}
	}
	return out, nil
}

// safety joins the sources of truth about whether a worktree may be destroyed:
// the machine's process table, the repository, and the pull requests.
type safety struct {
	g      *gitwt.Git
	probe  *liveness.Probe
	merged *gitwt.Merged
}

func (s safety) InUse(path string) (bool, error) { return s.probe.InUse(path) }
func (s safety) Dirty(path string) (bool, error) { return s.g.Dirty(path) }

// Unpushed asks git, then forgives the one case git cannot see.
//
// A squash-merged branch has commits that exist on no other ref, because the
// squash rewrote them into a single new commit. Reachability says the work is
// unique; the pull request says it is in main. The pull request is right.
func (s safety) Unpushed(path string) (bool, error) {
	unpushed, err := s.g.Unpushed(path)
	if err != nil || !unpushed {
		return unpushed, err
	}
	if s.merged.Is(s.g.Branch(path)) {
		return false, nil
	}
	return true, nil
}

func take(w io.Writer, flags map[string]string, rest []string) int {
	if len(rest) == 0 {
		return errorf(w, Usage, "take needs a branch", "usage: wt take <branch>")
	}
	branch := rest[0]

	s, err := open()
	if err != nil {
		return errorf(w, Fail, err.Error(), "run `wt` from inside a git repository")
	}

	owner := flags["owner"]
	if owner == "" {
		owner = defaultOwner()
	}

	var slot *pool.Slot
	var action string
	err = s.store.Update(func(reg *registry.Registry) error {
		s.svc.Pool = reg.For(s.git.Repo)
		if _, err := s.svc.Reconcile(); err != nil {
			return err
		}
		var err error
		slot, action, err = s.svc.Acquire(branch, owner, os.Getpid())
		if err != nil {
			return err
		}
		if flags["pin"] == "true" {
			slot.State = pool.Pinned
		}
		return nil
	})
	if err != nil {
		return takeFailed(w, err)
	}

	var d toon.Doc
	d.Section("worktree", map[string]any{
		"path":   slot.Path,
		"branch": slot.Branch,
		"slot":   slot.Index,
		"action": action,
	})
	d.Help(
		"cd "+slot.Path,
		fmt.Sprintf("Run `wt done %d` when finished, which keeps it for reuse", slot.Index),
	)
	_, _ = d.WriteTo(w)
	return OK
}

// takeFailed turns the two interesting failures into advice. A full pool names
// the leases to end; a broken liveness probe says the machine cannot be
// inspected rather than pretending the pool is full.
func takeFailed(w io.Writer, err error) int {
	var full *pool.ErrFull
	if errors.As(err, &full) {
		var d toon.Doc
		d.Field("error", fmt.Sprintf("the pool is full: %d of %d slots", pool.Limit, pool.Limit))
		rows := make([][]any, 0, len(full.Blockers))
		for _, b := range full.Blockers {
			rows = append(rows, []any{b})
		}
		d.Table("blocked", []string{"reason"}, rows)
		d.Help(
			"Run `wt done <index>` to release one you own",
			"Run `wt drop <index>` to remove one for good",
			"Run `wt` to see every slot",
		)
		_, _ = d.WriteTo(w)
		return Fail
	}
	if errors.Is(err, liveness.ErrProbeUnavailable) {
		return errorf(w, Fail,
			"cannot tell which worktrees are in use, so nothing will be recycled",
			"this needs lsof; nothing was changed",
			"Run `wt drop <index>` to remove a specific slot yourself")
	}
	return errorf(w, Fail, err.Error())
}

func defaultOwner() string {
	for _, key := range []string{"CLAUDE_SESSION_ID", "CODEX_SESSION_ID", "USER"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return "unknown"
}

func done(w io.Writer, _ map[string]string, rest []string) int {
	s, err := open()
	if err != nil {
		return errorf(w, Fail, err.Error())
	}

	index, err := targetIndex(s, rest)
	if err != nil {
		return errorf(w, Usage, err.Error(), "usage: wt done [<index>]")
	}

	err = s.store.Update(func(reg *registry.Registry) error {
		return reg.For(s.git.Repo).Release(index, time.Now())
	})
	if err != nil {
		return errorf(w, Fail, err.Error(), "Run `wt` to see the pool")
	}

	var d toon.Doc
	d.Field("released", index)
	d.Field("kept", "the worktree stays on disk for the next take on this branch")
	_, _ = d.WriteTo(w)
	return OK
}

// targetIndex resolves an explicit index, or infers the slot the caller is
// standing in, which is the common case and saves them looking it up.
func targetIndex(s *session, rest []string) (int, error) {
	if len(rest) > 0 {
		index, err := strconv.Atoi(rest[0])
		if err != nil {
			return 0, fmt.Errorf("%q is not a slot index", rest[0])
		}
		return index, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 0, err
	}
	for _, slot := range s.reg.For(s.git.Repo).Slots {
		if slot.Path != "" && strings.HasPrefix(cwd, slot.Path) {
			return slot.Index, nil
		}
	}
	return 0, errors.New("not inside a pooled worktree, so the slot must be named")
}

func drop(w io.Writer, _ map[string]string, rest []string) int {
	s, err := open()
	if err != nil {
		return errorf(w, Fail, err.Error())
	}
	index, err := targetIndex(s, rest)
	if err != nil {
		return errorf(w, Usage, err.Error(), "usage: wt drop <index>")
	}

	var removed string
	err = s.store.Update(func(reg *registry.Registry) error {
		p := reg.For(s.git.Repo)
		slot := p.Find(index)
		if slot == nil {
			// Already gone is the state the caller wanted. Not an error.
			return nil
		}
		reason, err := pool.Unsafe(s.svc.Safety, slot.Path)
		if err != nil {
			return err
		}
		if reason != "" {
			return fmt.Errorf("slot %d is not safe to drop: %s", index, reason)
		}
		if err := s.git.Remove(slot.Path); err != nil {
			return err
		}
		removed = slot.Path
		kept := p.Slots[:0]
		for _, other := range p.Slots {
			if other.Index != index {
				kept = append(kept, other)
			}
		}
		p.Slots = kept
		return nil
	})
	if err != nil {
		return errorf(w, Fail, err.Error(), "Run `wt` to see why")
	}

	var d toon.Doc
	if removed == "" {
		d.Field("dropped", fmt.Sprintf("slot %d was already free (no-op)", index))
	} else {
		d.Field("dropped", removed)
	}
	_, _ = d.WriteTo(w)
	return OK
}

func pin(w io.Writer, flags map[string]string, rest []string) int {
	s, err := open()
	if err != nil {
		return errorf(w, Fail, err.Error())
	}
	index, err := targetIndex(s, rest)
	if err != nil {
		return errorf(w, Usage, err.Error(), "usage: wt pin <index> [--off]")
	}

	off := flags["off"] == "true"
	err = s.store.Update(func(reg *registry.Registry) error {
		slot := reg.For(s.git.Repo).Find(index)
		if slot == nil {
			return fmt.Errorf("no slot %d", index)
		}
		if off {
			slot.State = pool.Idle
			return nil
		}
		slot.State = pool.Pinned
		return nil
	})
	if err != nil {
		return errorf(w, Fail, err.Error(), "Run `wt` to see the pool")
	}

	var d toon.Doc
	if off {
		d.Field("unpinned", index)
	} else {
		d.Field("pinned", index)
		d.Field("effect", "this slot is never recycled, whatever its age")
	}
	_, _ = d.WriteTo(w)
	return OK
}
