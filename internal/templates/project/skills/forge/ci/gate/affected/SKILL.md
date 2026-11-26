---
name: ci/gate/affected
description: The gate's affected mode on a branch in full — which Go packages unit-tests runs and when it falls back to the whole suite (CI_FULL, CI_BASE, ci.gate.affected_ignore), migrations through the db embed, ci.gate.affected_rules (claim non-Go paths and select Go test packages, frontend test-<name> rows via a test:related script, or the full suite with a reason), and extra checks gated by `paths`. Use when a branch's gate runs the whole suite and you want to know why, or when a frontend, KCL, script or extra package (electron/) should select only its own tests.
---

# Affected mode: what a branch's gate tests

On a non-default branch, `unit-tests` runs `task test -- <packages>` for only the
Go packages whose test binary (the package, its tests, everything they import)
contains a package the branch changed since `git merge-base HEAD origin/<default>`
— committed, staged, unstaged and untracked changes alike. A changed `.go` file,
a file a package embeds, or a file under a package's own `testdata/` changes that
package. The gate prints the mode and why, e.g.
`unit tests: affected mode — 7 of 212 test packages reach the 4 changed file(s) since origin/main`.

It **falls back to the whole suite** when any other file changed (`go.mod`,
`go.sum`, a `testdata/` outside a package, KCL, proto, the Taskfile, scripts,
a frontend's source — nothing proves which tests read them), on the default
branch, when nothing changed, without `origin/<default>`, and with `CI_FULL=1`
or `--full`. A branch stacked on another names its parent with `CI_BASE=<ref>`
or `--base`. List files no Go test reads in `ci.gate.affected_ignore`
(`["*.md", "docs/**"]`) so they do not force it. A direct `task test` always
runs the whole suite: affected mode is the gate's, for a branch.

A migration needs nothing: forge's generated `db/embed_gen.go` embeds
`db/migrations/*.sql`, so a changed migration is a file the `db` package embeds
and selects every test package that imports it, without a full fallback.

## Saying what other paths select: `ci.gate.affected_rules`

Each rule claims the changed files its `paths` match (minus `except`; globs as
in `affected_ignore`) and says what they select. A claimed file no longer forces
the whole suite; a file no rule matches behaves exactly as above. A Go file,
embedded file or testdata keeps its own mapping, and a rule may add packages
for it too.

```yaml
ci:
  gate:
    affected_rules:
      - paths: ["sql/queries/**"]           # required
        except: ["sql/queries/README.md"]   # optional
        packages: ["./internal/store/...", "./internal/db"]  # these packages' tests run
      - paths: ["frontends/web/**"]
        frontends: [web]                    # a test-web row (below)
      - paths: ["deploy/kcl/**"]
        full: true                          # the whole suite, as an unmatched file,
        reason: "KCL is read by the render tests"  # with this printed as why
```

- `packages` are Go package patterns: `./dir` (one package), `./dir/...` (the
  subtree), or an import path in either form. They are matched against the
  module's packages that have tests, and run themselves (their importers are not
  added; name them too if you mean them).
- `frontends` name discovered frontends (the directory under `frontends/`). For
  each selected one the gate adds a `test-<name>` row after `unit-tests` (lane
  and group `test`, heavy, after `deps-<name>`). It runs the frontend's
  `test:related` package.json script with the changed files under the
  frontend, relative to it, as arguments; with no such script, or no changed
  file under the frontend (a rule claimed a proto for it), the whole `test`
  script as `task test` runs it. forge reads no test runner's config: the script
  is the frontend's word for which tests a file reaches. A vitest frontend
  declares `"test:related": "vitest related --run --passWithNoTests"`.
- `test:related` only ever gets source modules. If **any** changed file under
  the frontend is not one, the row runs the whole `test` script and its note
  names the file. A related-test lookup follows imports, and a config file,
  manifest or lockfile changes every test while being imported by none, so
  passing it would select nothing and look green. The rule is generic, and it
  reads no tool's config. A file counts as source when:
  - its extension is `.ts .tsx .mts .cts .js .jsx .mjs .cjs .vue .svelte
    .astro .css .scss .sass .less`; JSON, YAML, HTML, `.env` and assets are not;
  - it is not a `*.config.*` file anywhere, and not a dotfile;
  - it is not a tool's file directly in the frontend root (`vite.*`,
    `vitest.*`, `jest.*`, `playwright.*`, `cypress.*`, `next.*`, `svelte.*`,
    `astro.*`, `nuxt.*`, `tsconfig*`).

  `package.json`, lockfiles, `.npmrc` and `tsconfig*.json` therefore always run
  the whole suite. A deleted source file is not passed to `test:related`.
- `full: true` cannot also select packages or frontends; a rule that selects
  nothing is an error (a path no test reads belongs in `affected_ignore`). A
  rule naming a frontend the project does not have fails the plan.
- Each rule that matched prints one line under the mode line: what it claimed
  and selected. When `unit-tests` reaches no package it is N/A, as without
  rules, and the `test-<name>` rows still run. In full mode there are no
  `test-<name>` rows: `task test` runs every frontend's suite.

### Walkers, helper importers, generated code and whole rows

Four optional rule fields cover what a path list cannot say:

