// Package gitwt is the only place that runs git. Everything above it works in
// terms of slots and paths, which is what lets the pool's rules be tested
// without a repository on disk.
package gitwt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Git runs git commands against one repository.
//
// Binary is resolved once at construction rather than looked up per call: this
// tool installs a shim named "git" earlier on PATH, and a lookup here would
// find the shim and recurse.
type Git struct {
	Binary string
	Repo   string
}

// Open finds the real git and the main checkout containing dir.
func Open(dir string) (*Git, error) {
	binary, err := RealBinary()
	if err != nil {
		return nil, err
	}
	g := &Git{Binary: binary, Repo: dir}
	root, err := g.run(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("not inside a git repository: %s", dir)
	}
	// --git-common-dir points at the main checkout's .git even from inside a
	// worktree, which is what makes the pool the same pool wherever it is
	// invoked from.
	g.Repo = filepath.Dir(strings.TrimSpace(root))
	return g, nil
}

// RealBinary locates git, skipping any shim this tool installed.
//
// The shim marks itself with a sentinel line rather than being recognised by
// path, so moving it or renaming the directory cannot turn the guard off.
func RealBinary() (string, error) {
	paths := filepath.SplitList(os.Getenv("PATH"))
	for _, dir := range paths {
		candidate := filepath.Join(dir, "git")
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		if isShim(candidate) {
			continue
		}
		return candidate, nil
	}
	return "", fmt.Errorf("no git found on PATH that is not the wt shim")
}

// ShimSentinel appears in the shim's source so it can be recognised.
const ShimSentinel = "# wt-worktree-shim v1"

func isShim(path string) bool {
	// A shim is a short script; a real git is a large binary. Reading the head
	// is enough and costs nothing.
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	return strings.Contains(string(head[:n]), ShimSentinel)
}

