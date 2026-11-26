---
name: migrations/v1.12.0/tests-capture
description: Test runs are recorded (forge v1.12.0). A new project's Taskfile runs each test lane through `forge test capture`, which runs the command unchanged and exits with its status, and records results and coverage under .forge/tests/runs/ (gitignored) on every `task test`, `task test:integration` and `task test:e2e`; `task test:report` summarises them, and the scaffolded CI uploads them to the control plane with a FORGE_CONTROL_PLANE_TOKEN holding tests:write, never failing without one. An older project's Taskfile.yml and .github/workflows/ci.yml are its own (scaffold-once), so its tests keep running unrecorded, unchanged, until it opts in. `forge project upgrade` rewrites each lane still in forge's old scaffolded shape, adds `test:report` and the CI report step (`forge fix tests-capture`), and names each lane you edited with the edit to make by hand. Opt out with FORGE_TESTS=off (one run) or `tests.upload: never` in forge.yaml. Use when Taskfile.yml has no `test:report` task, or a test lane runs `go test` without `forge test capture`.
version: v1.12.0
automation: fix:tests-capture
detection: forge-detect tests-capture
---

# Test runs, recorded (v1.12.0)

A project forge scaffolds from v1.12.0 records every test run. Each lane of its
Taskfile runs its tests through `forge test capture`:

```yaml
test:
  vars:
    RUN:
      sh: "{{.FORGE}} test begin"
  cmds:
    - "{{.SLOT}} {{.FORGE}} test capture --run \"{{.RUN}}\" --lane unit{{if contains \"-json\" .CLI_ARGS}} --render raw{{end}} -- go test {{.GOTESTRACE}} -count=1 -json -covermode=atomic -coverprofile=\"{{.RUN}}/unit/cover.out\" -timeout 120s {{if .CLI_ARGS}}{{.CLI_ARGS}}{{else}}./...{{end}}"
```

`forge test begin` opens a run, `.forge/tests/runs/<id>/` (gitignored under
`.forge/*`). `forge test capture` runs the command after `--` unchanged and exits
with its exit status, so `task test` passes and fails exactly as before. It prints
`go test -json` as the text `go test` prints (raw when you pass `-json`
yourself), and records the results, the coverage profile, the command and its
exit status in the run. Each Vitest frontend is recorded as lane
`frontend-<dir>`, with coverage when `@vitest/coverage-v8` is installed.

`task test:report` summarises the latest run: results, coverage merged across
lanes, diff coverage against your base. `task test:report -- --upload` also sends
the clean-tree runs to the project's control plane (results, coverage counts and
changed line ranges, never source), where the Tests pages show them with merged
coverage and flaky tests. The scaffolded CI runs it after each test step. With no
credential the upload is one line on stderr and exit 0: it never fails a build.

## What does not change by itself

Taskfile.yml and .github/workflows/ci.yml are the project's own (scaffold-once).
Moving `forge_version` changes neither, and nothing is forced: an older project's
`task test` keeps running its `go test` directly, unrecorded, exactly as it did.

`forge project upgrade --to v1.12.0` (or later) moves the pin and then runs this
playbook's automation, `forge fix tests-capture`. To run it on its own, after the
pin is at v1.12.0 or later (it writes nothing while forge.yaml pins an older
forge, so the Taskfile never calls a forge whose `forge test` lacks what the new
lanes use):

```bash
forge fix tests-capture --dry-run   # what it would change, and what it leaves to you
forge fix tests-capture
git diff
task --list-all && task test && task test:report
```

## What the codemod does

It reads the Taskfile Task would read (Taskfile.yml first). A lane is rewritten
only when its command is exactly the one an older forge scaffolded, compared as
the YAML string, so the single-quoted spelling of earlier releases counts. Every
line it writes is the current template's own line, so the upgraded Taskfile reads
as a new project's does. Comments are left as they are.

