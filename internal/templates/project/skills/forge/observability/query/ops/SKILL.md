---
name: observability/query/ops
description: The code-level view of telemetry — `forge obs ops` ranks every component op (the `<pkg>.<Method>` spans of forge's generated decorators), rpc and endpoint by calls, errors and p50/p95/p99 with its source location; `forge obs blast-radius` maps a diff to the ops it changes, with each op's traffic, for an edit or a PR review; when to reach for which, and how `obs check` judges the ops a release changed.
---

# Ops: the code the telemetry is about

An **op** is one unit the running code names a span after, so source and
telemetry join on the span name alone:

| Kind | Span | Name |
|---|---|---|
| `rpc` | a Connect rpc's server span (SERVER) | `<proto pkg>.<Service>/<Method>`, e.g. `todo.v1.TodoService/CreateTodo` |
| `endpoint` | any other server span: an HTTP route | the span name, e.g. `GET /healthz` |
| `component` | the span a component's generated `middleware_gen.go` decorator opens (INTERNAL) | `<pkg>.<Method>`, e.g. `billing.Charge` |

Newer decorators also stamp `forge.op` (= the span name), `code.function.name`
(`<import path>.<Iface>.<Method>`) and `code.file.path` (the implementation's
project-relative file). Older builds carry the name only; the checkout fills
in the location.

## Which one, when

| Moment | Run |
|---|---|
| Before editing a component or handler | `forge obs ops <env> --kind component --sort p95` or `forge obs blast-radius <env>`: how hot and how fragile the code you are about to touch is |
| Reviewing a PR | `forge obs blast-radius <env> --base <base> --head <sha> --json`: what the diff changes and how much traffic runs through it (control-plane pr-review reads this document) |
| After a deploy | `forge obs check <env>`: its `changed_op_errors` / `changed_op_latency` findings judge each op the release changed (`observability/release-health`, Changed ops) |

## `forge obs ops [env]`

```bash
forge obs ops prod --sort p95 --top 5
forge obs ops prod --kind component --sort errors --since 6h --json
forge obs ops prod --name billing.Charge,todo.v1.TodoService/CreateTodo --since 7d --json
```

| Flag | Meaning |
|---|---|
| `--sort calls\|p95\|errors\|rate` | the ranking (default `calls`); `errors` is the error ratio |
| `--top N` | most ops to return (default 20); `omitted` counts the rest |
| `--kind rpc,endpoint,component` | only these kinds (default all) |
| `--name OP` | only these ops, by span name: repeatable or comma-separated, at most 200 names of at most 200 characters. The query itself is narrowed to them (an `in` on the span name, locally and through the control plane), so it stays cheap over a long window where an unnarrowed one may not. With `--name`, `--top` defaults to every requested op. A name `--kind` leaves out is refused |
| `--since`, `--from/--to`, `--service`, `--filter`, `--explain` | the common telemetry flags (`observability/query`) |

Each op: `service`, `name`, `kind`, `found`, `calls`, `errors`, `error_ratio`,
`rate_per_second`, `p50_ms`/`p95_ms`/`p99_ms` (weighted for tail sampling),
and its code: `code_file_path`, `code_function`, `code_line`, with
`code_source` `telemetry` (the span's `code.file.path`) or `static` (found in
this checkout by the op's name); absent when neither knew. `found` is false
only for an op `--name` asked for that had no spans in the window and scope:
it is still listed, with zero figures and `service` `""`, so "no traffic"
reads apart from "not asked"; `names` echoes the requested ops. (`--filter
span.name=…` is not the way to ask for an op: span.name is not a filterable
attribute.) Each kind's table
is the busiest 200 ops; `--sort` re-ranks within them. Schema
`forge.obs.ops/v1`. `suggested[]` names the top op's slowest and failing
traces (a component op by `--filter span.attr.forge.op=<op>` when its spans
carry it). The hosted MCP's `obs_ops` tool is this document, located from
telemetry only; its `name` input is `--name`.

## `forge obs blast-radius [env]`

```bash
forge obs blast-radius prod                      # this branch's changes, with prod's traffic
forge obs blast-radius prod --base origin/main --head HEAD --json
forge obs blast-radius                           # no env: the static mapping alone
```

| Flag | Meaning |
|---|---|
| `--base REV` | default: the merge-base with `origin/main` |
| `--head REV` | the commits `base..REV`, tree to tree (nothing uncommitted is read). Default: the working tree — the commits `base..HEAD` plus a pseudo-commit of HEAD against the working tree: staged and unstaged changes (the working tree's content wins) and every untracked file `.gitignore` does not exclude |
| `--since D` | the traffic window (default `7d`) |

It reads the diff, maps each changed declaration to the ops it can change, and
adds each op's traffic from `obs ops`. Every op says why (`via`):

| Via | The change is… |
|---|---|
| `method` | the op's implementing method |
| `handler` | the rpc's handler, or a declaration in its `rpc_<name>.go` the handler reaches |
| `helper` | reached from the op's method through the package's call graph (every op that reaches it is named) |
| `package` | in the package but reached by no op (a constructor, Deps, a type): every op of the package |
| `proto` | the rpc's declaration, or a message it takes or returns |

Each op lists the commits that changed it, with the PR from a trailing
`(#123)` in the subject. Every changed file not mapped is listed as
unattributed, with its reason (generated, test file, not Go source, a package
with no ops, comments or imports only), so nothing is dropped silently.

The mapping needs only the checkout: with no env it is the whole answer. A
telemetry read that fails degrades the document to `telemetry.status =
unavailable` with the reason and still exits 0; it never hides the mapping.
Schema `forge.obs.blast_radius/v1`. It reads the repository, so it runs in the
CLI and in the local `forge mcp` (`blast_radius`).
