package pool

import "testing"

// The order Unsafe asks its questions is what makes an archive sweep safe, so
// it is held here rather than left to the reading order of a slice literal. A
// worktree that is both dirty and unpushed must report dirty: report unpushed
// and the sweep would push its commits, call it solved, and delete the edits
// that were never committed in the first place.
func TestUnsafeAsksTheIrreversibleQuestionsFirst(t *testing.T) {
	both := guarded{
		dirty:    map[string]bool{"/w": true},
		unpushed: map[string]bool{"/w": true},
	}
	reason, err := Unsafe(both, "/w")
	if err != nil {
		t.Fatalf("unsafe: %v", err)
	}
	if reason != ReasonDirty {
		t.Fatalf("reason %q, want %q; an archive sweep would delete uncommitted work", reason, ReasonDirty)
	}
	if Archivable(reason) {
		t.Error("a dirty worktree reads as archivable")
	}

	running := guarded{
		inUse:    map[string]bool{"/w": true},
		unpushed: map[string]bool{"/w": true},
	}
	reason, err = Unsafe(running, "/w")
	if err != nil {
		t.Fatalf("unsafe: %v", err)
	}
	if reason != ReasonInUse {
		t.Fatalf("reason %q, want %q; an archive sweep would delete a live workspace", reason, ReasonInUse)
	}
	if Archivable(reason) {
		t.Error("a worktree with a process in it reads as archivable")
	}
}

func TestOnlyUnpushedWorkIsArchivable(t *testing.T) {
	for reason, want := range map[Reason]bool{
		Safe:           false,
		ReasonInUse:    false,
		ReasonDirty:    false,
		ReasonUnpushed: true,
	} {
		if got := Archivable(reason); got != want {
			t.Errorf("Archivable(%q) = %v, want %v", reason, got, want)
		}
	}
}

func TestTriageSeparatesTheThreeOutcomes(t *testing.T) {
	world := guarded{
		inUse:    map[string]bool{"/busy": true},
		dirty:    map[string]bool{"/dirty": true},
		unpushed: map[string]bool{"/abandoned": true},
	}
	trees := []Tree{
		{Path: "/free", Branch: "a"},
		{Path: "/busy", Branch: "b"},
		{Path: "/dirty", Branch: "c"},
		{Path: "/abandoned", Branch: "d"},
	}

	strays, err := Triage(world, trees)
	if err != nil {
		t.Fatalf("triage: %v", err)
	}
	if len(strays) != len(trees) {
		t.Fatalf("triaged %d of %d strays", len(strays), len(trees))
	}

	byPath := map[string]Stray{}
	for _, s := range strays {
		byPath[s.Tree.Path] = s
	}
	if !byPath["/free"].Reclaimable() || byPath["/free"].Archivable() {
		t.Error("a worktree with no blocker is not plainly reclaimable")
	}
	for _, path := range []string{"/busy", "/dirty"} {
		s := byPath[path]
		if s.Reclaimable() || s.Archivable() {
			t.Errorf("%s reads as touchable: %q", path, s.Reason)
		}
	}
	abandoned := byPath["/abandoned"]
	if abandoned.Reclaimable() || !abandoned.Archivable() {
		t.Errorf("/abandoned reads as %q, want archivable", abandoned.Reason)
	}
}

// A machine that cannot answer for one worktree has not answered for any of
// them. Returning the rest as classified would hand an --apply a list built on
// a question nobody actually asked.
func TestTriageRefusesTheWholeSweepWhenOneAnswerIsMissing(t *testing.T) {
	strays, err := Triage(blind{}, []Tree{{Path: "/a"}, {Path: "/b"}})
	if err == nil {
		t.Fatal("triage returned a verdict for a machine it could not inspect")
	}
	if strays != nil {
		t.Errorf("triage returned %d classifications alongside its error", len(strays))
	}
}
