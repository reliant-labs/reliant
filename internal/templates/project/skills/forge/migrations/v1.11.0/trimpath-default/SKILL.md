---
name: migrations/v1.11.0/trimpath-default
description: Go builds use -trimpath because the project declares it (forge v1.11.0), so every worktree and scratch copy of a module shares Go build-cache entries instead of compiling and testing everything again under its own path. Scaffolded GoBuild literals carry `flags = ["-trimpath"]`, and forge now passes GoBuild.flags to every go build and go run it execs from that build (forge build, the host go-run runner, air via $FORGE_GO_BUILD_FLAGS, BuildOnly variants), so a project that already set GoBuild.flags sees them in its dev loop too. forge never adds -trimpath itself. An older project declares none of it, and deploy/kcl, .air.toml and Taskfile.yml are its own (scaffold-once). `forge project upgrade` adds "-trimpath" to each GoBuild's flags, $FORGE_GO_BUILD_FLAGS to .air.toml's build commands, and the Taskfile env GOFLAGS entry (`forge fix trimpath-default`), and names each shape it cannot rewrite with the edit to make by hand. The opt-out is removing -trimpath from GoBuild.flags; debug builds stay untrimmed. Also covers runtime.Caller code, which `forge lint` rule forge-runtime-caller-path names (warns in v1.11.0, gates from v1.12.0). Use when a GoBuild under deploy/kcl has no -trimpath in its flags, or .air.toml or Taskfile.yml lacks forge's trimpath wiring.
version: v1.11.0
automation: fix:trimpath-default
detection: forge-detect trimpath-default
---

# -trimpath, declared (v1.11.0)

Go hashes each package's absolute directory into its build-cache key unless it
builds with `-trimpath`. So two worktrees of one commit, or the scratch copy
`forge ci verify-generated` regenerates in, share nothing: each compiles the
whole module and re-runs every test as if the other had never run. Measured on a
forge project: a second worktree of one commit compiled 267 packages and found 0
of 73 test packages cached without `-trimpath`; with it, 0 and 58. On a machine
where agents work in many worktrees at once, those path-keyed copies are also
what fills the Go cache's disk.

From v1.11.0 a forge project builds with `-trimpath` because it says so, in the
place a person reads the build: its KCL. forge adds `-trimpath` to nothing on its
own. A built binary's stack traces then name module paths
(`github.com/you/app/internal/x/file.go:12`) instead of
`/home/you/src/app/internal/x/file.go:12`, which is also what makes a release
reproducible from any checkout.

## What changed in forge

- **Scaffolded builds declare it.** A new project's workloads get
  `forge.GoBuild {cmd = "./cmd/app", output_name = "app", flags = ["-trimpath"]}`.
- **GoBuild.flags reach every Go command forge runs from that build.** Before
  v1.11.0 only `forge build` passed them. Now the host go-run runner's argv, air
  (through `$FORGE_GO_BUILD_FLAGS`, which the scaffolded `.air.toml` expands) and
  BuildOnly variants get them too. So the command forge execs visibly carries
  `-trimpath`, and **a project that already set GoBuild.flags (`-race`, `-mod=…`)
  now sees those flags in its dev loop too**, not only in `forge build`. Check
  that they belong there.
- **Debug builds stay untrimmed.** `forge build --debug`, `forge debug`, the
  delve runner and `.air-debug.toml` put `-trimpath=false` after the declared
  flags. On a command line the last value wins, so breakpoints by `file:line`
  resolve against your checkout.

## What does not change by itself

Everything that declares it is the project's own and scaffold-once:
`deploy/kcl/**`, `.air.toml` and `Taskfile.yml`. Moving `forge_version` changes
none of them, and a GoBuild with no `-trimpath` in its flags keeps building with
paths.

`forge project upgrade --to v1.11.0` (or later) moves the pin and then runs this
playbook's automation, `forge fix trimpath-default`. To run it on its own:

```bash
forge fix trimpath-default --dry-run   # what it would change, and what it leaves to you
forge fix trimpath-default
git diff
forge env render dev && task --list-all && task test:short
```

It makes three edits, each of which changes nothing else in its file and is
checked before it is written. Re-running it changes nothing.

### 1. GoBuild.flags in deploy/kcl

Every `.k` file under `deploy/kcl/` except generated ones (`*_gen.k`,
`config_gen/`, a `Code generated … DO NOT EDIT` header). Each GoBuild literal
spelled through an alias of forge's module (`forge.GoBuild {…}`, or
`fw.GoBuild {…}` after `import forge.workloads as fw`) is read with KCL's own
parser:

- **no `flags`**: a one-line literal gets `, flags = ["-trimpath"]` before its
  closing brace; a multi-line literal gets a `flags = ["-trimpath"]` line at its
  attribute indentation;
- **`flags` a list of plain strings**: `"-trimpath"` goes in as its first
  element;
- **already says trimpath** (`-trimpath=false` included): left alone.

The edited file must parse again and hold `"-trimpath"` first in each edited
literal's flags, or it is not written.

### 2. $FORGE_GO_BUILD_FLAGS in .air.toml

forge hands air the GoBuild's flags in `$FORGE_GO_BUILD_FLAGS`, and only a build
command that expands it uses them. In `.air.toml`, and in every `air_config` an
`OnHost` runtime under `deploy/kcl` names:

- `[build] cmd` gets `$FORGE_GO_BUILD_FLAGS ` right after `go build `, as the
  current template spells it;
- `[build.windows] cmd`, when it is byte for byte the PowerShell form an older
  forge scaffolded, is replaced by the current one, which splits the variable
  into arguments (`$f = @("$env:FORGE_GO_BUILD_FLAGS" -split " " | Where-Object { $_ }); go build @f …`).