```yaml
ci:
  gate:
    affected_rules:
      - on: go-source                        # every changed .go file (paths optional)
        packages: ["./internal/guard"]       # a test that reads the whole tree's Go source
      - paths: ["db/migrations/**"]
        packages_importing: ["./internal/db/testutil"]  # tests whose test files import it
      - paths: ["proto/**"]
        packages: ["./gen/{dir}"]            # proto/x/v1/a.proto → ./gen/x/v1
        full_checks: [test-web, electron]    # these rows run whole
```

- `on: go-source` matches any changed `.go` file besides `paths`. It adds what
  the rule selects but claims nothing: a Go file outside every package still
  runs the full suite.
- `packages_importing` names helper packages (import path or `./dir`). The test
  packages whose test files import one run (go list's `TestImports` and
  `XTestImports`); packages that only link it transitively do not.
- `{dir}` in a `packages` pattern is the matched file's directory below the
  matching glob's literal prefix (`proto/**` → `proto/`), substituted per file.
- `full_checks` names gate rows: a path-gated check runs although no change is
  under its `paths`, and a `test-<frontend>` row is added if needed and runs
  the whole `test` script instead of `test:related`. `full: true` still means
  the whole unit lane.

## Capping the reach: `ci.gate.affected_reach`

By default a changed package selects every test package whose binary links
it, however far up the import graph. A hub package can make that most of the
module. `affected_reach` caps it:

```yaml
ci:
  gate:
    affected_reach:
      mode: direct        # all (default) | direct
      transitive_cap: 20  # transitive importers allowed to join a direct run
```

Under `direct`, unit-tests runs the changed packages, the packages a rule
names, and their direct importers (a package or its tests import a changed
package). The transitive importers join too when there are at most
`transitive_cap` of them; past the cap none of them run, and each is named as
deferred to the full gate: one `deferred to the full gate: <pkg>` line each,
the count on the unit-tests row's note, and `affected.deferred` in the
`--json` document. Nothing is dropped silently, and the full suite (the main
tier, `CI_FULL=1`) still runs them.

## Extra checks that run only when their paths change

An extra check with `paths: [globs]` runs on a branch in affected mode only when
a changed file matches; otherwise its row is N/A (`affected mode: no change under
electron/**`), which fails nothing and is never cached as a PASS. In full mode
(the default branch, `CI_FULL=1` / `--full`, `--tier main` or `full`, no base to
diff against) it always runs. The unit lane's own fallback (an unclaimed
`go.mod` change) does not make it run. `paths` only gates the check: a change
under them still sends `unit-tests` to the whole suite unless `affected_ignore`
lists them too (no Go test reads `electron/**`) or a rule claims them.

```yaml
      - name: electron-tests
        command: [npm, --prefix, electron, test]
        lane: test
        paths: ["electron/**"]
```

### `paths_go_closure`: run when a tool's Go build changed

A check that runs a generator or tool built from the module (a docs generator,
a codegen hook) depends on that tool's whole compile closure, which no glob list
tracks. `paths_go_closure: [<go package patterns>]` makes it run on a branch
when a changed file is in their build closure (`go list -deps`: the module's
packages they compile, their Go and embedded files, a new `.go` file in one of
those directories, `go.mod`/`go.sum`), besides any `paths` glob it matches.
Test files are not part of a build and do not count. When go list fails the
check runs (reach unknown is never N/A); in full mode it always runs.

```yaml
      - name: docs-generate
        command: [go, run, ./tools/docgen, -check]
        paths: ["docs/**"]
        paths_go_closure: ["./tools/docgen/..."]
```

## Lockfile rows: lockfile-<frontend>

On a branch in affected mode, when a frontend's `package.json`, its lockfile (the
nearest one, a workspace root's included) or a pnpm `pnpm-workspace.yaml`
changed, the gate adds a `lockfile-<name>` row just before `deps-<name>`. It asks
the frontend's own package manager whether a frozen install still accepts the
lockfile, without touching the checkout's `node_modules`: `pnpm install
--frozen-lockfile --lockfile-only`, `npm ci --dry-run --offline` (no registry
requests), or an isolated Yarn install in a disposable workspace copy:
`yarn install --immutable --mode=skip-build` (berry, scripts disabled) or
`yarn install --frozen-lockfile --ignore-scripts` (classic). Yarn may fetch
packages, but its install output is removed with the copy. This validates
manifest/lock consistency, not offline package availability. The
tests run against whatever `node_modules` is on disk, so without this row a
hand-edited lockfile that every fresh install rejects passes the gate. No
manifest or lockfile changed, full mode, or no lockfile: no row.

## Why each package runs, the build set, and a what-if

Under the mode line the gate prints one line per selected package (up to 30):
`ex.com/m/b ← a/x.go (imports ex.com/m/a)` — the first changed file that
reached it and the edge: `changed` (the file is part of it), `imports <pkg>`
(its test binary links the changed package) or `ci.gate.affected_rules[<i>]`.

`--json` adds an `affected` object to the forge.ci.gate/v1 document whenever the
gate computed the mode: `full`, `reason`, `changed`, `packages`, `why` (per
package: `package`, `file`, `edge`), `files` (per changed file: `package`,
`rules`, `ignored`, `note`), and `build`: every module package, with tests or
not, whose build contains a changed package, the set a compile phase needs.

`--plan` prints that decision without running a check; `--files` judges the
files you name instead of the diff:

```bash
forge ci gate --plan                                    # this branch
forge ci gate --plan --files internal/a/a.go,docs/x.md  # what would these run, and why
forge ci gate --plan --json | jq '.build'               # the compile set
```