func (g *Git) run(dir string, args ...string) (string, error) {
	cmd := exec.Command(g.Binary, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// Dirty reports whether a worktree has uncommitted or untracked changes.
func (g *Git) Dirty(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	}
	out, err := g.run(path, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// Unpushed reports whether a worktree holds commits that exist nowhere else in
// the repository.
//
// "Nowhere else" means no other ref contains them: not a remote, and not
// another local branch. Comparing against remotes alone looks right and is not.
// A repository with no remote configured has every commit unreachable from a
// remote, so every worktree reads as unpushed, nothing is ever recyclable, and
// the pool wedges at the cap for good. That is not hypothetical; it is what the
// first end-to-end run did.
//
// Excluding this worktree's own branch is what makes the question meaningful:
// a branch cut from main with nothing new on it is fully contained in main, and
// losing it costs nothing.
//
// Commits that exist nowhere else by hash can still exist upstream by content,
// and those are forgiven: see landedUpstream.
func (g *Git) Unpushed(path string) (bool, error) {
	count, err := g.UniqueCommits(path)
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	return !g.landedUpstream(path), nil
}

// landedUpstream reports whether every commit HEAD holds that the remote's
// default branch lacks is already there by content.
//
// Reachability cannot see a cherry-pick, or a squash that landed without a pull
// request for `Merged` to find: both put the same change on main under a new
// hash, so the worktree's own commits exist on no other ref while the work
// itself is upstream. One slot was held for exactly this with four commits,
// every one of which `git cherry` marked as already on origin/main.
//
// `git cherry` prints one line per commit HEAD has and upstream lacks, "-" for
// those whose patch is already upstream and "+" for the rest. The worktree is
// cleared only when there is at least one line and every line is "-". No lines
// is not proof: a worktree standing on the very branch it is compared with, as
// one on main is when there is no remote, lists nothing at all.
//
// `git cherry` skips merge commits without saying so, which would clear a merge
// carrying a conflict resolution or an edit of its own because its parents
// landed. So a worktree holding any unique merge is never cleared by content.
//
// Every failure answers false, which leaves the reachability verdict standing.
// Not being able to find out is not the same as finding out it is safe.
func (g *Git) landedUpstream(path string) bool {
	upstream := g.upstream(path)
	if upstream == "" {
		return false
	}

	revs, err := g.uniqueRevs(path)
	if err != nil {
		return false
	}
	merges, err := g.runStdin(path, revs, "rev-list", "--count", "--merges", "--stdin")
	if err != nil || strings.TrimSpace(merges) != "0" {
		return false
	}

	out, err := g.run(path, "cherry", upstream, "HEAD")
	if err != nil {
		return false
	}
	lines := 0
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			return false
		}
		lines++
	}
	return lines > 0
}

// upstream names the branch work is meant to land on, or "" when there is
// none to be found.
//
// The remote's own record of its default branch comes first, because a guess at
// the name is wrong for every repository whose trunk is not called main. It is
// only there when something set it (a clone does, `git remote set-head` does),
// so the conventional names follow, local last: in a repository with no remote,
// the local main is the only upstream there is.
func (g *Git) upstream(path string) string {
	if out, err := g.run(path, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		if ref := strings.TrimSpace(out); ref != "" && g.resolves(path, ref) {
			return ref
		}
	}
	for _, ref := range []string{"refs/remotes/origin/main", "refs/heads/main"} {
		if g.resolves(path, ref) {
			return ref
		}
	}
	return ""
}

func (g *Git) resolves(path, ref string) bool {
	_, err := g.run(path, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// UniqueCommits counts the commits a worktree holds that exist on no other ref.
//
// Unpushed is this same question asked as a yes or no. The count itself is what
// a person triaging abandoned work reads: one commit is a checkpoint nobody
// will miss, forty is a feature somebody should look at before it is forgotten.
func (g *Git) UniqueCommits(path string) (int, error) {
	if path == "" {
		return 0, nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return 0, nil
	}
	revs, err := g.uniqueRevs(path)
	if err != nil {
		return 0, err
	}
	out, err := g.runStdin(path, revs, "rev-list", "--count", "--stdin")
	if err != nil {
		return 0, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("counting unique commits in %s: %w", path, err)
	}
	return count, nil
}

// uniqueRevs is the `rev-list --stdin` input that selects the commits HEAD
// holds and no other ref does. Counting them and asking which are merges are
// the same selection, so it is built in one place.
func (g *Git) uniqueRevs(path string) (string, error) {
	branch, err := g.run(path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	mine := "refs/heads/" + strings.TrimSpace(branch)

	refs, err := g.run(path, "for-each-ref", "--format=%(refname)")
	if err != nil {
		return "", err
	}

	// `--exclude=<pattern> --all` looks like the way to say this and is not:
	// `--all` also contributes HEAD, which no refs/heads pattern excludes, so
	// the branch ends up excluded from itself and every worktree reads as
	// safe. Naming the other refs explicitly has no such trapdoor.
	//
	// They go in on stdin because a repository with thousands of refs would
	// otherwise build a command line long enough to be refused.
	var revs strings.Builder
	revs.WriteString("HEAD\n")
	for _, ref := range strings.Split(refs, "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" || ref == mine {
			continue
		}
		revs.WriteString("^" + ref + "\n")
	}
	return revs.String(), nil
}

// runStdin is run with input, for the commands that take a ref list.
func (g *Git) runStdin(dir, input string, args ...string) (string, error) {
	cmd := exec.Command(g.Binary, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// Worktree is one entry from `git worktree list`.
type Worktree struct {
	Path   string
	Branch string
	Bare   bool
	Main   bool
}

// List returns every worktree git knows about, main checkout first.
func (g *Git) List() ([]Worktree, error) {
	out, err := g.run(g.Repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var trees []Worktree
	var current *Worktree
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			if current != nil {
				trees = append(trees, *current)
			}
			current = &Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		case current == nil:
			continue
		case strings.HasPrefix(line, "branch "):
			current.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case line == "bare":
			current.Bare = true
		}
	}
	if current != nil {
		trees = append(trees, *current)
	}
	if len(trees) > 0 {
		trees[0].Main = true
	}
	return trees, nil
}

// Add creates a worktree at path on branch, creating the branch from base when
// it does not exist yet.
func (g *Git) Add(path, branch, base string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if g.hasBranch(branch) {
		_, err := g.run(g.Repo, "worktree", "add", path, branch)
		return err
	}
	_, err := g.run(g.Repo, "worktree", "add", "-b", branch, path, base)
	return err
}

func (g *Git) hasBranch(branch string) bool {
	_, err := g.run(g.Repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// Remove tears down a worktree and prunes the administrative entry.
//
// It never passes --force: every reason git would need forcing is a reason the
// pool should have refused the removal higher up, and a --force here would
// silently undo those checks.
func (g *Git) Remove(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		_, _ = g.run(g.Repo, "worktree", "prune")
		return nil
	}
	if _, err := g.run(g.Repo, "worktree", "remove", path); err != nil {
		return err
	}
	_, _ = g.run(g.Repo, "worktree", "prune")
	return nil
}

// DefaultBase is the ref new branches are cut from.
func (g *Git) DefaultBase() string {
	for _, ref := range []string{"origin/main", "origin/master", "main", "master"} {
		if _, err := g.run(g.Repo, "rev-parse", "--verify", "--quiet", ref); err == nil {
			return ref
		}
	}
	return "HEAD"
}

// Fetch updates the remote-tracking refs so a new worktree is cut from current
// state rather than from whatever was last pulled.
func (g *Git) Fetch() error {
	_, err := g.run(g.Repo, "fetch", "--quiet", "origin")
	return err
}
