# wt

[![ci](https://github.com/flazouh/wt/actions/workflows/ci.yml/badge.svg)](https://github.com/flazouh/wt/actions/workflows/ci.yml)

A capped pool of git worktrees, shared by every agent on this machine.

Five worktrees per repository. When a sixth is asked for, the least recently
used one is recycled rather than a sixth being created. Nothing is ever
recycled while something is working inside it.

## Install

```sh
go install github.com/flazouh/wt/cmd/wt@latest
```

That puts `wt` in `$(go env GOPATH)/bin`, which needs to be on your PATH. Go
1.26 or later; `git` is required, and `lsof` is required for the liveness check
that gates every deletion.

## Why

Forty-eight worktrees accumulated in one repository, 41 GB between them. That
is not a tidiness problem: the TestFlight preflight needs 12 GB free, the
machine sat at 11, and three releases in a row stopped on it.

Every agent that reached for `git worktree add` was individually reasonable.
Nothing was counting.

## Use

```sh
wt                        # the pool for the repository you are standing in
wt take feature/login     # lease a worktree, creating or recycling one
wt done                   # hand it back; it stays on disk for the next take
wt drop 3                 # remove one for good
wt pin 2                  # never recycle this one
wt strays                 # worktrees created outside the pool
wt strays --apply         # remove the ones that are safe to remove
wt strays --archive       # what preserving their work would free
wt strays --archive --apply
wt enforce --install      # make the cap binding
```

Output is [TOON](https://toonformat.dev/) on stdout, errors included, so an
agent reads success and failure through the same channel. Exit codes are 0 for
success including no-ops, 1 for failure, 2 for usage.

## What it will not do

Three questions are asked before anything is deleted:

- is a live process working in it
- does it have uncommitted changes
- does it hold commits that exist on no other ref

Any yes, and the worktree is left alone. So is *any error* asking: a machine
that cannot answer "is something running in here?" gets nothing deleted at all.
The shell script this replaces learned that the hard way and aborts when `lsof`
comes back empty; the same guard is here as a typed error.

The third question is subtler than it looks. Comparing against remotes alone
means a repository with no remote configured has every commit unreachable from
one, so every worktree reads as protected and the pool wedges at five for good.
It compares against every other ref instead.

## Archiving

Of the forty-seven strays in the repository this was built for, thirty-two were
held by one question and one only: commits that exist on no other ref. Mostly
abandoned Codex runs, sixteen hours to five weeks old. Not worth a worktree
each, not worth destroying either — and those are not the only two answers.

`wt strays --archive` pushes each of those branches to `refs/archive/<branch>`
on origin, checks the remote really holds it, and only then reclaims the
directory. The blocker is answered rather than overridden, so nothing in this
mode passes a `--force` to anything.

`refs/archive/` rather than `archive/`, which would be `refs/heads/archive/` —
a real branch, in `git branch -r`, in the GitHub branch list, in every
base-branch picker, and fetched into every clone by the default refspec. Thirty
two of them would be thirty-two entries in a list people read to find something
else. Outside `refs/heads` it is none of those things; the price is that getting
one back is an explicit fetch, which the command prints:

```sh
git fetch origin refs/archive/<branch>:refs/archive/<branch>
```

The push also writes the ref locally. That is not bookkeeping: the unpushed
check asks whether any ref *in this repository* holds the commits, so a ref that
exists only on the remote would leave the worktree protected — archived, and
then refused. The local write happens last, so a failure anywhere earlier leaves
the worktree exactly where it was.

The other two questions are untouched. A live process or uncommitted changes
still refuse, whatever flags are passed, and a push that fails leaves its own
worktree alone without stopping the rest of the sweep.

## Enforcement

`wt` can only police worktrees it was asked to create. `git worktree add` is
still there, and an agent that reaches for it walks straight past the pool.

`wt enforce --install` writes a small `git` shim into `~/.local/bin`, earlier on
PATH than the real one. It refuses exactly one subcommand and `exec`s the real
git for everything else, so signals, exit codes and the process tree are
unchanged. `WT_POOL=1` bypasses it, which is how `wt` itself gets through.

It refuses to overwrite anything that is not already the shim, and
`wt enforce` on its own reports whether the shim is installed *and* whether it
is actually the git that PATH finds first. Those are different questions.

## Layout

```
internal/pool       the rules: slots, acquire, recycle. No git, no disk.
internal/gitwt      the only package that runs git
internal/liveness   is anything alive in this directory
internal/registry   state on disk, file-locked, written through a rename
internal/shim       the git wrapper
internal/toon       the output format
internal/cli        the surface agents touch
```

The rules do not know what a repository is, which is why they are tested
without one. The parts that do talk to git are tested against a real one,
because the two bugs found during the first end-to-end run were both invisible
to a fake.

## State

`~/.local/state/wt/registry.json`, or `$WT_STATE_DIR`. Every read-modify-write
runs under an exclusive `flock` and lands through a temporary file and a
rename, so two agents racing cannot lose each other's slots and a crash
mid-write leaves the previous registry intact. Twenty concurrent writers is a
test, not a hope.

## Known limits

- A worktree is created on disk before the registry write commits. If the write
  fails in between, the directory exists and the registry does not know it. It
  shows up under `wt strays` and the next `take` at that index clears it, but
  the window is real.
- The cap is per repository. Five repositories with full pools is twenty-five
  worktrees, which is a lot less than forty-eight but is not five.
- `wt strays` reports across the current repository only.
- macOS and Linux. The liveness probe shells out to `lsof`, and the shim is a
  POSIX script, so Windows is unsupported rather than untested.

## Contributing

`go test -race ./...`, `gofmt -l .` empty, `go vet ./...` clean. CI runs all
three on Linux and macOS.

The layout above is the review standard: `internal/pool` must stay free of git,
and anything that shells out belongs in `internal/gitwt`. Changes to the three
safety questions want a test against a real repository, not a fake — every bug
found in this tool so far was invisible to a fake git.

## License

MIT. See [LICENSE](LICENSE).
