package gitwt

import (
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
)

// Merged reports whether a branch's pull request was merged.
//
// This exists because commit reachability cannot see a squash merge. A squashed
// pull request lands as one new commit with a new hash, so the branch's own
// commits exist on no other ref and `Unpushed` correctly says so — while the
// work itself is sitting in main. Without this, every squash-merged worktree is
// protected for ever, which on this machine was most of them.
//
// It asks the GitHub CLI, because the PR is the only place that fact is
// recorded. When gh is missing, not authenticated, or the remote is not GitHub,
// it returns false: unknown must read as "not known to be merged", which leaves
// the worktree protected. Erring the other way deletes work.
type Merged struct {
	once     sync.Once
	branches map[string]bool
	usable   bool
}

// NewMerged returns a lookup that has not yet asked anything.
func NewMerged() *Merged { return &Merged{} }

// Is reports whether this branch has a merged pull request.
func (m *Merged) Is(branch string) bool {
	m.load()
	if !m.usable || branch == "" {
		return false
	}
	return m.branches[branch]
}

// Available reports whether the lookup could reach GitHub at all, so a caller
// can say "this was not checked" rather than implying a negative answer.
func (m *Merged) Available() bool {
	m.load()
	return m.usable
}

func (m *Merged) load() {
	m.once.Do(func() {
		m.branches = map[string]bool{}

		if _, err := exec.LookPath("gh"); err != nil {
			return
		}
		// One query for every merged branch, rather than one per worktree:
		// forty-seven round trips to the API is a minute of waiting.
		out, err := exec.Command("gh", "pr", "list",
			"--state", "merged", "--limit", "500",
			"--json", "headRefName").Output()
		if err != nil {
			return
		}
		var prs []struct {
			HeadRefName string `json:"headRefName"`
		}
		if err := json.Unmarshal(out, &prs); err != nil {
			return
		}
		for _, pr := range prs {
			if name := strings.TrimSpace(pr.HeadRefName); name != "" {
				m.branches[name] = true
			}
		}
		m.usable = true
	})
}

// Branch returns the branch a worktree is on, or empty when it is detached.
func (g *Git) Branch(path string) string {
	out, err := g.run(path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(out)
	if name == "HEAD" {
		return ""
	}
	return name
}
