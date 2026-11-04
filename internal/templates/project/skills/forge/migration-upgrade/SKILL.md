---
name: migration-upgrade
description: Upgrade a forge project to a newer forge binary version — version pinning in forge.yaml, the per-version migration skills, the supported upgrade window (N minors back), and the deprecation cycle policy.
---

# Upgrading a forge project

Use this skill when the forge binary on your PATH is newer than the
`forge_version` recorded in `forge.yaml`, or when `forge generate` warns
about a version mismatch.

## How forge tracks versions

Every `forge.yaml` carries a `forge_version` field set at scaffold time
to the version of `forge project new` that produced the project.

For a project whose `go.mod` requires `github.com/reliant-labs/forge` (every
service project), the pin **follows go.mod**: bumping forge is

```bash
go get github.com/reliant-labs/forge@vX.Y.Z && go mod tidy
forge generate        # converges forge.yaml's forge_version to vX.Y.Z
```

`forge generate` rewrites that one scalar and says so (`📌 forge_version … →
vX.Y.Z`). It never writes the version of the forge binary running it — only
the version go.mod names, which every clone resolves identically. The one
exception is a hop `forge project upgrade` has to walk (a codemod, a
migration this project still needs, or more minors than the supported
window): generate leaves the pin, prints why, and names `forge project
upgrade`.

For a CLI or library whose go.mod does not require forge, `forge.yaml`'s
`forge_version` IS the pin (CI installs that version), and only `forge
project upgrade` moves it.

Legacy projects that predate the field are treated as `0.0.0`. They
get a one-time nudge from `forge generate` to run `forge project upgrade` so
the baseline can be pinned.

## The supported upgrade window

forge supports upgrading from at most **2 minor releases back** in a
single run (the Istio model). A project further back does a **staged
upgrade**: step to an intermediate release first, then continue.

```
v0.3  v0.4  v0.5  v0.6  v0.7        binary: v0.7
 │                 └──────┴─ project at v0.5: inside the window, one step
 └─ project at v0.3: 4 minors back — stage through v0.5 first
```

Attempting too large a jump is refused with the intermediate version
named:

```text
$ forge project upgrade --to v0.7.0
  v0.3.0 is 4 minor releases behind v0.7.0 — at most 2 minors back
  is supported in one step

  staged upgrade: run 'forge project upgrade --to v0.5' first, then
  re-run the original command
```

Why a window at all: each migration is written against the shapes of the
releases inside it. Migrations compose multiplicatively — N releases back
is N playbooks that must all still apply cleanly, in order, to a tree
none of their authors ever saw. Bounding N is what keeps the registry
small enough that every migration in it is still true.

## How to upgrade

```bash
# Inspect the current pin and the binary version.
grep forge_version forge.yaml
forge version

# A project whose go.mod requires forge: bump it there first.
go get github.com/reliant-labs/forge@vX.Y.Z && go mod tidy

# See which migrations this project actually needs.
forge project upgrade list

# Preview what the upgrade would change.
forge project upgrade --dry-run        # alias for --check

# Apply.
forge project upgrade                  # bumps to the binary's version
forge project upgrade --to v0.5.0      # bumps to a specific version

# Force-overwrite user-modified frozen files (rare; usually you want
# to inspect the diff first and reconcile manually).
forge project upgrade --force
```

`forge project upgrade` is one verb that applies every mechanical fix forge
knows for the range the pin crosses, in this order:

1. **modules** — `go get` / `go mod tidy` the forge requirement of every
   module that has one.
2. **codemods** — the `forge fix <name>` codemods keyed to the releases the
   range crosses (the ones marked to run on every crossing). A pin to a
   pre-release of a release (a Go pseudo-version of main) crosses that
   release's codemods too.
3. **templates** — frozen Tier-2 files diffed against the latest templates;
   unmodified files are auto-updated, modified ones are reported and kept
   (`--force` adopts them).
4. **pin** — `forge_version` in `forge.yaml` moves to the target.
5. **playbooks** — each crossed playbook's `automation:` steps (a playbook
   declares `fix:<codemod>` for the steps that are mechanical). A playbook
   whose detection still matches afterwards is left for a person.
6. **vendored-contracts** — see below.
7. **generate** — `forge generate`.
8. **locks** — a lockfile that disagrees with its `package.json` (generate
   edits dependency ranges) is refreshed by the package manager.
