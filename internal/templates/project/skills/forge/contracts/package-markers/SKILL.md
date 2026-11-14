---
name: contracts/package-markers
description: The package-level contract markers — //forge:exclude-contract (no contract, no mock) and //forge:not-a-component (a contract package the server does not compose) — when each is legitimate, what forge refuses, and how gofmt places //forge: directives.
---

# Package markers

The full rules for the package-level contract markers. `contracts` has the one-table summary of which marker to pick.

## Packages forge should not manage: `//forge:exclude-contract: <why>`

There is ONE header directive for "forge does not manage this package". It takes a **reason**, on the same line. Put it in the package's `doc.go`:

```go
// doc.go

// Package algos is a strategy registry.
//
//forge:exclude-contract: strategy registry — one constructor per algorithm, no single New(Deps) to bind
package algos
```

```go
// algos.go
package algos

type Strategy interface {
    Name() string
    Run(ctx context.Context, input []float64) (float64, error)
}

var registry = map[string]Strategy{}

func Register(s Strategy) { registry[s.Name()] = s }
```

It is the per-package equivalent of a `contracts.exclude` entry in `forge.yaml`: the package is skipped by the canonical-shape check, by bootstrap wiring, and by mock generation together. The trade is real — **no contract means no generated mock, no observability decorator and no seam** — so the marker costs something at lint time:

- **A bare marker is a lint error** (`forge-exclude-contract-reason`). Write why, the same way a suppressed error-severity rule needs a reason. Codegen still honours a bare marker, so a missing reason never breaks `forge generate`; it fails `forge lint`.
- **The reason follows a colon, and only a colon.** `//forge:exclude-contract — why` (an em-dash, a hyphen, a bare space) is NOT the directive: the package is not excluded. `forge lint` reports it (`forge-go-markers`, with file:line and the expected form), and so does the contract finding it leaves behind. The same lint fails on any `//forge:<name>` forge does not read — a typo is a silent no-op otherwise. `forge project annotations --kind go` lists every Go-source marker and its form.
- **The marker is refused outright on two shapes, whatever the reason says**:
  - `forge-exclude-contract-outbound-io`: the package calls an outbound-I/O entry point — an HTTP client, `database/sql`/pgx, a controller-runtime or client-go client, a gRPC/Connect client, NATS, Redis, an OCI registry client, or a cloud SDK. (Naming those types in a struct is fine; *calling* them is the signal. Local file I/O is not a signal.) **Fix:** make it an adapter — `contract.go` with `// forge:outbound-io` and a narrow `Service`, and `// forge:constructor` on `New(Deps)` (see `adapter`).
  - `forge-exclude-contract-multi-impl`: the package declares an exported interface with two or more implementations in the module, at least one of them in the package itself, and does not consume it. An interface with interchangeable implementations IS the contract. **Fix:** move it to `contract.go` (mark it `//forge:contract` if it is not named `Service`). A seam the package declares because it NEEDS it — the type of one of its struct fields or parameters, on something that neither implements nor returns it — is not refused, even with an in-package default: declare interfaces where they are consumed.

**The only legitimate reasons**, and they are few:

| Reason | Example |
|---|---|
| Pure functions / data vocabulary: no I/O, time, randomness or state | string-case helpers, a finding/severity enum, a strict decode+validate of a spec |
| Test-support code (its non-test source imports `testing`) | an `httptest` fake of a vendor, a policy-enforcing test server |
| API / CRD type packages | `api/v1alpha1` with deepcopy and `Validate()` |
| A strategy registry (an exported `Register…(I)` taking the interface) | pluggable algorithms, each with its own constructor |
| Analyzer / embed-only packages whose shape a framework dictates | a `go/analysis` sub-package, an `//go:embed` wrapper |

Packages whose kind forge already exempts structurally — the `internal/app` composition seam, supervised workers under `internal/workers/`, controller-runtime reconciler packages (they have `SetupWithManager`) — are not refused for doing I/O: their shape is a forge runtime interface, not a `Service`.

A dial or HTTP call whose target is **statically loopback** is not outbound I/O: a constant `"127.0.0.1:8080"`, `"[::1]:80"`, `"localhost:3000"` or `"http://localhost:3000/health"`, or `net.JoinHostPort("127.0.0.1", port)` with a constant host and any port. It cannot leave the machine. If the host comes from a variable, the gate cannot tell, so it still refuses. When the call really only reaches loopback, write the host as a constant.

A genuinely-justified exception to a refusal (a conversion that is its own project) uses the ordinary suppression mechanism in the marker's comment block. That keeps it visible, reasoned, and reported by `--show-suppressed`:

```go
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: 40-method DB layer; per-aggregate split tracked in #123
//forge:exclude-contract: hand-written data layer, split pending
package db
```

A marker that draws both refusals needs one allowance per rule, stacked. Each covers the marker.

### One declaration per package, in `doc.go`

A package directive is a property of the **package**, like its package clause: it applies to the whole package from whichever file declares it. Declare it once, in `doc.go`, with its allowances in the same comment block — a file that holds the package's code can move to another package and take the declaration with it, and `doc.go` never does.

A package may repeat a marker only **identically**: the same reason, and the same allowances in its comment block. Two that disagree — different reasons, a bare marker beside a reasoned one, or an allowance on one and not the other — are refused (`forge-exclude-contract-conflict`, `forge-not-a-component-conflict`), naming both `file:line`s; the gate's other checks wait until the package gives one answer. The fix is to keep one declaration, in `doc.go`, and delete the rest.

