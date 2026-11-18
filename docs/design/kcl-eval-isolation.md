# Project-root KCL evaluation

`forge -C <project> kcl eval <file-or-directory>` evaluates in a child of the
same forge binary. Directory sources include every KCL file in the package.
Relative imports still use the source's `kcl.mod`; `file.read("deploy/…")`
uses the requested project root. The caller's cwd never changes.

Environment entity and manifest renders reuse this command, its JSON output,
selectors, and repeated `-D key=value` flags. `forgeExecCommand` retains the
standalone or embedded (`reliant forge`) command route. The child sets `cmd.Dir`
to the absolute project root and disables pin delegation for this internal
re-entry: an old project pin cannot parse a newer binary's internal flags.
The original command's normal version-pin policy still applies.

## Internal operation context

Hidden flags carry only the parent operation's chosen plugin capabilities:

- `--process-rooted`: evaluate directly, after checking cwd equals project root;
  this prevents recursive subprocesses and avoids even a temporary child chdir.
- `--render-env <env>`: identify the port-store scope for an environment render.
- `--render-context read|inspect|claim`: unarmed block allocator, lookup-only
  allocator, or launch-time allocator and persistent port reservation.
- `--render-active`: carry the operation's dev-stack git facts. A plain,
  unactivated render does not acquire a worktree/branch binding accidentally.
- `--materialize`: enable `forge.write_file`, only with `claim`.

These are operation-controlled argv, not settings read from forge.yaml, KCL
options, or environment variables. Ordinary eval/diagnostics default to read-only;
`-D materialize=True` grants nothing. Starting another CLI command clears prior
launch capabilities in an embedder. This is plugin write policy, not a sandbox:
KCL's own `file.write` remains native KCL behavior.

Launch operations retain the same allocator/store/materializer implementation as
before; children re-arm it for the exact project and environment. Cluster
manifest rendering receives the CLI runner through command context, not a global
callback. Library callers without that context retain their embedded renderer.

Stdout contains only the selected value (JSON for environment renders). Progress
and suppressed-write notices stay on stderr. A failed evaluation includes the
child's diagnostics; cancellation kills and waits for the evaluation child.

The real control-plane reproduction is read-only:

```sh
forge -C control-plane obs trace bbb717b547650bb66e288a7a855a97e0 prod --full --logs 0 --json
```

Run from the workspace parent, it must pass the KCL project-relative file reads;
a subsequent remote credential refusal is a separate result, not a render failure.
