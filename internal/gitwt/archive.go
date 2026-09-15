package gitwt

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ArchiveNamespace is where preserved branches land on the remote.
//
// `refs/archive/<branch>` rather than `archive/<branch>`, because the second
// spelling is `refs/heads/archive/<branch>`: a real branch. It would appear in
// `git branch -r`, in the branch list on GitHub, in every base-branch picker
// and every completion, and it would be fetched into every clone by the default
// refspec. Thirty-two abandoned Codex runs would be thirty-two entries in a
// list people read to find the branch they want.
//
// Outside refs/heads it is none of those things. The price is that the default
// refspec does not fetch it either, so reclaiming one is an explicit fetch —
// which ReclaimCommand spells out, and which is the right shape for work that
// was abandoned rather than paused.
const ArchiveNamespace = "refs/archive/"

// ArchivePlan is what archiving one worktree would do. Building it reads the
// repository and changes nothing.
//
// The plan is a value rather than a set of arguments so the dry run and the
// push are the same decision rather than two derivations of it. A dry run that
// names a ref the push then computes differently is a bug waiting for the day
// the two pieces of code disagree, and it would be invisible until it fired.
type ArchivePlan struct {
	Path    string
	Branch  string
	Ref     string
	Commit  string
	Commits int
}

// PlanArchive works out where a worktree's commits would go, and how many there
// are. It touches nothing.
func (g *Git) PlanArchive(path string) (ArchivePlan, error) {
	p := ArchivePlan{Path: path, Branch: g.Branch(path)}

	head, err := g.run(path, "rev-parse", "HEAD")
	if err != nil {
		return p, fmt.Errorf("reading HEAD of %s: %w", path, err)
	}
	p.Commit = strings.TrimSpace(head)
	p.Ref = ArchiveRef(p.Branch, path, p.Commit)

	p.Commits, err = g.UniqueCommits(path)
	if err != nil {
		return p, err
	}
	return p, nil
}

// Archive carries out a plan and proves the commits arrived. It reports whether
// the remote already held them.
//
// Three things have to be true before the caller may remove the directory, and
// each is checked rather than assumed:
//
//   - the push reported success,
//   - the remote actually holds that commit at that ref, read back with
//     ls-remote rather than inferred from an exit code,
//   - a local ref now points at the commit too.
//
// The last one is not bookkeeping. `Unpushed` asks whether any ref in this
// repository contains the commits; a ref that exists only on the remote is not
// one of them, so without the local ref the worktree would stay protected and
// the sweep would archive it and then refuse to reclaim it. Writing the local
// ref last means a failure anywhere earlier leaves the worktree protected,
// which is the direction a failure should fall.
func (g *Git) Archive(p ArchivePlan) (existed bool, err error) {
	remote, err := g.remoteRef(p.Ref)
	if err != nil {
		return false, err
	}

	switch {
	case remote == p.Commit:
		// Already preserved, by an earlier run or an earlier worktree on the
		// same branch. Pushing again would be a no-op; say so instead.
		existed = true
	case remote != "":
		return false, fmt.Errorf("%s already holds %s on the remote, not %s; archive it by hand or under another name",
			p.Ref, short(remote), short(p.Commit))
	default:
		// --no-verify skips the repository's own pre-push hook, and nothing
		// else.
		//
		// A pre-push hook is a gate on contributions: it runs the test suite
		// against work that is about to become a branch someone builds on. An
		// archive is not that. It lands outside refs/heads, no CI watches it,
		// nothing builds from it, and by definition it is work that was
		// abandoned unfinished — so the gate would fail on most of it, and each
		// failure would keep the worktree it was meant to free. The repository
		// this was built for runs its whole affected test suite on pre-push;
		// twenty-one archives would have been twenty-one test runs and
		// twenty-one worktrees kept.
		//
		// None of the three questions that protect a worktree are skipped, and
		// the remote is still read back below either way. The one thing being
		// bypassed is a check about code quality, on a ref that no code is ever
		// built from.
		if _, err := g.run(p.Path, "push", "--no-verify", "origin", p.Commit+":"+p.Ref); err != nil {
			return false, fmt.Errorf("pushing %s to %s: %w", short(p.Commit), p.Ref, err)
		}
		landed, err := g.remoteRef(p.Ref)
		if err != nil {
			return false, err
		}
		if landed != p.Commit {
			return false, fmt.Errorf("%s reads back as %q on the remote, want %s", p.Ref, landed, short(p.Commit))
		}
	}

	if _, err := g.run(g.Repo, "update-ref", p.Ref, p.Commit); err != nil {
		return existed, fmt.Errorf("recording %s locally: %w", p.Ref, err)
	}
	return existed, nil
}

// remoteRef returns the commit the remote holds at a ref, or empty if it holds
// none. An unreachable remote is an error, not an absent ref: the two lead to
// opposite decisions about whether it is safe to delete a directory.
func (g *Git) remoteRef(ref string) (string, error) {
	out, err := g.run(g.Repo, "ls-remote", "origin", ref)
	if err != nil {
		return "", fmt.Errorf("asking the remote about %s: %w", ref, err)
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return "", nil
	}
	return strings.Fields(line)[0], nil
}

// ArchiveRef is where a worktree's work is preserved.
//
// A detached worktree has no branch name to borrow, so it is named after the
// directory and the commit. Six of the forty-eight strays on this machine were
// detached, which is more than enough to need a name rather than a skip.
func ArchiveRef(branch, path, commit string) string {
	if branch != "" {
		return ArchiveNamespace + branch
	}
	return fmt.Sprintf("%sdetached/%s-%s", ArchiveNamespace, filepath.Base(path), short(commit))
}

// ReclaimCommand is how a person gets an archived branch back, printed beside
// the archive so the answer travels with the thing it is about.
func ReclaimCommand(ref string) string {
	return fmt.Sprintf("git fetch origin %s:%s", ref, ref)
}

// HasRemote reports whether origin exists, so an archive sweep can refuse up
// front rather than one failed push at a time.
func (g *Git) HasRemote() bool {
	out, err := g.run(g.Repo, "remote")
	if err != nil {
		return false
	}
	for _, name := range strings.Fields(out) {
		if name == "origin" {
			return true
		}
	}
	return false
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