9. **lint** — `forge lint --json`. Errors are what is left for a person;
   findings of a rule still in its warn-first grace are listed as upcoming,
   with the release they gate from.

A codemod that replaces files you own (it cannot be reviewed except as a
`git diff`) only runs over a clean tree: upgrade leaves it and reports it under
"Still needs a person" rather than rewrite uncommitted work. `--allow-dirty`
runs it anyway. The pin files a bump itself edits (`go.mod`, `go.sum`,
`go.work`, `forge.yaml`) do not make a tree dirty.

`--check` (alias `--dry-run`) writes nothing and runs neither generate nor
lint; it still names each codemod rewrite and lockfile it would touch.
`--skip <phase>` (repeatable) omits `codemods`, `playbooks`, `vendored-contracts`,
`generate`, `locks` or `lint`; `modules`, `templates` and `pin` always run. It is how
a big rewrite goes into its own PR: `--skip playbooks` leaves the codemods a playbook
automates (`connect-errors`, `composition-graph`) for `forge fix <name>` on a branch
of their own, and `--skip codemods` does the same for the registry codemods the range
crosses. The pin moves in the same run, so a skipped codemod is not picked up by the
next upgrade: the skipped phase's `detail` in `--json` names each to run yourself.

### Exit codes and `--json` — the contract a pin bot calls

| Exit | Meaning | The caller |
| --- | --- | --- |
| `0` | Converged: every fix applied, nothing left for a person. | Merge candidate. |
| `1` | Failed: an error stopped the run (generate failed, a codemod errored, a bad flag, a staged-upgrade refusal). | Read `error`; do not retry blindly. |
| `2` | Could not determine: a package manager or registry could not be reached, or lint printed no readable verdict. | Retry. |
| `9` | Needs a person: everything mechanical is applied and `needs_human` is not empty. Re-running changes nothing. | Open the PR as a draft with `needs_human` in the body. |

`forge project upgrade --json` prints **one** document on stdout (human
progress goes to stderr), for a failing run too, so the caller can read what
was applied before the failure. Schema `forge-project-upgrade/v1`:

```json
{
  "ok": true, "exit_code": 0, "error": "",
  "schema": "forge-project-upgrade/v1",
  "mode": "apply",
  "from": "v0.1.45", "to": "v1.0.0",
  "converged": true,
  "phases": [{"name": "codemods", "status": "done", "detail": ""}],
  "codemods": [{"name": "operatorkit", "version": "v0.1.46", "source": "upgrade",
                "rewrites": ["internal/legacyops/ops.go: …"], "next": [], "blocked": []}],
  "locks": [{"path": "frontends/web/package-lock.json", "runner": "npm",
             "details": ["…"], "pending": false, "refreshed": true}],
  "playbooks": [{"id": "v1.0.0", "automation": ["fix:composition-graph"], "resolved": true}],
  "vendored_contracts": [{"name": "controlplane", "status": "lag", "revendored": true}],
  "needs_human": [{"kind": "lint-error", "rule": "forgeconv-…", "file": "internal/x.go",
                   "line": 7, "message": "…", "fix": "…"}],
  "upcoming": [{"rule": "forge-frontend-lock-drift", "count": 1, "introduced_in": "v1.0.0",
                "gates_from": "v1.0.1", "fix": "…", "examples": ["…"]}],
  "template_drift": ["Dockerfile"]
}
```

- `ok` and `exit_code` are the process's own: `ok` is true exactly when the exit
  is 0. Every list is an array, never `null`.
- `converged` is `ok` with an empty `needs_human`.
- `phases[].status` is `done`, `skipped`, `planned` (`--check`) or `failed`.
- `needs_human[].kind` is `lint-error`, `playbook`, `codemod-blocked`,
  `codemod-skipped`, `lock-drift` or `vendored-contract`. Each carries `fix`:
  the command or skill that clears it.
- `upcoming` is informational and never changes the exit code.

### Re-vendoring a contract the project copies from another repository

forge does not know what a project vendors. The project declares it, and
upgrade runs the declared hook when the project's own check says the copy lags:

