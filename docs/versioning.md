# forge versioning: what a project pins, and what a dev build does instead

forge is **one Go module**, `github.com/reliant-labs/forge`. Generated projects
import `github.com/reliant-labs/forge/pkg/*` (serverkit, appkit, orm, observe,
testkit, ...) — those are packages inside that module, not a separate one.

This document is the canonical description of how that dependency is versioned.
The implementation lives in:

- `internal/buildinfo/buildinfo.go` — `InstallableVersion()`, the single
  predicate for "can a module proxy serve this build?"
- `internal/generator/project_pkgdep.go` — the scaffold-time decision.
- `internal/templates/project/go.mod.tmpl`, `gen-go.mod.tmpl` — what is emitted.
- `internal/cli/generate_pkg_compat.go` — the pre-codegen compatibility gate.
- `internal/cli/new.go` (`writeDevForgeGoWork`) — the dev-build source bridge.

## One version, by construction

There used to be two modules: the CLI, and a `github.com/reliant-labs/forge/pkg`
submodule holding the runtime libraries. They were released in lockstep, at the
same commit, with the same version — and keeping that true took three
hand-maintained syncs, two of which failed in production:

- the root module's `require forge/pkg` went stale, so `go install
.../cmd/forge@main` fetched an older pkg than main's code needed and forge's
  main branch was uninstallable;
- a hand-listed registry of "forge/pkg symbols the generator emits" went stale
  when `testkit.StubNotConfigured` was added, so `forge generate` rewrote a
  project tree and then failed its own validate.

Merging the modules makes both unrepresentable. The generator and the runtime it
generates calls into are now the same artifact at the same version, so there is
nothing left to keep in sync. Import paths did not change.

## What a scaffold pins

Whatever the running binary can honestly name as itself, and only when a module
proxy can serve it. That is exactly `buildinfo.InstallableVersion()`:

| How forge was built                            | What the project requires                                                                                            |
| ---------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `go install .../cmd/forge@v0.1.16` (a release) | `require github.com/reliant-labs/forge v0.1.16`                                                                      |
| `go install .../cmd/forge@main` or `@<sha>`    | the pseudo-version, verbatim — it is proxy-resolvable, and this is what a consumer in commit-pinning mode depends on |
| `go build ./cmd/forge` (local tree)            | **nothing**                                                                                                          |
| a dirty working tree                           | **nothing**                                                                                                          |

The last two rows are the important ones. A local or dirty build exists nowhere
a consumer could fetch it, so there is no version that describes it.

An EMBEDDED forge (`reliant forge …`) is judged by forge's own dependency entry,
never by the host's build stamps: a host built from a checkout that got forge
from the proxy (a go.sum hash, no `replace`) pins that forge version. Reading the
host's `vcs.*` stamps instead made every released reliant pin nothing.

A binary that can neither pin nor bridge (below) is refused by
`forge project new` before anything is written. It used to scaffold anyway, and
`go mod tidy` — with nothing requiring forge — resolved
`github.com/reliant-labs/forge/pkg/*` to the retired `forge/pkg` module, so the
project failed its first `forge generate`. `task build` and `task install`
stamp the source root for exactly this reason. forge used
to fall back to a `defaultPublishedForgePkgVersion` constant naming the last
release — which told projects to require a version that could not satisfy the
code being generated. That constant is gone, and an empty version now means
"this build needs a source bridge", never "use a default".

## The dev build's bridge

A binary installed with `task install:dev` carries `buildinfo.DevForgeRoot`: the
absolute path of the checkout it was built from, injected by ldflags from the
builder's own tree (never committed). An embedder that does not pass that ldflag
— reliant runs forge in-process — is covered by
`DiscoverDevForgeRootFromSource()`, which recovers the path the compiler baked
into forge's own source files.

With that path, a dev forge:

- **scaffolding a new project with `--link-forge`** writes a gitignored
  `go.work` into it that `use`s the forge checkout, so the project compiles
  against your source. Without the flag nothing is written — and a service
  scaffold from an unreleased build is refused, naming the flag;
- **generating into an existing project** that resolves forge from a proxy
  refuses _before_ codegen touches the tree, and prints the `go work use`
  command to run.

The bridge is **opt-in** because it used to be implicit, and that went wrong
quietly: a host binary embedding forge through a workspace is a dev build too,
so every project it scaffolded was bridged to whatever checkout that host
compiled from — on a shared machine, the main checkout everyone pulls into.
The library under those projects moved with each merge; the binary generating
their code did not. The `go.work` line is the record of the decision, and the
npm twin (`.forge-link/` for `@reliantlabs/forge-web-runtime`) follows it
rather than deciding again.

## Skew between the binary and a bridged checkout

A bridged project compiles generated code against the checkout, so the binary
must have been built from that same source. `forge generate` and `forge lint`
print one line, and `forge doctor` reports a `Forge Bridge` warning, whenever
it cannot show that:

- a binary that records its forge commit (`vcs.revision`, or a version naming
  one) is compared with the checkout's `HEAD` exactly; a dirty checkout
  matches only a dirty binary built from that tree after its newest edit;
- a binary that records none — forge embedded in a host through a workspace,
  where build info says `(devel)` and `vcs.*` describes the host — matches
  only if it was compiled from that checkout and neither `HEAD` nor any file
  has changed since the executable was written.

```
⚠️  forge skew: reliant (built 2026-10-01 01:31) records no forge commit, and go.work bridges /src/forge, which changed after it was built (now 750833054b78) — generated code and library differ. Fix: rebuild reliant from that checkout, or unbridge: go work edit -dropuse=/src/forge
```

## A forge that is not the project's hands the command over

The forge on PATH is routinely not the forge a project pins: a Reliant
workspace bundles one at image-build time, a laptop keeps whatever was last
installed, and every project pins its own `forge_version`. forge's templates,
KCL schemas and analyzers live inside the binary, so the wrong forge either
rewrites a project with the wrong output or fails with an error that blames
the project (`Cannot add member 'release_dependencies' to schema ...`).

So forge does not refuse and print a recipe. It runs the right forge. Before a
command that writes the project (`generate`, `scaffold`, `package new`,
`component install`, `env new`, `project rescaffold`/`delete`/`migrate`, and
the build/deploy commands) or renders its KCL (`env render|shape|config|diff|up`,
`kcl`, `doctor`) runs under a forge other than the project's, it execs the
project's forge with the same arguments, environment, stdio and exit status, and
prints one line on stderr saying so:

```
forge: this forge is v0.1.45-…, but the project wants forge_version v0.1.46-… — running forge v0.1.46-… (just built) instead (--allow-version-skew, or FORGE_ALLOW_VERSION_SKEW=1, to run this one)
```

Which forge is "the project's", in order:

1. **A linked checkout.** A project whose go.work `use`s a forge checkout or whose
   go.mod directory-`replace`s it (the dev link) compiles against that source, so
   the forge it runs is a build of that checkout, rebuilt only when HEAD or its
   edits change. (If this binary already was built from it, nothing is done.)
2. **`.tools/bin/forge`**, when it reports the pinned version (what `task tools`
   and `scripts/install-tools.sh` install).
3. **The pinned module version**, built with `go install
github.com/reliant-labs/forge/cmd/forge@<forge_version>` into a
   content-addressed cache, `<user cache dir>/forge/pinned/<version>/<os>-<arch>/`.
   A pseudo-version names one commit, so an entry never goes stale; the build
   happens once (about a minute), is renamed into place only when complete, and
   the cache keeps the newest few versions.

Read-only runs (`--dry-run`, `--check`, `lint`, `project audit`, `env list`,
every inspection verb) stay on the forge you ran and only warn, as before.

The explicit "run THIS forge" is `--allow-version-skew` or
`FORGE_ALLOW_VERSION_SKEW=1`: no hand-off, and the old warn/refuse rules apply
(a rewriting command under the wrong forge is refused with the exact
`go run …@<pin>` line, which now also carries the reason the pinned forge could
not be used, if that is why: offline, or a version nobody published). A
delegated forge sets `FORGE_VERSION_DELEGATED=1` and never hands off again, so
the two cannot loop. On Windows, which has no `exec`, the old process stays as
the parent and relays the exit status.

## The compatibility gate

`forge generate` checks, before writing anything, that the forge the project
compiles against can satisfy the code about to be generated:

- the project's forge must be **>=** the generating binary's version, since
  generated code can only call symbols that existed at the generator's version.
  A project resolving a NEWER forge is fine — that is the ordinary upgrade
  order;
- a project bridged to a local checkout always passes: it compiles against
  source, so there is no version to be behind;
- an unreleasable binary against a proxy-pinned project is refused, with the
  bridge command;
- a DIRECT require on the retired `forge/pkg` module is refused. Both
  `github.com/reliant-labs/forge` and `github.com/reliant-labs/forge/pkg`
  provide `forge/pkg/*` import paths, so requiring both makes every one of
  them ambiguous and nothing compiles. Only the project's own go.mod is read:
  forge does NOT inspect the dependency graph looking for it. It used to, and
  that failed a project's generate because one of its DEPENDENCIES had not
  bumped — a graph forge does not own, and advice the reader could not act on.
  `go.mod` resolution is sufficient: MVS selects one version of forge for the
  whole build, so the library side is coherent whatever individual pins say,
  and a genuine binary/library mismatch is an ordinary compile error that
  emit-validate-rollback already catches empirically.

This replaced a probe that compiled a throwaway program against a hand-listed
set of symbols. The inequality covers every symbol ever added, needs no registry
to keep current, and costs one `go list` instead of a `go build`.

## Releasing

`task release:forge -- vX.Y.Z` — one tag, one commit. See `docs/releasing.md`.
