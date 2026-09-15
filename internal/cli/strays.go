package cli

import (
	"fmt"
	"io"

	"github.com/flazouh/wt/internal/gitwt"
	"github.com/flazouh/wt/internal/pool"
	"github.com/flazouh/wt/internal/toon"
)

// strays lists worktrees the pool never created and, with --apply, reclaims the
// ones that are safe to remove. With --archive it first preserves the work that
// would otherwise be the only thing stopping them.
//
// Dry by default, in both modes. Every one of these directories may hold work,
// and the tool that deletes forty of them without being asked twice is not one
// to trust.
func strays(w io.Writer, flags map[string]string, _ []string) int {
	s, err := open()
	if err != nil {
		return errorf(w, Fail, err.Error())
	}

	found, err := s.svc.Strays()
	if err != nil {
		return errorf(w, Fail, err.Error())
	}
	if len(found) == 0 {
		var d toon.Doc
		d.Field("strays", "0 worktrees outside the pool in this repository")
		_, _ = d.WriteTo(w)
		return OK
	}

	triaged, err := pool.Triage(s.svc.Safety, found)
	if err != nil {
		// One unanswerable question stops the whole sweep. Reporting the rest
		// as safe would invite an --apply that deletes the wrong thing.
		//
		// The cause names which of the three questions went unanswered, which
		// is the difference between a missing lsof and a repository git cannot
		// read, and those have different fixes.
		return errorf(w, Fail,
			"cannot answer every safety question, so nothing was pushed or removed",
			"cause: "+err.Error(),
			"the process check needs lsof; the other two need git")
	}

	apply := flags["apply"] == "true"
	if flags["archive"] == "true" {
		return archiveStrays(w, s, triaged, apply)
	}
	return reclaimStrays(w, s, triaged, apply)
}

// reclaimStrays is the sweep that touches nothing but the ones already free.
func reclaimStrays(w io.Writer, s *session, triaged []pool.Stray, apply bool) int {
	var d toon.Doc
	strayTable(&d, triaged)

	ready := reclaimableNow(triaged)
	archivable := 0
	for _, st := range triaged {
		if st.Archivable() {
			archivable++
		}
	}

	if !apply {
		d.Field("reclaimable", len(ready))
		help := []string{"Run `wt strays --apply` to remove the reclaimable ones"}
		if archivable > 0 {
			// The count is the point: an agent reading "10 reclaimable" against
			// forty-eight strays needs to know the other route exists, or it
			// concludes the rest are simply stuck.
			d.Field("archivable", fmt.Sprintf("%d more hold unpushed commits and nothing else", archivable))
			help = append(help,
				"Run `wt strays --archive` to see what preserving that work would reclaim")
		}
		d.Help(append(help, "Nothing has been removed")...)
		_, _ = d.WriteTo(w)
		return OK
	}

	removed, failures := removeAll(s, ready)
	d.Field("removed", removed)
	if len(failures) > 0 {
		failureTable(&d, failures)
	}
	d.Field("kept", len(triaged)-removed)
	_, _ = d.WriteTo(w)
	return OK
}

// archiveStrays preserves the work that is holding worktrees hostage, then
// reclaims them.
//
// Thirty-two of the forty-eight strays on this machine were protected for one
// reason: commits that exist on no other ref. The work was not worth a worktree
// and was not worth destroying either, and those are not the only two options.
// Pushing it somewhere durable answers the blocker rather than overriding it,
// which is why this mode never needs a --force anywhere.
//
// The other two blockers are untouched. A live process and uncommitted changes
// still win, whatever flags are passed.
func archiveStrays(w io.Writer, s *session, triaged []pool.Stray, apply bool) int {
	if !s.git.HasRemote() {
		return errorf(w, Fail,
			"no origin remote, so there is nowhere to archive to",
			"add one with `git remote add origin <url>`",
			"nothing was pushed or removed")
	}

	sw := planSweep(s, triaged)

	var d toon.Doc
	strayTable(&d, triaged)
	sw.planTable(&d)

	if !apply {
		return sw.dryRun(w, &d)
	}
	return sw.apply(w, s, &d)
}

// sweep is one archive pass: what it found, what it intends, and what it could
// not do. Both halves read from one value, so the dry run and the --apply
// cannot end up describing different plans.
type sweep struct {
	triaged  []pool.Stray
	plan     []gitwt.ArchivePlan
	ready    []string
	failures []failure
}

// planSweep decides what would happen, without pushing or removing anything.
//
// A worktree whose history cannot even be read is recorded as a failure rather
// than dropped: it is still on disk either way, and the sweep that does not
// mention it is the one that loses it.
func planSweep(s *session, triaged []pool.Stray) *sweep {
	sw := &sweep{triaged: triaged, ready: reclaimableNow(triaged)}
	for _, st := range triaged {
		if !st.Archivable() {
			continue
		}
		plan, err := s.git.PlanArchive(st.Tree.Path)
		if err != nil {
			sw.failures = append(sw.failures, failure{path: st.Tree.Path, reason: err.Error()})
			continue
		}
		sw.plan = append(sw.plan, plan)
	}
	return sw
}

