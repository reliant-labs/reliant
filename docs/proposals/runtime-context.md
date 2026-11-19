# Runtime context: joining telemetry back to the code and the diff

Status: accepted by the owner (2026-11-18), in implementation.

## Why

Hud (hud.io) sells a "runtime code sensor": an SDK that measures every function
in a service, captures redacted forensics when a request fails or is slow, and
puts that data in front of a coding agent while it writes (MCP, editor
annotations), when a PR is reviewed (blast radius, risk verdict) and after a
deploy (regressions scoped to the code that changed, the PR that caused them).

forge already collects most of the raw data, and it knows something Hud has to
discover at runtime: every component boundary is GENERATED. `contract.go`
produces `middleware_gen.go` (`internal/generator/contract/observed.go`), whose
wrappers route every Service method through `observe.ComponentChain`
(`pkg/observe/component_chain.go`): recovery, a span named `<pkg>.<Method>`,
`<pkg>.{calls,errors,duration}` metrics, and a log line per failure (server
faults at ERROR, so a failure the caller swallows still becomes an sfp1 issue).
What is missing is the join from that data back to the code, the diff, and the
person or agent changing it.

## The pieces

### A. Contracts derived from a marked struct (`//forge:contract` on a struct)

Today a package hand-writes `type Service interface {...}` in `contract.go` and
implements it on an unexported struct. The contract linter already requires
that the implementation export ONLY the contract's methods, so the interface is
by construction the struct's exported method set: writing it twice is pure
ceremony and the most common source of the "contract.go and mock_gen.go are an
atomic edit pair" friction.

New, additive form:

```go
// internal/email/service.go
//forge:contract
type service struct{ deps Deps }

// Send delivers one message.
func (s *service) Send(ctx context.Context, to, subject string) error { ... }

func New(deps Deps) (Service, error) { return &service{deps: deps}, nil }
```

`forge generate` writes `contract_gen.go` (forge-owned): `type Service
interface` holding every exported method of the marked struct (pointer and
value receivers), each method's doc comment copied, plus
`var _ Service = (*service)(nil)`. `mock_gen.go` and `middleware_gen.go` are
generated from it exactly as from a hand-written `contract.go`. Dependency
interfaces stay hand-declared, consumer-side, in `deps.go` (the existing
`// forge:deps` file) or any non-generated file.

- `contract.go` with a hand-written interface remains fully supported. It is
  the right form for multi-implementation (strategy) interfaces, where the
  interface IS the abstraction. Nothing is removed: forge is semver at major 1.
- New scaffolds (`forge scaffold package`) use the marker form.
- `forge fix` offers the migration for a single-implementation package.
- Everything that today keys on the literal file `contract.go` (instrument
  decision, contract-gap lint, component discovery, retire, explain, binaries)
  accepts a marked struct as the same thing.

### B. Richer component boundaries at runtime (pkg/observe + the decorator)

1. Code location, generated as constants: every component span carries
   `code.function.name` (`<import path>.<Iface>.<Method>`), `code.file.path`
   (project-relative file of the implementing method when the generator can
   resolve it, else of the interface method) and `forge.op` (`<pkg>.<Method>`,
   identical to the span name). No line numbers: they churn the generated file
   on every edit. Zero per-call cost beyond attaching precomputed attributes.
2. Slow relative to its own normal: a cheap per-op rolling estimate in-process;
   a call far above its op's own normal (and above an absolute floor) marks its
   span `forge.slow=true` so a tail sampler can keep it (control plane sampler:
   follow-up), and is eligible for forensics.
