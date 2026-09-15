package pool

// Stray is one worktree outside the pool, with the verdict already reached.
//
// The verdict is reached once, for every stray, in a single pass, because the
// two things a sweep does with it — report and act — must agree. A dry run that
// classifies a worktree one way and an --apply that classifies it another is
// worse than no dry run at all.
type Stray struct {
	Tree   Tree
	Reason Reason
}

// Reclaimable reports whether the worktree can be removed as it stands.
func (s Stray) Reclaimable() bool { return s.Reason == Safe }

// Archivable reports whether preserving its commits would make it reclaimable.
func (s Stray) Archivable() bool { return Archivable(s.Reason) }

// Triage classifies every stray.
//
// One unanswerable question fails the whole sweep rather than the one worktree.
// Reporting the rest as safe would invite an --apply that deletes the wrong
// thing on the strength of an answer nobody actually gave.
func Triage(safe Safety, trees []Tree) ([]Stray, error) {
	strays := make([]Stray, 0, len(trees))
	for _, t := range trees {
		reason, err := Unsafe(safe, t.Path)
		if err != nil {
			return nil, err
		}
		strays = append(strays, Stray{Tree: t, Reason: reason})
	}
	return strays, nil
}
