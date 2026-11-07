# The forge demo's recorded scaffold run

`capture-forge-scaffold.sh` writes the data behind "How this app was built" in
the forge demo console (`/forge/demo?section=built`, and the demo host's tour
step `fg-built`):

- `src/components/Forge/Demo/howBuilt/data/forge-scaffold-cast.json`, the run
  as a terminal recording;
- `src/components/Forge/Demo/howBuilt/data/forge-scaffold-manifest.json`, every
  file the run left, counted.

Both come from a real run. Nothing in them is typed by hand, and the UI parses
them strictly (`howBuilt/fixtures.ts`), so a file the script did not write
fails the tests.

## Running it

```bash
frontends/web/scripts/demo/capture-forge-scaffold.sh
```

It takes a few minutes, most of it `forge lint`. Run it after every forge bump
in `go.mod`, and commit the two JSON files with the bump. `FORGE_BIN=<path>`
uses a forge you already built (it must report the pinned version);
`KEEP_WORK=1` keeps the temp directory so you can look at the app.

It needs what forge needs to make a project: Go, Node with pnpm, and git.
`forge lint` starts its own throwaway Postgres.

## What it runs

In a fresh temp directory, with a plain environment (the toolchains'
variables only, so an agent harness or CI variables don't change what forge
does):

1. `forge project new shop --service orders --service inventory --frontend web`
2. The protos a person writes: in each service's proto, forge's
   `// TODO: Add your RPC methods here.` line is replaced with one custom rpc
   (`CancelOrder`, `Reserve`, each declaring its activity event) and one
   `// forge:entity` message (`Order`, `Stock`), plus the rpc's request and
   response. The text is `HAND_WRITTEN` in `capture-forge-scaffold.mjs`.
3. `forge scaffold`
4. `forge lint --quiet`
5. `go build ./...`

Any command that exits non-zero stops the run, and nothing is written.

## The cast (`forge-demo.scaffold-cast/v1`)

- `steps[]`: each command with its working directory, exit code, real running
  time (`real_seconds`), played length (`duration`) and output `events`
  (`[seconds, text]`); each edit with the file, the line it replaces and the
  text written.
- Output is kept as printed, except:
  - waits longer than `idle_time_limit` (1 s) play as 1 s, as asciinema's
    `idle_time_limit` does, and chunks less than 50 ms apart are merged;
  - colour codes are removed;
  - machine paths read as `~`: the work directory is `~`, `$HOME` is `~`,
    `$XDG_CACHE_HOME` is `~/.cache` and `GOMODCACHE` is `~/go/pkg/mod`.
- `toAsciicast()` in `howBuilt/fixtures.ts` turns it into a standard asciicast v2
  file for any asciinema player.

## The manifest (`forge-demo.scaffold-manifest/v1`)

- **Which files:** every file git tracks in the finished app (so `.gitignore`
  applies: no `node_modules`). Left out, and listed under `excluded`:
  dependency lockfiles (`go.sum`, `go.work.sum`, `pnpm-lock.yaml`,
  `kcl.mod.lock`), forge's own state in `.forge/`, and binaries.
- **Lines:** as `wc -l` counts them, plus a last line without a newline.
- **You wrote:** the lines git counts as added by step 2 that are still in the
  file afterwards. That is only proto. The migrations are forge's: it writes
  each table's first migration at scaffold time (after that, the migration is
  yours to evolve).
- **forge wrote:** every other counted line. `regen_lines` is the part in files
  forge rewrites on every `forge generate` (a `.forge/hashes` entry, a
  generated-code header, or protoc/buf output); `machine_lines` is the
  protoc/buf output alone (`gen/`, `frontends/web/src/gen/`).
- **Categories:** the first matching rule in `CATEGORIES` wins, tests and mocks
  first; `project` takes whatever is left. Each entry is
  `[path, lines, your lines, "regen" | "yours"]`.
- **by_step:** files `forge project new` wrote, then what each later command
  added or changed.
- **chain:** six excerpts for the `Order` entity (proto message, migration, ORM
  row, handler, React hooks, CRUD test), found by the anchors in `CHAIN`. If a
  forge change moves an anchor, the run fails instead of committing a wrong
  excerpt; update the anchor.
