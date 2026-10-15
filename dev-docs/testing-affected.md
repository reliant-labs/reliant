# Run only the tests your change can reach

```bash
make test-affected        # run them
make test-affected-plan   # print what would run, and why, without running it
```

`make test-affected` diffs your tree against the merge-base with `origin/main`
(committed, staged, unstaged and untracked files all count), works out which
tests can observe each changed path, and runs only those. It prints the reason
next to every selection, so a surprising test is one line away from its cause.

It is the first thing to run before pushing. It does not replace the full lane
(`go test ./...`, `npm test`): that is what a release or a `go.mod` bump needs.

## What runs

| Lane | Command it runs | Selected by |
|---|---|---|
| Go | `go test -short -timeout 60s <pkgs>` (cached, no `-race`) | the changed package, **plus every package whose test binary imports it** |
| web | `npx vitest run <files>` in `web/` | every vitest file that imports a changed module, transitively |
| electron | `npm test` in `electron/` | any change under `electron/`, or a changed file an electron test names |

Knobs (environment): `TEST_BASE=<rev>` moves the diff base (a stacked branch),
`TEST_FULL=1` runs every lane in full, `LANES=go,web` picks lanes. For other
flags: `go run ./tools/test-affected -h` (`-json` prints the plan,
`-files a,b` or `-files @list.txt` asks "what if only these changed?").

## The rule table

A changed path is matched against these rules (`tools/test-affected/rules.go`),
and also mapped to its Go package and to the tests that name it. A path that no
rule claims and no Go package owns runs **everything** (fail-safe).

| Changed path | Effect |
|---|---|
| a `.go` file | its package + reverse dependencies (test binary imports it); packages that walk every Go file (`architecture`, `features`, ...) |
| a `_test.go` file | only its own package |
| a Go file behind a build tag (`e2e`, `live`, ...) | nothing: `go test -short` does not build it |
| a file a package `go:embed`s | its package + reverse dependencies, like code |
| a file a Go test names in a path literal (`deploy/...`, `config/...`) | only the packages that name it |
| `db/migrations/**`, `internal/db/migrations/**` | the packages with DB-backed tests. Their tests skip under `-short`, so they run only when **you** set `DATABASE_URL` (then under `REQUIRE_TEST_DB=1`); otherwise the plan says so |
| `proto/**/*.proto`, `proto-vendor/**/*.proto` | its generated Go package and the vitest files importing its `_pb.ts` |
| `web/src/**` module | the vitest files importing it (import graph below) |
| `web/src/**/*.test.ts(x)` | that test only |
| a file a web or electron test reads at run time | those tests |
| `web/e2e/**` | nothing (Playwright is separate) |
| `electron/**` | the electron lane |
| `docs/**`, `**/*.md`, `.github/**`, `research/**`, lint config | nothing, unless a test names the file |
| `go.mod`, `go.sum`, `tools/tools.go` | **Go: full** |
| `package.json`, lockfiles at the root | **web and electron: full** |
| `web/package.json`, `vite.config.ts`, `tsconfig*.json`, `tailwind`/`postcss` config, `web/src/test/setup.ts` | **web: full** |
| `Makefile`, `forge.yaml`, `buf*.yaml`, `sqlc.yaml` | **everything: full** |
| `proto/**` that is not a `.proto` | **Go and web: full** |
| a deleted web module | **web: full** (its importers can no longer be found) |
| anything else | **everything: full** |

## How the selection works, and where it errs

**Go.** `go list -test -json ./...` gives each package's test-binary
dependency set; a package runs when that set contains a changed package. This
is exact for compiled code. The inexact part is data a test reads from disk: the
selector scans each package's Go sources for string literals that name a tracked
repo path (`filepath.Join(root, "deploy", "kcl")`) and selects those packages
when that path changes. It over-approximates on purpose: a test run for no
reason costs seconds, a test wrongly skipped costs a red main.

**Web.** vitest's own `related` mode builds the same graph by transforming every
test file: `vitest list --changed` took 65 seconds of wall time and 26 seconds of
CPU just to name the files, before running any, on a loaded machine.
`tools/test-affected/webgraph.go` scans quoted
`./x`, `../x` and `@/x` specifiers instead, so the graph takes milliseconds and
`vitest run` gets an explicit file list. Every quoted specifier is an edge, whether
it is an `import`, `import()`, `vi.mock()` or `import.meta.glob`, **except**
`import type` / `export type ... from`, which `verbatimModuleSyntax` erases:
without that, a type import of a hub module (`routeSchemas.ts`) made one
leaf change select 523 of 750 test files. The result was checked against vitest's
own `related` selection for five changed files (`logger.ts`, `transport.ts`, `ChatMessage.tsx`,
`deliveryScope.ts`, a test file): it was a superset every time, with nothing missing
(it also selects the tests that read whole directories of `web/`, which vitest
cannot see).

**Known blind spots** (both directions are over-selection except these):

- A Go test that builds a path at run time from non-literal parts reads a file
  the scan cannot name. It still runs when its own package or a dependency changes.
- A web test that reads a file through a variable path. The marker scan
  (`readFileSync`, `readdirSync`, `process.cwd()`, `__dirname`) selects any test
  that reads files and names the changed file as a literal.
- `-short` hides DB-backed tests. That is today's inner-loop behavior too.

## Parallelism and timeouts

- **vitest** (`web/vite.config.ts`): one worker per core, capped at 8, and the
  5-second test timeout scales 1x to 6x with the one-minute load per core. On an
  idle machine nothing changes. Under load (many agents share the box) a "timed
  out in 5000ms" is the machine, not the code. `VITEST_MAX_WORKERS` and
  `VITEST_TIMEOUT_MS` override both.
- **go test**: `-p` defaults to `GOMAXPROCS`, which is right when one `go test`
  runs. Pass `go run ./tools/test-affected run -p N` to lower it when several
  agents run builds at once.

## Changing the selector

`tools/test-affected` is a plain Go package with table tests
(`plan_test.go`). A change to it selects its own tests, like any package. The
rules live in one table in `rules.go`; add the path there and a test row here.
