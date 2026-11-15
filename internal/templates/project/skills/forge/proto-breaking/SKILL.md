---
name: proto-breaking
description: Protobuf evolution discipline — deprecate first, reserve removed tags, version-rev for hard breaks, gate breaking changes in CI.
emit: both
---

# Proto Breaking Changes

Protobuf is designed for backward-compatible evolution. Used correctly, you can add fields, deprecate fields, add RPCs, and rename APIs without breaking deployed clients. Used carelessly, a single careless field renumber takes down every consumer that hasn't redeployed. This skill covers the discipline that keeps proto evolution clean: what trips the breaking-change detector, the deprecation flow that ships through it without incident, and the rare hard-break flow when deprecation isn't an option.

## The deprecation flow (the 99% path)

When you need to evolve a field, message, or RPC: deprecate first, ship the new shape alongside, remove the deprecated shape in a later release.

### Step 1 — add the new shape; deprecate the old

```proto
message User {
  string id = 1;
  // Deprecated: use full_name. Remove after 2026-08-01.
  string name = 2 [deprecated = true];
  string full_name = 3;
}
```

Non-breaking by construction: clients that read `name` keep working, clients that read `full_name` start working. Servers populate both fields during the deprecation window.

### Step 2 — migrate callers

Update internal callers (handlers, services, frontends) to write `full_name` and read `full_name`. Leave `name` populated server-side during the window — external clients still depend on it.

### Step 3 — remove the deprecated field (later release)

After the deprecation window, remove the field — but **tombstone the tag and field name**:

```proto
message User {
  string id = 1;
  reserved 2;                    // tombstone the tag — never reuse
  reserved "name";               // tombstone the field name — never reuse
  string full_name = 3;
}
```

The `reserved` declarations are what make the removal safe. Without them, a future contributor could renumber tag 2 onto a new field of a different type — and deployed clients that still send tag 2 as a string would silently corrupt the new field. The breaking-change detector flags removal-without-reserve for exactly this reason.

### Worked example: renaming an RPC

```proto
// 1. Deprecate the old, add the new.
service Users {
  // Deprecated: use FetchUser. Remove after 2026-08-01.
  rpc GetUser(GetUserRequest) returns (GetUserResponse) { option deprecated = true; }
  rpc FetchUser(FetchUserRequest) returns (FetchUserResponse) {}
}

// 2. Implement FetchUser; have GetUser delegate to FetchUser server-side.
// 3. Migrate clients/codegen consumers (both methods are now available).
// 4. After the window: remove GetUser, GetUserRequest, GetUserResponse.
//    No `reserved` for RPC names — proto allows reuse of method names
//    once removed.
```

## Hard breaks (rare)

Deprecation isn't always possible:

- **Security.** A field leaking PII; an RPC with a fundamentally unauthenticated path that needs auth.
- **Type confusion.** A field declared `string` that should have been `bytes`; an enum value with the wrong semantics.
- **Fundamental redesign.** The old shape is no longer expressible with new requirements (e.g. a synchronous request/response API becoming event-driven).

For these, **version-rev**:

```proto
// proto/services/users/v1/users.proto
package services.users.v1;

service Users {
  // Frozen — maintenance fixes only.
  rpc GetUser(GetUserRequest) returns (GetUserResponse) {}
}

// proto/services/users/v2/users.proto
package services.users.v2;

service Users {
  rpc GetUser(GetUserRequest) returns (GetUserResponse) {}
}
```

The flow:

1. Copy the v1 proto to a v2 package. Make the breaking changes in v2.
2. Codegen emits clients for both versions.
3. Implement v2 handlers; either delegate v1 to v2 server-side (with the bug intact) or keep v1's logic frozen.
4. Migrate frontends and external callers from v1 to v2.
5. Mark the v1 service `option deprecated = true;`.
6. After the deprecation window, delete the v1 directory entirely. This DOES break v1 clients — the version rev is the advertisement that they need to migrate.

A breaking-change detector is happy with all of this because v2 is a new package; v1 is unchanged until you delete it.

## Local verification with `buf breaking`

`buf breaking` is the standard protobuf breaking-change detector. Run it against your main-branch baseline before pushing:

```bash
# Compare the working tree against the main branch on origin.
buf breaking --against '.git#branch=origin/main'

# Compare against a remote URL (matches what most CI gates do).
buf breaking --against 'https://github.com/<owner>/<repo>.git#branch=main'

# Compare against a specific commit (useful for stacked-PR debugging).
buf breaking --against '.git#ref=<sha>'
```