A debug config (`.air-debug.toml`) is never touched, and neither is a command
that already says trimpath.

### 3. The Taskfile env

A project scaffolded from v1.0.13 on has this at the top of Taskfile.yml, so
`task test`, `task test:short` and the CI lanes that run through Task share cache
entries across worktrees:

```yaml
env:
  # GOFLAGS gets -trimpath, merged into whatever GOFLAGS you already use (from
  # the environment, `go env -w` or the toolchain's go.env). Without it Go
  # hashes each package's ABSOLUTE directory into its build-cache key, so every
  # worktree and scratch copy of this module (forge ci verify-generated
  # regenerates in a fresh one every run) compiles the whole module again and
  # re-runs every test instead of reusing what a sibling already built.
  # Measured: a second worktree of one commit compiled 267 packages and cached
  # 0 of 73 test packages without it; 0 and 58 with it. A GOFLAGS already in
  # your environment wins over this block (Task never overrides one), and
  # -trimpath=false in it, or in `go env -w GOFLAGS`, opts out.
  GOFLAGS:
    sh: 'f="$(go env GOFLAGS 2>/dev/null)"; case " $f " in *" -trimpath"*|*" --trimpath"*) echo "$f" ;; *) echo "${f:+$f }-trimpath" ;; esac'
```

The codemod inserts that entry. If there is a top-level `env:` map, it becomes
its first key. Otherwise a new `env:` block goes before the first of `vars:`
and `tasks:`, or after `version:`. It reads the Taskfile Task would read
(Taskfile.yml first, then Taskfile.yaml and the `.dist` variants).

### What it leaves to you

Each of these is left byte for byte, and the run prints a line naming the file
and the edit to make by hand. The playbook stays pending until it is made.

| Where | Shape | The edit |
|---|---|---|
| deploy/kcl | `flags = _shared` or any expression | put `"-trimpath"` in the list it evaluates to |
| deploy/kcl | a flags list with a non-string element | add `"-trimpath"` as its first element |
| deploy/kcl | a `**` spread, an if-entry, arguments | add `flags = ["-trimpath"]` where the literal's attributes come from |
| deploy/kcl | `schema X(forge.GoBuild):` | give the schema `flags: [str] = ["-trimpath"]` |
| deploy/kcl | a file KCL cannot parse | fix it, then re-run |
| .air.toml | a `[build.windows] cmd` you edited | split `$env:FORGE_GO_BUILD_FLAGS` into arguments and pass them to `go build`, as above |
| .air.toml | a `[build] cmd` whose `go build` forge cannot find | pass `$FORGE_GO_BUILD_FLAGS` to that `go build` |
| Taskfile.yml | GOFLAGS already set in `env:`, without -trimpath | append `-trimpath` to the flags it yields, or use forge's entry above |
| Taskfile.yml | GOFLAGS from a `sh:` whose code does not name -trimpath (neither the command nor, outside comments, the project script it runs) | make sure its output includes `-trimpath`; forge cannot read a command's output without running it, so the step stays listed until that code names the flag (forge's entry above does) |
| Taskfile.yml | flow style, an alias, invalid YAML | add forge's entry to the top-level `env:` |

## Opting out

**Remove `-trimpath` from a GoBuild's `flags`.** That is the opt-out, and it is
per build. GOFLAGS cannot override a declared flag: Go reads GOFLAGS first and
the command line after, and the later value wins, so with `-trimpath` in the
build's flags a `GOFLAGS=-trimpath=false` changes nothing for that build. To keep
paths while leaving it declared, put `"-trimpath=false"` after it in the same
list. Debug builds need neither: they already end with `-trimpath=false`.

For Task and raw `go` commands, which read GOFLAGS and no KCL, `-trimpath=false`
in GOFLAGS (the environment, or `go env -w`) does opt out; the Taskfile entry
passes it through.

## `go build` outside forge and Task

A `go build ./...` or `go test ./...` you type yourself reads neither the KCL nor
the Taskfile. To give those the same cache sharing, set it once per machine:

```bash
go env -w GOFLAGS=-trimpath     # replaces any GOFLAGS you set with go env -w before; merge by hand if so
```

## Code to check: files found from runtime.Caller

Under `-trimpath`, `runtime.Caller` and a panic's stack name a file by its
module path, not a path on disk. Code that turns that name into a directory,
usually a test helper that finds `testdata/` or the repository root with
`_, file, _, _ := runtime.Caller(0); filepath.Dir(file)`, then reads a path
that does not exist. It fails only where `-trimpath` is on (in forge's dev
loop, in Task, in CI).

`forge lint` names each such site with rule `forge-runtime-caller-path`. It
warns in v1.11.0 and gates from v1.12.0, so fix them before the next bump (or set
`lint.rules: {forge-runtime-caller-path: error}` in forge.yaml to gate now).
None of the fixes needs a source path:

- **A test's own fixtures**: `go test` runs each package's tests in that
  package's directory, so a relative path is already right:
  `os.ReadFile("testdata/x.json")`, or `filepath.Abs("testdata/x")` when you need
  it absolute.
- **The module root**: walk up from the working directory to the first
  `go.mod`:

  ```go
  func moduleRoot(t *testing.T) string {
  	t.Helper()
  	dir, err := os.Getwd()
  	if err != nil {
  		t.Fatal(err)
  	}
  	for {
  		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
  			return dir
  		}
  		parent := filepath.Dir(dir)
  		if parent == dir {
  			t.Fatal("no go.mod above the test's directory")
  		}
  		dir = parent
  	}
  }
  ```

- **Production code**: embed what it reads (`//go:embed`), or take the path from
  configuration. A binary must not depend on where its source was checked out.

More: `forge skill load migration-upgrade` (how playbooks and codemods run),
`forge skill load testing`.
