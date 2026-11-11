# Scenario state and generic HTTP destination investigation

Investigated 2026-11-07 on fresh `origin/main`
`22a9dd7a39f57976636ceb2d94accc96f2d551a4`, in an isolated Reliant
worktree. No live saved credential or remote service was used.

## Scenario state: confirmed regression (report 7962d7b6)

The supplied Growth `docs/scenarios` path does not exist in the referenced
worktree. The report reference and full-entry workaround are recorded in
Growth `docs/workflows-fundraising-operating.md`, under “Offline scoped
checks”; the initial-destination finding is under “Credential and model
boundaries (P1 repairs)”.

The current contract remains explicit:

- `internal/workflow/scenario/types.go`: `Scenario.State` pre-populates node
  outputs; `StartAt` may be combined with it.
- `docs/reference/scenario-schema.mdx`: “Partial testing with state” example
  and `state` field description.
- `internal/workflow/scenario/validation.go`: validates state node references.

At the investigated base, the runner did not read `sc.State`.
`runner/backend.go` supplied inputs and `resumeInput(sc)` to
`runtime.DynamicWorkflow`; `resumeInput` passed only the start node.
`runtime/workflow.go` created an empty `NodeOutputStore`. Position-resume
intentionally does not reconstruct prior outputs. Scenario fixtures must
therefore be explicitly seeded; production recovery semantics must not change
as a side effect.

A focused regression runs a real compute node `second` reading
`nodes.first.value`, with `state: {first: {value: supplied}}`. Both entry-point
execution and `start_at: second` fail with:

```
CEL evaluation error: no such key: first
```

The fix needs a runtime hydration seam. Options discussed with the owner:

1. Recommended: a harness-only typed workflow-context seed, consumed when the
   root node-output store is created. The scenario wrapper supplies it; it is
   not a public workflow input, checkpoint, RPC or YAML field. Child workflows
   must not inherit root fixtures. Existing unseeded resume tests stay unchanged.
2. An explicit optional `WorkflowInput` field: clear and replayable, but extends
   the recorded execution contract for a harness-only capability.
3. Defer the fix and retain the reproduction until the seam is approved.

### Implemented repair (owner-approved option 1)

`runtime.WithScenarioState` copies the supplied JSON-shaped fixtures into a
private typed workflow-context value. The runner wraps only fixture-bearing
root invocations; scenarios without fixtures still invoke `DynamicWorkflow`
directly. `DynamicWorkflow` consumes and shadows the seed at entry, then merges
it into its root output store before node evaluation. Inline or child executions
cannot inherit it. No public workflow argument, checkpoint, recovery behavior,
RPC or YAML schema changes; `call_llm` and ambient isolation are untouched.

Regression coverage uses real compute/CEL execution, not mocked compute outputs:

- `state` alone and `state` plus `start_at` expose earlier outputs without
  executing or reporting fixture nodes as completed.
- `fetch_context.approved` true/false drives real conditional routing after a
  `start_at` that skips the graph's original entry.
- Root fixtures remain visible at root but absent from an inline workflow with
  the same node ID.
- Absent fixtures still produce `no such key` on unguarded reads, including
  position-resume; the existing production-resume regression is unchanged.
- Context fixtures are consumed and deeply copied, including nested maps and
  lists, so repeated use cannot mutate the scenario definition.

## HTTP: confirmed unbound initial-destination exposure

### Saved connection schema

`proto/reliant/v1/connection.proto` defines
`CreateApiKeyConnectionRequest` with integration ID, name, credential kind,
write-only secret fields, and manifest-defined non-secret params. For generic
HTTP, API-key fields are `api_key` plus optional `header`; basic auth uses
`username` and `password`. There is no destination binding field.

`internal/integrations/catalog/http/manifest.yaml` explicitly sets
`allow_any_public_host: true`, declares optional API-key/basic authentication,
and declares no `base_url`, `allowed_hosts` or `connection_params`.

`internal/connections/authenticator.go` permits only four API-key header
placements (`bearer`, `x-api-key`, `api-key`, `x-auth-token`) and applies the
secret to the request it receives. This restricts header selection, not host
selection. `internal/integrations/connauth/contract.go` separately enforces
run-owner/integration resolution; that does not bind a generic credential to
an initial destination.

### Runtime enforcement

`internal/integrations/httpaction/httpaction.go` derives `pinnedHost` from the
**rendered initial target**, requires HTTPS when a credential is present, and
checks redirects and pagination against that pin. `checkURL` permits any host
for the generic HTTP manifest; netguard still rejects private, loopback,
link-local and metadata addresses in production. An attacker-controlled
public HTTPS endpoint passes those transport restrictions.

`generic_destination_test.go` uses the built-in `http/request@1` manifest,
a synthetic credential, and a local TLS server behind the test netguard.
The same credential reaches two independently caller-selected initial hosts;
a cross-host redirect is refused before the second request. No real
secret or attacker endpoint is involved.

### Documentation

`docs/integrations/http.mdx` accurately said the credential is sent only to
“the host the request started at”, but did not explicitly warn that the caller
selects that first host. Clarified that saved generic HTTP connections are
unbound initially, redaction does not protect the remote endpoint, and safe
workflows use independently fixed origins or host-constrained named
integrations. Disabling redirects alone is insufficient. The existing generic
HTTP behavior is unchanged.

## Proposed host-binding direction — NOT IMPLEMENTED

Pending owner discussion; this is not a new supported configuration contract.

- Proposed metadata name: `origin_binding` (not an implemented field). Offer
  explicit opt-in immutable HTTPS **origin** binding on the saved connection at
  trusted connection creation (scheme, normalized DNS host, effective port), not from
  workflow inputs or action params. Refuse userinfo, wildcards, ambiguous hosts
  and non-HTTPS bindings; handle case/IDNA/default-port normalization once.
- Persist the binding with the saved credential metadata and enforce it before
  attaching a credential to the initial request, every redirect, and every
  pagination hop. Keep SSRF/DNS/IP guards and secret redaction independently.
- Preserve existing unbound generic connections and workflows. Expose their
  unbound status clearly, offer an explicit reviewed migration to a new bound
  connection, and never infer a trusted origin from first use or silently
  tighten existing `http/request@1` semantics.
- Keep owner/integration checks and delegated-provider restrictions intact.
  Cover native and any hosted execution paths; an HTTP-layer-only check must
  not leave another secret-attachment path unrestricted.
- Changing a bound destination should create/reconfirm authority, not let a
  workflow rewrite its own credential policy. Workflow activation, connection
  reuse, SDK/UI schema and rollback behavior need review before implementation.

Alternative: use curated integration manifests with fixed hosts today. This
requires no new credential architecture, but is heavier for arbitrary private
APIs. A workflow-only allowlist is easy to deploy but is not credential-level
protection when the same caller controls the allowlist or can edit the graph.

## Scoped verification

Passed on 2026-11-07:

```bash
go test -short ./internal/workflow/scenario/runner ./internal/workflow/runtime ./internal/integrations/httpaction
go test -short -count=3 ./internal/workflow/scenario/runner ./internal/workflow/runtime ./internal/integrations/httpaction -run 'TestRunner_(State|NoState)|TestScenarioState|TestGenericHTTPInitialDestinationIsCallerSelected'
go build ./internal/workflow/scenario/runner ./internal/workflow/runtime ./internal/integrations/httpaction
git diff --check
```

No whole-repository gate, merge, release, deployment or live credential test was
run.