```yaml
# forge.yaml
upgrade:
  vendored_contracts:
    - name: controlplane
      report: tmp/ci/reliant-vendored-proto.json   # written by the project's own check
      schema: reliant-vendored-proto/v1            # a report claiming another is never acted on
      env_path: revendor.env                       # optional: an object in the report, exported to the commands
      commands:
        - [node, .github/scripts/sync-controlplane-proto.mjs]
        - [npm, run, "proto:generate:controlplane"]
```

The report is JSON with a top-level `schema` and a `status` of `in-sync` or
`lag`. `lag` runs `commands` in order from the project root, before generate;
`in-sync`, or no report yet, runs nothing; any other status or schema is
reported under `needs_human` as `vendored-contract`. The report format of
control-plane's `reliant-vendored-proto/v1` (`status`, `files[]`, `revendor.env`)
is one such producer.

## Reading per-version migration skills

A migration skill at `migrations/<version>` is the playbook for the
breaking changes one RELEASE introduced. Load it by the path
`forge project upgrade list` prints for it.

**Apply them in the order `forge project upgrade list` prints them —
oldest release first.** Each assumes the ones above it have already
landed; running a later release's playbook against a tree still in an
earlier shape is how a staged upgrade corrupts a project.

Every skill follows the same six-section shape:

1. **What changed.** A one-paragraph technical description.
2. **Detection.** How to identify whether your code has the old shape.
3. **Migration (deterministic part).** Commands that `forge project upgrade`
   already runs for you (regen, build).
4. **Migration (manual part).** What user-edited code might need to
   change. This is where the LLM does its real work.
5. **Verification.** `go build && go test && forge lint` plus any
   shape-specific checks.
6. **Rollback.** How to back out if something breaks.

forge does not automate the manual steps, because they touch hand-written
code that an LLM is better placed to reason about than a regex-based rewrite.
The steps that ARE mechanical are declared in the playbook's `automation:` and
run by `forge project upgrade` itself.

Once you've finished a migration's steps:

```bash
forge project upgrade apply <version>   # records it in .forge/migrations.json
```

## Currently shipped migrations

**None.** forge ships no migration skills right now — no release inside
the supported window carries a breaking change that a project can still
be on the wrong side of.

An empty registry is the normal steady state, not a gap. `forge project
upgrade list` says so directly:

```text
No migrations shipped by <this binary> — no release in the supported
upgrade window carries a breaking change.
```

<!-- @forge-only:start -->

## Authoring a migration skill (forge core authors)

Add a migration when a release changes the *shape* of a generated
artifact in a way user code or downstream tooling can observe. A new
annotation, a renamed helper, a changed file layout, a removed config
key — those need one. Pure internal refactors (swapping the regex engine
that parses proto annotations) do not.

### Where it goes and what it's called

One directory per RELEASE, named for that release:

```
internal/templates/project/skills/forge/migrations/v0.5.0/SKILL.md
```

