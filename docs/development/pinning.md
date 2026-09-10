# Pinning forge: commits now, tags at launch

**If you found a version like `v0.1.15-0.20260910160302-c195f15386be` in
`go.mod` and it looked wrong: it is deliberate.** That is a Go pseudo-version,
this repo is in commit-pinning mode, and the reasoning is below. Do not "fix"
it back to a tag without reading the switch section — you would be reverting a
decision, not correcting a mistake.

The authoritative version of this document, covering all three repos, is
**`control-plane/docs/pinning.md`**. This file is the reliant-side summary.

## The commands

```sh
make pin-forge                 # pin forge AND forge/pkg to forge@origin/main
make pin-forge REF=v0.1.14     # pin to a specific tag, branch or sha
make pin-drift                 # how far behind the pin is (warn-only)
```

`make pin-forge` reads forge's `origin/main` from the **remote** rather than
trusting the local `../forge` checkout, which on a dev box is routinely on
another agent's branch or days stale. It pins **both** forge modules to one
commit — they are tagged from a single commit, so a split pin compiles today
and skews later — then proves the result with `GOWORK=off go build ./...`.

`GOWORK=off` matters here specifically: this repo has a `go.work` that resolves
forge from `../forge` on disk, so a plain `go build` can pass while CI fails on
the version `go.mod` actually pins.

It edits `go.mod` and `go.sum` and **stops**. It does not commit — this
checkout is shared with other agents, and staging paths in a tree holding
someone else's in-flight work is how that work gets lost.

## Why commits, pre-launch

Prod is the only real environment, and every bug we chase has only ever
appeared there. Shipping one forge fix the tagged way costs three tags, three
CI cycles and three merges. Measured: a commit pin resolves in **2.065s**; a
freshly pushed tag took **~20 minutes** to become usable because
`proxy.golang.org` had not ingested it. `GOPRIVATE=github.com/reliant-labs/*`
(already in `pr-ci.yml`) is what makes the fast number real.

A pseudo-version is a real, resolvable, checksummed module version. It is **not**
a `replace` — a `replace` applies in every build mode, `GOWORK=off` does not
disable it, and it breaks the container build. That remains forbidden and
`internal/buildmode/forge_replace_guard_test.go` is untouched by this mode; it
passes on a pseudo-version as-is.

## Drift

A commit pin has no version number to notice is stale — a pseudo-version looks
equally current whether it is one commit old or ninety. `make pin-drift`
reports how many commits behind `forge@main` each pin is. It **warns and never
fails**: a deliberately older pin is legitimate, and a check that failed CI for
it would be one whose only fix is to bypass it.

## Switching back to tags at launch

```sh
make pin-forge REF=vX.Y.Z
grep 'reliant-labs/forge' go.mod    # both lines must read vX.Y.Z
GOWORK=off go build ./...
go test -count=1 ./internal/buildmode/
```

Then confirm nothing pseudo-versioned remains:

```sh
grep -n 'reliant-labs/forge' go.mod | grep -- '-0\.[0-9]\{14\}-'   # expect no output
```

Nothing needs to be un-built to switch: `pin-forge` takes a `REF` and the drift
check already understands both shapes. The full release procedure is in the
`release` skill; this file only covers which SHAPE the pins take.
