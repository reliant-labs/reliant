# One Go build cache per machine: `forge cache`

Every build on a machine (agents, worktrees, GitHub self-hosted runners, the
merge-queue gate) can read one shared Go build cache, without letting an
untrusted build poison what main's builds read. Nothing is shared between
machines.

## The tiers

| tier    | path                                                                  | written by                                                                                                                       | read by                    |
| ------- | --------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- | -------------------------- |
| trusted | `/var/cache/workspace/go-build-trusted`, or `$FORGE_GO_TRUSTED_CACHE` | trusted builds only: `forge cache seed` on each merge to main, and the gate on main, running plain `go` with `GOCACHE` set to it | every build on the machine |
| overlay | the context's own `GOCACHE`                                           | that context, through `forge cache prog`                                                                                         | that context               |

An untrusted build runs `go` with `GOCACHEPROG="forge cache prog"`. For each
lookup the helper reads the trusted tier first, then the overlay. A trusted hit
is returned in place: nothing is copied and nothing is written to the trusted
tier. Every write goes to the overlay; when the trusted tier already holds the
same output bytes, only the overlay's index entry is written.

Why: a cache entry is keyed by the hash of a build action's inputs, and `go`
trusts whatever bytes are stored under that key. Anyone who can write a shared
cache can plant an object under a key main's build will look up. So pull
requests and agents only read the trusted tier.

A trusted tier that is missing or unreadable is not an error: the build runs on
its overlay alone. A damaged entry is a miss, never a failure. An overlay that
is the trusted tier itself is refused.

Both tiers are plain cmd/go cache directories, so `forge storage` trims them
like any Go cache. A hit restamps the entry's mtime when it is an hour stale,
like `go` itself. Trusted hits are restamped when the reader may (same uid; it
changes no bytes). For other readers (a runner's uid, a read-only mount in the
gate container) the trim's 2h floor and its live-build lease protect a trusted
entry in use, and seeding restamps everything main's tree uses on every merge.

A few lookups always miss the trusted tier: cmd/go's module index entries for
the main module's own directories, which are keyed by the checkout's absolute
path. They are tiny, and they land in the overlay.

## Using it

`forge cache env` prints the settings an untrusted context needs:

```sh
$ forge cache env
# GOCACHE stays this context's own cache: it is the overlay, the only tier this build writes.
GOCACHEPROG=/usr/local/bin/forge cache prog
FORGE_GO_TRUSTED_CACHE=/var/cache/workspace/go-build-trusted
# -trimpath keys every checkout's packages alike, so one tier serves them all.
GOFLAGS=-trimpath
# Or once per user, in go's env file (an exported variable still overrides it):
#   go env -w GOCACHEPROG="/usr/local/bin/forge cache prog --trusted /var/cache/workspace/go-build-trusted" GOFLAGS=-trimpath
```

`-trimpath` matters: without it every package of the main module is keyed by
its absolute directory, so no two checkouts share an entry. forge's Taskfile and
CI workflow set it.

`forge cache prog` runs as this forge in every project, whatever forge the
project pins: it starts once per `go` command and must not be handed to a
pinned forge that does not have it.

## Seeding

```sh
forge cache seed --repo ~/src/forge                # origin/main, as the trusted dir's owner
forge cache seed --repo ~/src/forge --vet --race   # also vet facts and -race compiles
```

`seed` takes `<trusted>/.forge-seed.lock` (queued seeds wait up to 30 minutes,
then skip), resolves `--rev` (default `origin/main`; `--fetch` runs
`git fetch origin main` first), and stops at once with `already seeded <sha>`
when `<trusted>/.forge-seed/<repo>.json` records that commit seeded with the
same flags (`--force` rebuilds). Otherwise it exports exactly that commit with
`git archive` (never the working tree: an untracked file would land in the
trusted tier) and, in each Go module of the export, runs
`go list -e -export -deps -test ./...`, which compiles every package and test
variant without linking or running anything.

The go commands run with `GOCACHE=<trusted>`, `GOFLAGS=-trimpath`, `GOENV=off`
(a `GOCACHEPROG` in go's env file must not route the seed through the helper)
and no `GOCACHEPROG`; module settings such as `GOMODCACHE` and `GOPROXY` are
kept, from the environment or from go's env file.

The trusted directory is created `0755` when missing, and refused when it is
group- or world-writable or owned by another user.

## Measuring

Set `FORGE_CACHE_PROG_STATS=<file>` and each `go` command's helper appends one
JSON line when it exits:

```json
{
  "pid": 4242,
  "dir": "/home/u/src/forge",
  "trusted": "/var/cache/workspace/go-build-trusted",
  "overlay": "/var/cache/workspace/go-build",
  "gets": 3214,
  "trusted_hits": 3190,
  "overlay_hits": 20,
  "misses": 4,
  "puts": 3,
  "put_bytes": 1840,
  "deduped_puts": 0,
  "seconds": 7.4
}
```

`trusted_note` appears when the trusted tier was not used, with the reason.