`forge lint --show-suppressed` lists every suppression in the project with its file:line, what it covers and its stated reason: each `//forge:exclude-contract` marker, each allowance like the one above, every other `forge:lint-disable*` and `//nolint:` directive, `forge.yaml`'s `lint.rules` downgrades and `contracts.exclude` paths, and the migration (`forge:allow-destructive`, `forge:allow-unsafe-not-null`) and `forge:optional-checked` pragmas. It is read-only and lints nothing; add `--json` for a machine-readable list, or `--scope <path>` for one slice.

## Packages the server does not have: `//forge:not-a-component: <why>`

By default every `internal/<pkg>/contract.go` package is a **server component**: `internal/app/compose.go` constructs it, and `forge generate` must resolve every `Deps` field from another component or from `*Infra` (`internal/app/providers.go`), or it fails with `<Pkg>.Deps.<Field> (<type>) has no provider`.

Constructing it means calling `pkg.New(pkg.Deps{…})`, so a composed package needs a `type Deps struct` and a constructor `func New(Deps) T` or `func New(Deps) (T, error)` (any name, with `// forge:constructor`). A composed package without one — an interface catalogue, a constants package that grew a `contract.go`, a `New` that takes something other than `Deps` — fails `forge generate` before `compose.go` is written, naming the package and what is wrong with it. Give it the constructor, or mark it with whichever marker below is true.

That is wrong for a package only the CLI, a daemon or another binary uses — a browser login flow, an on-disk credential store. Its `Deps` (a credentials directory, an `OpenURL func(string) error`, an HTTP doer) are things the server neither has nor should have, and putting them on the server's `Infra` to satisfy the generator is the wrong design. Mark the package instead, with who builds it:

```go
// Package cliauth signs a CLI user in through the browser.
//
//forge:not-a-component: CLI-only — built by `reliant login`
package cliauth
```

What it keeps and what it drops:

| | not-a-component package |
|---|---|
| `contract.go` + contract lint (`forge lint --contract`, `forgeconv-*`, including `forgeconv-deps-are-interfaces`) | kept — it is still a contract package |
| `mock_gen.go` (one mock per interface in `contract.go`) | kept |
| `middleware_gen.go` decorator | kept when `New` carries `// forge:constructor`; whoever constructs it wraps it: `pkg.New<Iface>WithForgeMiddleware(pkg.New(deps))` |
| a `compose.go` field and construction, an `Infra` field, `Deps` resolution | **none** |
| `helpers_gen_test.go` (`NewTest<Pkg>`) | none — there is no forge-built instance for it to stand in for; test against `New(Deps{…})` with the mocks |
| the decorator wrap at the construction site | yours — forge generates `middleware_gen.go`, but whoever constructs the package decides whether to call it |
| `forge project audit` / `forge project graph` | listed, with `composition: not-a-component` and the reason |

The three package markers answer different questions — pick by what is true:

| Marker | The server… | Mock | Consumers' `Deps` resolve to |
|---|---|---|---|
| *(none)* | constructs it in `compose.go` | yes | the component |
| `//forge:external-component` | has it, built by hand in `providers.go` | yes | the `Infra` field you put it on |
| `//forge:not-a-component: <why>` | does not have it | yes | nothing — a required consumer fails generate |
| `//forge:exclude-contract: <why>` | — no contract at all | **no** | — |

The marker is checked from both sides, so it cannot quietly go stale:

- **A server component that requires it by type fails `forge generate`**, naming the marker and its reason. The way out is whichever is true: delete the marker (the server needs it and forge can build it), switch to `//forge:external-component` (the server needs it and you build it), or remove the `Deps` field (`//forge:optional-dep` if the consumer copes with nil — an optional consumer is allowed, it just gets nil).
- **Declare it once, in `doc.go`.** Repetitions must be identical (see "One declaration per package" above; `forge-not-a-component-conflict`).
- **`forge lint` refuses a bare marker** (`forge-not-a-component-reason`) and **a marker where composition does not read it** (`forge-not-a-component-scope`): on a service handler, worker or operator (server components by definition), beside `//forge:external-component` (a contradiction), or on a package without `contract.go`, outside `internal/`, or already `//forge:exclude-contract` (no effect). A contract package *nested below* a handler is an ordinary internal package and may carry it.

**When generate suggests it.** If a package's `Deps` have no provider and no service, worker or operator reaches the package through a chain of `Deps`, the missing-provider error adds the marker as a second remedy. It is a hint, not a decision: a server package whose consumer you have not written yet looks the same, and for that one the answer is to write the consumer (or wire `Infra`).

**Marking a package that is already composed.** `compose.go` is yours, and forge never deletes an owned construction: when you mark a package that `compose.go` already builds, delete its import, its `Components` field and its construction block yourself, along with any `Infra` field you added only to feed it.

## Spelling: `//forge:` or `// forge:` — and what gofmt does to it

Both spellings are accepted by every forge directive and marker. **Prefer `//forge:` (no space).** It is Go's directive syntax, the same family as `//go:build` and `//nolint:`. godoc hides it from rendered documentation, and gofmt places it deterministically.

What gofmt does with it: it **moves every `//forge:` line to the end of the doc comment**, after any prose, and separates it from the prose with a blank `//`. It keeps the directives' relative order. A spaced `// forge:` line is prose to gofmt and stays where you wrote it. So the placement you see after saving is gofmt's, and it is stable.

Forge reads directives in a way that survives this:

- Markers (`forge:exclude-contract`, `forge:constructor`, `forge:contract`, …) are found anywhere in the comment, so a move does not matter.
- `forge:lint-disable-next-line` covers **the next line of code, plus the comment lines between it and that code**. After gofmt moves a directive to the end of a doc comment it still covers the declaration the comment documents, and it still covers a marker further down the same block. It never crosses a blank line or a line of code.
- The exclude-contract gate accepts an allowance anywhere in the marker's comment block. Mixing spellings (a spaced marker with an unspaced allowance) makes gofmt put the allowance *below* the marker, and that still applies.
