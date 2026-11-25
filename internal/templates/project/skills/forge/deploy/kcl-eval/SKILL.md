---
name: deploy/kcl-eval
description: Read a VALUE out of a project's KCL from a script or a Go test — `forge kcl eval <file> -S <path>`, why a plain `kcl run` cannot resolve `import forge` by design, and the pkg/kcleval Go API.
---

# Reading values out of project KCL

A project's KCL holds two different kinds of thing, and only one of them is a
manifest. `deploy/kcl/<env>/` declares workloads, and `forge env render <env>`
turns those into Kubernetes objects. But a project also keeps plain declarative
constants in library files — the node-pool inputs a build script needs, the
cluster each env's daemon pods land in, a vendored chart's pinned version — and
those are read by scripts and tests, never deployed.

**`forge kcl eval` is THE way anything outside forge reads one of those values.**

```bash
forge kcl eval <file> [-S <path>]... [--format json|yaml|raw] [-D k=v]...
```

## A plain `kcl run` cannot do this, by design

`import forge` resolves from the **forge binary** — there is no kcl.mod
dependency to fetch and no copy of the module on disk (ADR 0003). The binary
rendering a project IS the module version it renders against, which is what lets
a released forge render a project on a fresh CI checkout with no network. `kcl
run` has no way to supply that, so on any project file that imports forge it
simply fails.

That is not a limitation to work around. If you find yourself reconstructing
forge's render setup from outside — making forge materialize its module into a
cache directory, digging the path out, passing `-E forge=<dir>` — stop: that is
this command's job. A reconstruction breaks the moment forge's cache layout,
module name or plugin namespace changes, and it breaks in your CI with an error
about KCL that names nothing leading back to the cause.

Nothing needs `kcl` on PATH. Forge has not needed it since it embedded the
runtime.

## From a shell script

`--format raw` prints a scalar's own text — no quotes, no trailing newline — so
`$(...)` captures exactly the value:

```bash
IMAGE_FAMILY="$(forge kcl eval deploy/kcl/lib/kata_pool.k -S sbd.image_family --format raw)"
DISK_GB="$(forge kcl eval deploy/kcl/lib/kata_pool.k -S sbd.disk_size_gb --format raw)"
CTX="$(forge kcl eval deploy/kcl/lib/placement.k -S placement.prod.context --format raw)"

# A composite goes to a file or through jq as JSON:
forge kcl eval deploy/kcl/lib/platform.k -S chart.values > values.json
```

This replaces `kcl run -S <field> | tr -d "\n'\""`. That pipeline existed
because `kcl run -S` emits YAML, which quotes a scalar only when it would
otherwise parse as something else — so the quoting rule differed per value and
nobody could tell which values needed cleaning. Worse, `tr` deletes every
newline and quote *anywhere* in the value, so a value legitimately containing
one came back silently corrupted.

Four things `kcl eval` does that `kcl run -S` does not:

| | `kcl run -S` | `forge kcl eval -S` |
|---|---|---|
| a selected **list** | one YAML document per element — unusable without knowing the count | one list |
| a **missing** field | empty output, which a caller reads as a legitimately empty value | an error naming the fields the document does have |
| an **integer** | quoted-or-not depending on the value | `200` |
| the **cwd** | matters; scripts `cd deploy/kcl` first | irrelevant |

The cwd one is worth expanding, because it is the step everyone copies without
knowing why. Imports resolve from the file's own kcl.mod package, so a
`cd` was never what made `import lib.foo` work. What the `cd` was load-bearing
for is `file.read`, which resolves against the process cwd — and `kcl eval`
anchors that at the project root for you. Run it from anywhere, including a
subdirectory, and you get the same answer.

Repeat `-S` to pull several fields at once; the result is an object keyed by
selector:

```bash
forge kcl eval deploy/kcl/lib/platform.k -S chart.name -S chart.namespace
```

## From a Go test

Use the library rather than exec'ing anything:

```go
import "github.com/reliant-labs/forge/pkg/kcleval"

func TestPlacementCoversEveryEnv(t *testing.T) {
    var placement map[string]struct {
        ClientID string `json:"client_id"`
        Context  string `json:"context"`
    }
    kcleval.MustSelect(t, "deploy/kcl/lib/placement.k", "placement", &placement)
    // assert against placement
}
```

`MustSelect` fails the test on any error, including a selector that names a
field the document does not have — so a typo cannot decode as a zero value that
an assertion reads as a legitimately empty declaration.

It drives the forge binary the project is pinned to, which is the same binary
`forge env deploy` renders with. So a test and a deploy cannot disagree about
what the KCL says — the property that makes asserting against declared values
worth doing instead of restating them in Go. `Select`/`Eval` take a context and
an `Options` (project dir, explicit binary, `-D` bindings, timeout) when you
need them; `ErrForgeNotFound` is typed so a laptop without forge can skip while
CI, which installs it, fails.

## Scope

- **Read-only.** No port block is claimed, no port store is written, no cluster
  is contacted. It is not guaranteed *pure*, for the same reason `env render` is
  not: KCL evaluates `file.write` itself, so a file that generates a file still
  writes it — reported on stderr.
- **stdout carries only the value.** Every diagnostic goes to stderr, so
  `V="$(forge kcl eval … --format raw)"` is safe.
- **One file, its own values.** To evaluate an *environment* into Kubernetes
  objects, that is `forge env render <env>`.