3. Forensics on failure or slowness, redaction decided at codegen time: on an
   error or slow call, the span gets ONE `forge.forensics` event carrying the
   call's arguments as the generator classified them, rate-limited per op per
   process. Deny by default: numbers, bools, enums and ID-shaped strings
   (parameter or field named `id`/`ids`/`uuid`/`*ID`/`*Id`/`*IDs`/`*_id`/
   `*_uuid`/`*Uuid`/`*UUID`; one rule on both sides, so the browser and the
   server agree on every spelling) are
   recorded by value — except under a secret name, where a string, bytes or
   number of any kind records only `"[len=N]"` (a number's digit count),
   ID-shaped or not. A name is secret when, lowercased with `_` and `-`
   removed, it contains `token`, `secret`, `passw`, `credential`, `session`,
   `cookie`, `apikey`, `privatekey`, `signingkey` or `otp`; or when one of its
   words (split on `_`, `-`, camelCase and letter/digit boundaries), or two
   adjacent words joined, is `pin`, `otp`, `cvv`, `cvc`, `ssn`, `tan`,
   `passcode`, `securitycode`, `mfa` or `totp`, plural or not. So `pin`,
   `userPIN`, `cvv2`, `security_code` and `session_id` are secret, and
   `spinner`, `pinned`, `shipping` and `tank` are not. A secret-named bool or
   enum keeps its value (`mfa_enabled`, `session_state`): it is one of a few
   values the schema itself lists, so it cannot carry anything a person
   typed, and it is usually the state a failure needs explained. A NUMBER
   whose first or last word is `max`, `min`, `limit`, `count`, `total`,
   `size`, `len` or `length`, or whose FIRST word is `num`, is a quantity,
   not a secret, and keeps its value under a secret name (`max_tokens`,
   `token_limit`, `session_count`, `num_sessions`). `num` counts only as the
   first word: last, it reads as "number", so `pin_num`, `ssn_num` and
   `otpNum` are the secret itself and record their digit count, as does
   `otp_counter_id` (last word `id`). An ID-shaped secret name is never a
   quantity, whatever leads it: `max_session_id` and `num_otp_id` record
   their digit count. A string named like a quantity still records its
   length.
   Server (`pkg/observe/forensic_names.go`) and browser
   (`web-runtime/src/rpc-forensics.ts`) are held to one table,
   `pkg/observe/testdata/forensic_secret_names.json`. Times and durations (Go and proto well-known types)
   are user content and record only their type; proto messages are walked
   with protoreflect under the same field rules, and a `debug_redact` field
   is omitted entirely. Encoding of a value that is not recorded:
   `"[len=N]"` for strings and bytes, `"[count=N]"` for slices and maps,
   `"[message]"` below the depth limit, `"[type=<Name>]"` for anything else
   (times, durations, unclassified structs). `// forge:no-capture <param>`
   on a method excludes a parameter; forge.yaml
   `observability.forensics: false` turns the whole feature off. The capture
   closure is built lazily, so a successful fast call pays nothing.

### C. The join: ops, blast radius and release health scoped to changed code

1. `forge obs ops <env>`: every component op and RPC, with calls, error ratio,
   p50/p95/p99, weighted for tail sampling like `obs endpoints`, and with its
   code location. Component ops are internal spans carrying `forge.op`.
2. `forge obs blast-radius [--base <rev>] [<env>]`: maps a diff to the ops it
   changes (a changed method of an instrumented component's implementation, a
   changed `rpc_<name>.go` handler, a changed unexported helper attributed to
   the ops of its package) and attaches each op's production traffic, error
   ratio and latency. JSON first (`forge.obs.blast_radius/v1`): it is the input
   `pr-review` consumes to tier a PR's risk.
3. `pkg/releasehealth` gains op-scoped findings (`changed_op_errors`,
   `changed_op_latency`): the ops the release's commit range changed, judged
   window-before vs window-after. A 3x slowdown in an op that carries 1% of a
   service's traffic no longer hides inside the service p95. Findings name the
   commit (and PR, when the subject carries `(#123)`) that changed the op.
4. `forge obs endpoints` gains a client-perceived column (p95 as the browser
   saw it, from D's spans), joined by `rpc.service` + `rpc.method`.

### D. The browser side (`@reliantlabs/forge-web-runtime`)

Hud covers no browser code. The runtime's Connect interceptor stack records one
client span per RPC, named `<rpc.service>/<rpc.method>`, with `rpc.system`,
`rpc.service`, `rpc.method`, `forge.rpc.client=true` and the Connect error code,
in the same trace as the server span (traceparent is already injected). On a
failed RPC it attaches one redacted forensics record (request message shape
under the same deny-by-default rules, proto `debug_redact` honoured), at most
once per failure signature per page session.

## Attribute contract (shared by B, C and D)

| Attribute                   | Set by                          | Meaning                                                                                       |
| --------------------------- | ------------------------------- | --------------------------------------------------------------------------------------------- |
| `forge.op`                  | B                               | `<pkg>.<Method>`, equal to the component span's name                                          |
| `code.function.name`        | B                               | `<import path>.<Iface>.<Method>`                                                              |
| `code.file.path`            | B                               | project-relative source file of the implementation                                            |
| `forge.slow`                | B                               | bool, the call was far above its op's own normal                                              |
| event `forge.forensics`     | B, D                            | attributes `forensics.arg.<name>` (classified values), `forensics.reason` = `error` or `slow` |
| `forge.rpc.client`          | D                               | bool, a browser-side RPC span                                                                 |
| `rpc.service`, `rpc.method` | D (and otelconnect server side) | the join key between client and server                                                        |

## Not copied from Hud

Hud hooks every function through the language runtime. Go cannot do that
without eBPF uprobes, and forge declares boundaries rather than discovering
them. Generated component boundaries plus continuous pprof profiles (already
linked to spans by labels) are the Go equivalent.

## Follow-ups (outside forge)

- control-plane tail sampler (`deploy/kcl/lib/observability/sampler.k`): keep
  spans with `forge.slow=true`.
- control-plane `pr-review`: feed `forge obs blast-radius --json` into the risk
  tier.
- control-plane `GetReleaseHealth`: adopt the op-scoped findings.
- Frontend blast radius: which routes and RPCs a changed component calls.