| Task | Old command | Becomes |
|---|---|---|
| `test` | `{{.SLOT}} go test {{.GOTESTRACE}} -count=1 -timeout 120s …` | lane `unit` through `forge test capture`, `-json`, `-coverprofile="{{.RUN}}/unit/cover.out"`; a service project's `- task: test:frontend` gets `vars: { RUN: "{{.RUN}}" }`, so the frontends join the same run |
| `test:integration` | `{{.SLOT}} go test {{.GOTESTRACE}} -tags=integration -count=1 -timeout=10m …` (service), `… -count=1 -timeout 600s -tags=integration …` (cli, library) | lane `integration`, with coverage |
| `test:e2e` | `go test -tags=e2e -v -count=1 -timeout=20m …` | lane `e2e`, results only (the code under test runs in the stack, so a profile here would measure nothing) |
| `test:frontend` | `SCRIPT: '{{.SLOT}} $PM test $([ "$PM" != npm ] \|\| printf %s --) --passWithNoTests'` | the RUN and CAPTURE vars, and `LABEL` + the capturing `SCRIPT` it hands `frontends`, whose `==>` line now prints `LABEL` |
| `test:report` | (absent) | added after `test:e2e` (service) or `test:all` (cli, library), else after `test` |

Each rewritten task gets the template's `vars:` block (RUN, `forge test begin`)
above its `cmds:`. The edited Taskfile must parse and decode to exactly the old
one with those tasks replaced by the template's, or nothing is written.

When the Taskfile has `test:report`, it also edits .github/workflows/ci.yml: after
each step whose `run:` is exactly `task test` or `task test:integration` (with an
optional ` GOTESTRACE=`), unless the job already runs `task test:report` after it,
it inserts the step below, comment included. The workflow must decode to the old
one plus those steps.

Re-running it changes nothing.

## What it leaves to you

Each of these is left byte for byte, and the run prints a line naming it with the
edit to make. A recognised old lane keeps the playbook pending until it is made; a
lane you edited does not, since forge cannot tell whether your edit records it.

| Where | Shape | The edit |
|---|---|---|
| Taskfile.yml | a lane whose command you changed (other flags, another runner, a script) | give the task `vars: {RUN: {sh: "{{.FORGE}} test begin"}}` and run its command as `{{.FORGE}} test capture --run "{{.RUN}}" --lane <name> -- <command>`, adding `-json` to a `go test` (and `-covermode=atomic -coverprofile="{{.RUN}}/<name>/cover.out"` for coverage) |
| Taskfile.yml | an old lane, but no top-level `FORGE` / `SLOT` / `GOTESTRACE` var (a Taskfile scaffolded before v1.0.0) | add those vars as a new project's Taskfile declares them (`forge project new` a scratch project and copy them), then re-run `forge fix tests-capture` |
| Taskfile.yml | an old lane that already has its own task-level `vars:`, flow style, or a different indentation | the edit in the first row, by hand |
| Taskfile.yml | a `test:frontend` older than `$PM` (a hard-coded `npm test`, or an inline loop) | leave it unrecorded, or move to the current `frontends` task and `test:frontend` from a new project's Taskfile |
| Taskfile.yml | flow-style `tasks:`, invalid YAML | the edits above, by hand |
| frontends | coverage | add `"@vitest/coverage-v8"` to each Vitest frontend's devDependencies, at the range of its `vitest` (the scaffold declares it); without it the frontend lane records results only |
| ci.yml | a test step that runs anything but `task test` / `task test:integration` | add the step below after it |
| .gitignore | a `.gitignore` that does not ignore `.forge/` | add `.forge/*`, as every scaffold has it |

## The CI step, and the token

The scaffolded `test` and `integration-test` jobs carry this after their test
step; add it by hand where the codemod could not:

```yaml
      - name: Report test results
        if: ${{ !cancelled() }}
        env:
          FORGE_CONTROL_PLANE_TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}
        run: task test:report -- --upload || echo "forge test report exited $? (the test step above is the verdict)"
```

The upload needs a repository secret, `FORGE_CONTROL_PLANE_TOKEN`, holding a token
with `tests:write`. A person signed in to the web app mints it under Settings → API
tokens (Reliant cloud: https://forge.reliantlabs.io/settings/tokens), then stores it:

```bash
gh secret set FORGE_CONTROL_PLANE_TOKEN
```

Without it (no secret yet, or a fork's pull request) the step prints one line on
stderr and the job's verdict is the test step's, as it was before.

## Opting out

- **One run:** `FORGE_TESTS=off task test` runs the tests and records nothing.
- **Never upload:** `tests.upload: never` in forge.yaml; runs are still recorded
  locally and `task test:report` still summarises them.
- **Not at all:** keep your old lanes. Don't run the codemod, or restore the old
  lines from `git diff` after it; nothing in forge requires the new shape.

More: `forge skill load testing` (the test lanes), `forge skill load
migration-upgrade` (how playbooks and codemods run).
