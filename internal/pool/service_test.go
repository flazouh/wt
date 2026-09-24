package pool

import (
	"errors"
	"testing"
	"time"
)

// fakeGit records what was asked of it so a test can assert on the order of
// destruction and creation, which is where a recycle goes wrong.
type fakeGit struct {
	trees   []Tree
	removed []string
	added   []string
	addErr  error
	remErr  error
}

func (f *fakeGit) Add(path, branch, _ string) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.added = append(f.added, path+"@"+branch)
	f.trees = append(f.trees, Tree{Path: path, Branch: branch})
	return nil
}

func (f *fakeGit) Remove(path string) error {
	if f.remErr != nil {
		return f.remErr
	}
	f.removed = append(f.removed, path)
	kept := f.trees[:0]
	for _, t := range f.trees {
		if t.Path != path {
			kept = append(kept, t)
		}
	}
	f.trees = kept
	return nil
}

func (f *fakeGit) DefaultBase() string   { return "origin/main" }
func (f *fakeGit) Fetch() error          { return nil }
func (f *fakeGit) List() ([]Tree, error) { return f.trees, nil }

func service(t *testing.T, p *Pool, g *fakeGit, safe Safety) *Service {
	t.Helper()
	return &Service{
		Pool:   p,
		Git:    g,
		Safety: safe,
		Root:   t.TempDir(),
		Now:    func() time.Time { return epoch },
	}
}

func TestAcquireCreatesTheWorktreeOnDisk(t *testing.T) {
	g := &fakeGit{}
	s := service(t, &Pool{Repo: "/repo"}, g, safeWorld{})

	slot, action, err := s.Acquire("feature/x", "agent", 1)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	if action != "created" {
		t.Fatalf("action %q, want created", action)
	}
	if len(g.added) != 1 {
		t.Fatalf("added %v, want one worktree", g.added)
	}
	if slot.Path == "" {
		t.Fatal("the slot was handed over without a path")
	}
}

// The old worktree must be gone before the new one is built at the same index,
// or git refuses and the slot is left leased with nothing in it.
func TestRecycleRemovesBeforeItAdds(t *testing.T) {
	p := full(t)
	g := &fakeGit{}
	for _, slot := range p.Slots {
		g.trees = append(g.trees, Tree{Path: slot.Path, Branch: slot.Branch})
	}
	s := service(t, p, g, safeWorld{})

	_, action, err := s.Acquire("feature/new", "agent", 1)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	if action != "recycled" {
		t.Fatalf("action %q, want recycled", action)
	}
	if len(g.removed) != 1 || g.removed[0] != "/repo/.wt/1" {
		t.Fatalf("removed %v, want the least recently used", g.removed)
	}
	if len(g.added) != 1 {
		t.Fatalf("added %v, want exactly one", g.added)
	}
}

// A failed removal must abort. Building the new worktree anyway would leave the
// old one orphaned and the pool over its own limit.
func TestRecycleStopsWhenTheOldWorktreeWillNotGo(t *testing.T) {
	p := full(t)
	g := &fakeGit{remErr: errors.New("worktree is locked")}
	s := service(t, p, g, safeWorld{})

	_, _, err := s.Acquire("feature/new", "agent", 1)

	if err == nil {
		t.Fatal("acquire succeeded despite the removal failing")
	}
	if len(g.added) != 0 {
		t.Fatalf("built %v after failing to remove the old worktree", g.added)
	}
}

func TestReuseTouchesNothingOnDisk(t *testing.T) {
	p := full(t)
	p.Slots[1].Branch = "feature/x"
	g := &fakeGit{}
	s := service(t, p, g, safeWorld{})

	_, action, err := s.Acquire("feature/x", "agent", 1)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	if action != "reused" {
		t.Fatalf("action %q, want reused", action)
	}
	if len(g.added) != 0 || len(g.removed) != 0 {
		t.Fatalf("reuse touched disk: added %v removed %v", g.added, g.removed)
	}
}

func TestReconcileForgetsWorktreesRemovedByHand(t *testing.T) {
	p := full(t)
	g := &fakeGit{}
	// Only two of the full pool still exist.
	g.trees = []Tree{
		{Path: "/repo", Main: true},
		{Path: p.Slots[0].Path},
		{Path: p.Slots[1].Path},
	}
	s := service(t, p, g, safeWorld{})

	dropped, err := s.Reconcile()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if dropped != Limit-2 {
		t.Fatalf("dropped %d slots, want %d", dropped, Limit-2)
	}
	if len(p.Slots) != 2 {
		t.Fatalf("pool holds %d slots, want 2", len(p.Slots))
	}
}

// A slot the pool never filled in has no path yet. Reconcile must not mistake
// that for a worktree someone deleted.
func TestReconcileKeepsASlotThatHasNoWorktreeYet(t *testing.T) {
	p := &Pool{Repo: "/repo", Slots: []*Slot{{Index: 1, State: Leased}}}
	s := service(t, p, &fakeGit{trees: []Tree{{Path: "/repo", Main: true}}}, safeWorld{})

	dropped, err := s.Reconcile()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if dropped != 0 || len(p.Slots) != 1 {
		t.Fatalf("dropped %d, kept %d; a pending slot was discarded", dropped, len(p.Slots))
	}
}

func TestStraysAreEverythingThePoolDidNotCreate(t *testing.T) {
	p := full(t)
	g := &fakeGit{trees: []Tree{
		{Path: "/repo", Main: true},
		{Path: p.Slots[0].Path},
		{Path: "/repo/.worktrees/codex/something", Branch: "codex/x"},
	}}
	s := service(t, p, g, safeWorld{})

	strays, err := s.Strays()
	if err != nil {
		t.Fatalf("strays: %v", err)
	}

	if len(strays) != 1 {
		t.Fatalf("found %d strays, want 1: %v", len(strays), strays)
	}
	if strays[0].Path != "/repo/.worktrees/codex/something" {
		t.Fatalf("stray %q is wrong", strays[0].Path)
	}
}

func TestTheMainCheckoutIsNeverAStray(t *testing.T) {
	s := service(t, &Pool{Repo: "/repo"}, &fakeGit{trees: []Tree{{Path: "/repo", Main: true}}}, safeWorld{})

	strays, err := s.Strays()
	if err != nil {
		t.Fatalf("strays: %v", err)
	}

	if len(strays) != 0 {
		t.Fatalf("reported the main checkout as a stray: %v", strays)
	}
}