The standard rule category to enforce is `WIRE_JSON` — it catches anything that breaks existing wire-format clients (added/removed enum values, type-changed fields, removed RPCs, renumbered fields, deleted oneof variants, JSON-name conflicts). Tighten to `WIRE` if you don't ship JSON; loosen to `FILE` only if you don't care about cross-version client compatibility (uncommon).

```yaml
# buf.yaml
version: v2
breaking:
  use:
    - WIRE_JSON
```

A clean exit means the change is non-breaking. A non-empty output lists each rule violation with file/line — fix or deprecate-then-rev each one.

## Rules

- **Deprecate first, remove later.** The deprecation window is typically one minor version unless the team has explicit reason to extend it.
- **Reserve removed tags AND names.** `reserved 2;` and `reserved "name";` together. Otherwise the detector correctly flags the removal as unsafe.
- **Version-rev for hard breaks.** Copy `proto/<svc>/v1/` to `v2/`, make the changes there, deprecate `v1`, delete `v1` after the window.
- **Don't loosen the breaking-change rules below `WIRE_JSON`** unless you don't ship JSON. `FILE` is too loose for any project with an actual client.

<!-- @forge-only:start -->
## Forge CI integration

Forge generates a `.github/workflows/proto-breaking.yml` workflow that runs `buf breaking` against `main` on every PR that touches `proto/**`, `buf.yaml`, or `buf.gen.yaml`. The gate is on by default.

```yaml
# .github/workflows/proto-breaking.yml (forge-generated, Tier-1)
on:
  pull_request:
    branches: [main]
    paths:
      - 'proto/**'
      - 'buf.yaml'
      - 'buf.gen.yaml'

jobs:
  buf-breaking:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - name: Check the base branch has protos
        id: base
        # ...writes has_protos=false, with a ::notice, when origin/<base> has
        # no .proto files yet
      - uses: bufbuild/buf-action@v1
        if: steps.base.outputs.has_protos == 'true'
        with:
          breaking: true
          breaking_against: 'https://github.com/${{ github.repository }}.git#branch=${{ github.base_ref }}'
          lint: false
          format: false
          push: false
          archive: false
          pr_comment: false
```

The workflow is generated when `forge.yaml` has `ci.lint.buf_breaking: true` (the default) AND the project has at least one service. Pushes to main itself are exempt — main is the baseline against which PRs are compared.

It checks breaking changes ONLY. buf-action's defaults would also run lint and format (already gated by ci.yml's `buf lint` and pre-commit's `buf format`), push to the BSR, and post a PR comment — which fails under this workflow's read-only token ("Resource not accessible by integration"). And the PR that introduces a project's first protos passes with a notice: against a base with no protos, buf fails with `Module "path: "proto"" had no .proto files`, which is not a breaking change.

`forge.yaml` toggles whether the workflow is generated, not which rules run — edit `buf.yaml` directly to change the rule set.

## Override flags

For legitimately breaking changes the gate must allow — internal-only tools where you control every client, or a v1-deletion as part of the hard-break flow — bypass `buf breaking` for the single PR:

**Per-PR commit-message escape (recommended for one-offs).** Add this line to the PR's merge commit:

```
[skip-buf-breaking] reason: removing v1 after deprecation window
```

The forge-generated action recognises the marker and emits a warning instead of failing. The marker is auditable in git history and forces the author to write down a reason.

**Per-file ignore in `buf.yaml`.** When a single file legitimately breaks (e.g. an internal-only proto consumed only by your own server process), add it to the ignore list:

```yaml
breaking:
  use:
    - WIRE_JSON
  ignore:
    - proto/internal/heartbeat/v1/heartbeat.proto
```

Use sparingly — every entry on the ignore list is a permanent exemption, not a one-time bypass.

**Disable the workflow entirely (not recommended).** Set `ci.lint.buf_breaking: false` in `forge.yaml` and re-run `forge generate` — the workflow file is removed. Only do this for projects where the protos are an internal implementation detail and there are no external clients (most forge projects DO have external clients, even if it's just their own frontend bundle deployed independently).

`forge ci` does not currently wrap `buf breaking` (the command is short enough that the wrapper has no leverage). Just call `buf` directly.

## When this skill is not enough (forge sub-skills)

- **CI workflow generation and tier boundaries** — see `ci`.
- **Proto file structure, annotations, CRUD conventions** — see `proto`.
- **Splitting a service's protos into multiple files** — see `proto-split`.
- **The forge project upgrade flow** (when forge itself ships a breaking proto-shape change) — see `migration-upgrade`.
<!-- @forge-only:end -->