// planTable is the full push list: every ref that would be written, and how
// much work is behind it. It prints in the dry run and in the --apply, because
// the second is the record of what the first promised.
func (sw *sweep) planTable(d *toon.Doc) {
	rows := make([][]any, 0, len(sw.plan))
	for _, p := range sw.plan {
		rows = append(rows, []any{collapseHome(p.Path), p.Branch, p.Commits, p.Ref})
	}
	d.Table("archive", []string{"path", "branch", "commits", "ref"}, rows)
}

func (sw *sweep) dryRun(w io.Writer, d *toon.Doc) int {
	if len(sw.failures) > 0 {
		failureTable(d, sw.failures)
	}
	d.Field("would_push", len(sw.plan))
	d.Field("would_reclaim", len(sw.ready)+len(sw.plan))
	d.Field("would_keep", len(sw.triaged)-len(sw.ready)-len(sw.plan))
	d.Help(
		"Run `wt strays --archive --apply` to push that list and reclaim what it frees",
		"Nothing has been pushed or removed",
		"Get one back later with `"+gitwt.ReclaimCommand(gitwt.ArchiveNamespace+"<branch>")+"`",
	)
	_, _ = d.WriteTo(w)
	return OK
}

// apply pushes, proves the push landed, and only then removes.
//
// A failed push leaves its worktree exactly where it was. That is the whole
// contract of this mode: the directory is the last copy until the remote has
// one, so nothing is torn down on the strength of a command that might not have
// worked. One failure does not stop the rest, either: a single wedged directory
// must not strand forty.
func (sw *sweep) apply(w io.Writer, s *session, d *toon.Doc) int {
	pushed, already := 0, 0

	// The ones that were already free go too: --archive is --apply plus the
	// work of making more worktrees free, not a separate sweep.
	doomed := make([]string, 0, len(sw.ready)+len(sw.plan))
	doomed = append(doomed, sw.ready...)

	for _, p := range sw.plan {
		existed, err := s.git.Archive(p)
		if err != nil {
			sw.failures = append(sw.failures, failure{path: p.Path, reason: err.Error()})
			continue
		}
		if existed {
			already++
		} else {
			pushed++
		}

		// Ask the original question again rather than assuming the archive
		// answered it. This is what proves the preserved commits are visible to
		// the same rule that protects every other worktree, and it is the only
		// thing standing between a push that quietly did nothing and a deletion.
		reason, err := pool.Unsafe(s.svc.Safety, p.Path)
		if err != nil {
			sw.failures = append(sw.failures, failure{path: p.Path, reason: err.Error()})
			continue
		}
		if reason != pool.Safe {
			sw.failures = append(sw.failures, failure{
				path:   p.Path,
				reason: fmt.Sprintf("archived to %s but still blocked: %s", p.Ref, reason),
			})
			continue
		}
		doomed = append(doomed, p.Path)
	}

	removed, removeFailures := removeAll(s, doomed)
	sw.failures = append(sw.failures, removeFailures...)

	d.Field("pushed", pushed)
	if already > 0 {
		d.Field("already_on_remote", already)
	}
	d.Field("removed", removed)
	if len(sw.failures) > 0 {
		failureTable(d, sw.failures)
	}
	d.Field("kept", len(sw.triaged)-removed)
	d.Help("Get one back with `" + gitwt.ReclaimCommand(gitwt.ArchiveNamespace+"<branch>") + "`")
	_, _ = d.WriteTo(w)
	return OK
}

// reclaimableNow is the worktrees nothing has to be done to first.
func reclaimableNow(triaged []pool.Stray) []string {
	ready := make([]string, 0, len(triaged))
	for _, st := range triaged {
		if st.Reclaimable() {
			ready = append(ready, st.Tree.Path)
		}
	}
	return ready
}

// strayTable is the same view in every mode, so a dry run and an --apply can be
// read against each other line by line.
func strayTable(d *toon.Doc, triaged []pool.Stray) {
	rows := make([][]any, 0, len(triaged))
	for _, st := range triaged {
		verdict := string(st.Reason)
		if st.Reclaimable() {
			verdict = "reclaimable"
		}
		rows = append(rows, []any{collapseHome(st.Tree.Path), st.Tree.Branch, verdict})
	}
	d.Table("strays", []string{"path", "branch", "verdict"}, rows)
}

// failure is one worktree that was left where it stands, and why.
type failure struct {
	path   string
	reason string
}

// failureTable names every worktree that survived the sweep by accident rather
// than by rule. A count alone would leave an agent to guess which.
func failureTable(d *toon.Doc, failures []failure) {
	rows := make([][]any, 0, len(failures))
	for _, f := range failures {
		rows = append(rows, []any{collapseHome(f.path), f.reason})
	}
	d.Table("failed", []string{"path", "error"}, rows)
}

// removeAll tears down what it can and reports what it could not.
func removeAll(s *session, paths []string) (int, []failure) {
	removed := 0
	var failures []failure
	for _, path := range paths {
		if err := s.git.Remove(path); err != nil {
			failures = append(failures, failure{path: path, reason: err.Error()})
			continue
		}
		removed++
	}
	return removed, failures
}