The directory name IS the identifier: it's what `forge project upgrade
list` prints, what `forge project upgrade apply <id>` takes, and what
lands in `.forge/migrations.json`. One skill covers everything that
release broke — do not create a directory per individual change.

### A breaking change registers its codemod, and upgrade runs it

A PR that breaks a consumer (the API-diff gate names it with `API-BREAK:
codemod=<name>`) ships the `forge fix <name>` codemod in `internal/codemod`.
`forge project upgrade` then runs it when the range crosses the release, by
one of two declarations — the registration is incomplete without one, and
`TestEveryRegisteredCodemodIsReachableFromUpgrade` fails otherwise:

- `Upgrade: true` on the codemod, for a rewrite that keeps the program's
  meaning exactly (a rename, a moved symbol). It runs on every crossing of its
  `Version`, and decides from the tree, so it rewrites nothing where the old
  shape is absent.
- `automation: fix:<name>` in the release's playbook, for a codemod that
  replaces files wholesale or lists what it cannot decide. It runs only when
  the playbook's `detection` matches, only over a clean tree (or
  `--allow-dirty`), and a `Blocked` result lands in `needs_human`.

### Frontmatter contract

```yaml
---
name: v0.5.0
description: the deploy target moved from forge.yaml into KCL
relevance: migration
version: v0.5.0
detection: grep -q '^dev_target:' forge.yaml
---
```

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | Human-readable title. |
| `description` | yes | One line; this is what the worklist prints. |
| `relevance` | no | `migration` — defaulted from the directory, declare it anyway. |
| `version` | yes | The release that introduced the break. **Must equal the directory name** (a test pins this). |
| `automation` | no | Comma-separated mechanical steps `forge project upgrade` runs when the playbook applies. Each is `fix:<codemod>` (a registered `forge fix` name). A playbook that still matches its detection afterwards is reported as needing a person. |
| `detection` | yes | POSIX shell snippet, run in the project root by forge's in-process interpreter (identical on every OS, no `sh` needed). Exit 0 means the project still has the old shape. |

A migration applies when **both** hold: the project's pinned
`forge_version` is BELOW `version`, and `detection` exits 0.

### Writing detection: state, never version

Detection must test what the tree CONTAINS, never what version it claims
to be. The version half of the decision is already made by the `version`
field, and duplicating it there breaks the two cases that matter most:

- **Unpinned or dev-built projects.** A project bridged to a local forge
  checkout pins a pseudo-version; one predating `forge_version` has no
  pin at all. Neither has a usable position on the timeline, so forge
  falls back to detection ALONE. A version-based detection returns
  nothing useful here.
- **Multi-release jumps.** A project crossing four releases at once gets
  all four migrations. Each one must independently answer "does this tree
  still have my old shape?" — including after the previous three have
  already rewritten parts of it.

Good detection is a grep or a file test. Detection runs in-process, so
stick to shell builtins (`test`, `[`, `&&`, `||`, `!`) plus `grep` / `rg`,
which forge provides as portable built-ins supporting only `-q -i -F -E -r
-e` (`rg` is recursive by default). Any other external tool will not exist
on stock Windows. Three examples of the
`detection:` frontmatter value:

```text
test -f pkg/app/wire_gen.go
rg -q 'forge:server-set' internal/
grep -q '^dev_target:' forge.yaml
```

A migration with no detection applies to NOTHING — it cannot demonstrate
any project needs it, so it stays out of every worklist rather than
landing in all of them.

### The pruning rule

**When a release ages out of the supported window, DELETE its skill.**
Not archive, not mark retired — delete.

The window is `supportedUpgradeWindowMinors` in
`internal/cli/upgrade_migrations.go` (currently 2). A project older than
that cannot reach the migration anyway: it gets the staged-upgrade
refusal instead, which routes it through an intermediate release where
the migration was still in the window.

A skill nothing can reach is a skill nothing tests. Keeping it around
grows the registry without bound and leaves playbooks that quietly stop
being true. Deleting it is what makes the honest answer — "stage through
vX first" — the only answer that path can give.

<!-- @forge-only:end -->

## Deprecation cycle policy

When forge changes the shape of a generated artifact:

- **Old shape works for N versions with warnings.** N is at least 2
  minor versions (e.g. an old shape introduced before 1.4 stays
  buildable, with deprecation warnings, through 1.5 and 1.6). This is the
  same 2 the upgrade window uses, and not a coincidence: a project inside
  the window is a project whose shapes forge still supports.
- **Old shape removed in next major.** A 2.0 release is allowed to
  delete the old shape entirely.
- **Behavioural fingerprints preserved across the cycle.** Mock
  not-set error strings, slog attribute keys, span names, and metric
  names are locked by fingerprint tests. A migration that breaks one
  of those gets called out explicitly in the skill's "What changed"
  section.

## See also

- `migration` — the top-level skill for porting a non-forge project
  *into* forge in the first place. This skill is for upgrading an
  already-forge project.

## Post-merge gotchas

Two things to know when `forge project upgrade` interacts with branches:

- **Recommended branch order: upgrade on `main` first, then merge into
  work branches.** The reverse (running `forge project upgrade` on a work
  branch and then merging `main` back in) produces ApplyDeps codemod
  conflicts that are painful to reconcile — the bootstrap/wire layer
  gets rewritten twice from different baselines, and the textual merge
  cannot tell which side owns which call. Treat `forge project upgrade` like a
  global codemod: land it on `main`, then rebase every open branch.
- **Generated-file merge conflicts: regenerate, don't hand-merge.**
  There is no global checksums manifest to reconcile anymore — each
  generated file carries its own `forge:hash` marker, so a textual
  merge of two pristine renders produces a file whose marker no longer
  verifies. Resolve by accepting either side and running
  `forge generate` (the writer heals pristine-but-stale vintages
  loudly); only a file BOTH branches hand-edited needs real conflict
  resolution, and the drift guard will name it.
