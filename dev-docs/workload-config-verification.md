# Workload config values: evidence, rollout verification and recovery

This is the verification contract for the Config values UI in Reliant #2381.
It separates client proofs from server proofs and from checks that require a
running environment. The UI does not implement server authorization, artifact
verification, redaction or auditing. Mocked client tests cannot prove those
boundaries, and vendoring an RPC does not install its server implementation.

## Immutable source and review evidence

The client vendors control-plane **8901240e0ff6fbdbc099d0284ff72ca07ee62e9c**
([SOURCE](../proto-vendor/controlplane/SOURCE)), exported from a clean archive
with `task contract:controlplane:sync`. The backend was merged by
[control-plane #2182](https://github.com/reliant-labs/control-plane/pull/2182)
at **4db414a91831d1b63d4598c026e0b2da6b988b23**, not its reviewed branch head
`3473045c68132a2d033f194b09c0f78ea3522e9a`. That actual merge is an ancestor of
the vendored source. The merged scalar-hardening fix is included.

Read-only reproduction, from a credentialed control-plane checkout and this
Reliant checkout respectively (no registry, database or production access):

```bash
# control-plane
git cat-file -e 8901240e0ff6fbdbc099d0284ff72ca07ee62e9c^{commit}
git merge-base --is-ancestor 4db414a91831d1b63d4598c026e0b2da6b988b23 8901240e0ff6fbdbc099d0284ff72ca07ee62e9c
git merge-base --is-ancestor 8901240e0ff6fbdbc099d0284ff72ca07ee62e9c origin/main
# reliant; CONTROL_PLANE_DIR points at the checkout holding those commits
CONTROL_PLANE_DIR=../control-plane task contract:controlplane:check
```

These commands passed during the UI refresh. The contract check reproduces the
export at SOURCE and checks main ancestry, not just a matching branch name.
Reviewers without the private sibling snapshot can use the commit-pinned GitHub
links below with their normal authorized repository access. An inaccessible
private link is **not evidence of a passing server test**; obtain the authorized
snapshot or escalate to the review owner rather than replacing server tests
with client mocks.

### Server proof map at the exact vendored source

All links below pin the same full SOURCE commit, not moving main.

| Boundary / adversarial case | Server source and existing tests | What is and is not established |
|---|---|---|
| Both permissions; live membership and grants | [callerOrgID](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/deploy/helpers.go#L247-L297), [requireConfigRead](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/deploy/config_authorization.go#L12-L35), [config denial tests](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/deploy/workload_config_test.go#L54-L128) | Membership and human grants are read per request; missing/unwired config grant denies. `TestWorkloadConfigEnvironmentToken` also denies either missing scope, other-env and port-bound credentials. Production dependency wiring still needs the live checks below. |
| Requested org and tenant/env/bundle/release binding | [RPC selectors](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/deploy/rpc_get_workload_config.go#L25-L108), [binding/selector/token tests](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/deploy/workload_config_test.go#L54-L208), [requested-org tests](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/auth/org_test.go#L36-L82), [org field guard](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/app/org_scoped_requests_test.go#L37) | Tests refuse wrong org/env/bundle ID/returned release, dual or empty selectors, project mismatch and missing workload. Generic requested-org tests refuse a machine naming another org; the app guard requires the RPC's `org_id`. This is not a deployed non-default-org end-to-end proof. |
| Revoked grants, former members, expiry | [live authority tests](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/accesstoken/authority_test.go#L55-L125), [wildcard re-read tests](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/accesstoken/wildcard_test.go#L79-L136), [token expiry/revocation lookup](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/accesstoken/store.go#L554-L603), [auth adapter](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/accesstoken/authadapter.go#L32-L67), [JWT expiry tests](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/auth/jwt_test.go#L185-L230) | Existing tests cover grant removal and former-member clipping; token lookup rejects revoked/expired credentials. These are shared authority proofs, **not config-RPC-specific expiry or deployed token-wiring tests**. Fresh-request expiry/revocation remains a live acceptance item. |
| Redact before BOTH encodings; malformed scalar bypass regression | [single normalized tree to Objects/YAML](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/bundleview/service.go#L157-L178), [strict identifier/composite-value hardening](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/bundleview/service.go#L318-L365), [TestProjectionRedactsBeforeBothEncodings](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/bundleview/projection_test.go#L35-L123) | Regression checks raw secrets, nested-name/composite env values, credentials, headers and legacy SHA markers are absent from serialized Objects **and** YAML; fixed withheld and safe values survive. This test would fail for the reviewed malformed-value bypass. |
| Immutable artifact and workload/reference boundaries | [configLayer](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/deploy/config_layer.go#L11-L53), [descriptor bytes/size verification](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/deploy/bundle_admission.go#L630-L681), [archive/cluster tests](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/bundleview/projection_test.go#L125-L169) | Config reader requires platform-registry reference/digest agreement, verified manifest, exactly one bounded manifests layer and matching blob descriptor. Tests include tampered manifest, huge layer, unsafe archives, ambiguous workloads, cross-cluster/namespace references. Historical artifact retention/registry connectivity is checked live, not assumed. |
| Synchronous, fail-closed audit; resolved digest | [audit defer](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/deploy/rpc_get_workload_config.go#L59-L74), [response provenance](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/deploy/rpc_get_workload_config.go#L123-L134), [TestWorkloadConfigBindingsAndAudit](https://github.com/reliant-labs/control-plane/blob/8901240e0ff6fbdbc099d0284ff72ca07ee62e9c/internal/handlers/deploy/workload_config_test.go#L54-L128) | Audit failure returns no response; dependency error bytes are not exposed. Successful test asserts one `workload.config.read` event with target IDs/outcome only. Audit is for authorized attempts, including downstream failures; do **not** expect this handler's audit defer for pre-authorization denial. |

### Execution evidence, not an invented CI verdict

The trusted backend [fixed-and-requeued report](https://github.com/reliant-labs/control-plane/pull/2182#issuecomment-6724398388)
records execution of `go build ./...` and
`task test:short -- ./internal/app/... ./internal/handlers/deploy/... ./internal/bundleview/...`
(including `TestOrgScopedRequestsCarryOrgID`) and reports its gate passed.
The subsequent [HIGH accepted review](https://github.com/reliant-labs/control-plane/pull/2182#pullrequestreview-5818825806)
explicitly accepts the scalar fix and both-encoding regression; it **read**, did
not execute, tests. The [carried exact-head review](https://github.com/reliant-labs/control-plane/pull/2182#pullrequestreview-5818836096)
explains the generated-only carry to head `3473045c68132a2d033f194b09c0f78ea3522e9a`.
GitHub records the actual merge at `4db414a91831d1b63d4598c026e0b2da6b988b23`.
The GitHub Actions checks on this backend PR are SKIPPED, not passing test
executions. No independent full-main or production execution is claimed here.

In the UI refresh, 53 scoped Vitest tests (five files), scoped ESLint, frontend
`tsc -b`, `go build ./gen/controlplane/...`, exact contract check and scoped Forge
lint passed at client head `8f8a90e3db30bc89b916f8c00e3f65a5fe3c754d`. Those tests
prove action-only calls, org pin/reset, response validation, safe rendering/export,
withheld comparisons and compatibility—not server enforcement. This evidence
addition changes documentation only; it does not require generation or another
mock test run. No coverage waiver is requested.

## Owner-approved live acceptance (not yet executed)

Before anyone performs these checks, the release owner must explicitly approve a
**non-customer test organization and environment**, the tester identities, two
retained same-env bundles/releases, synthetic canaries and a time window. Record
that approval and the actual IDs in the release/incident record, not credentials
in this document. Do not use a customer's config, paste payloads into PRs, mint
production credentials for review, or imply that this plan itself grants access.
An authorized operator records release digest, timestamps, request IDs, resolved
bundle IDs/digests, audit row IDs and sanitized verdicts, never raw values or tokens.

| Step | Action and required observation | Stop / evidence rule |
|---|---|---|
| Release health | After an approved deploy, run `reliant forge obs check <env> --wait --json` from the deployment project. Record the actual release and `exit_code`. | 0 proves the observability check only; 1 regressed: inspect suggested issues and fix forward; 2 means **not proven good**; 8 means insufficient data, check later. None proves config security by itself. This UI author deploys nothing. |
| Names-only and refusal | Open Config with a tester holding only `deploy:read`. No values RPC on mount/tab change; names remain. Explicit values click must deny without returning either projection/export. | A role label is not a substitute for held grants. Record request/error code, no response content. |
| Positive authorized read | With both grants, explicitly click once in the approved non-default org/env. Verify resolved org request, env/workload/bundle ID, and `bundleDigest` equals the authoritative recorded bundle digest, not merely another UI label. | Authorized read has a synchronous audit row; correlate its row ID/actor/env/bundle/workload/outcome with request ID/time. Missing evidence is unknown, not passed. |
| Both redacted encodings | Synthetic fixtures include plain config, Secret keys/references, credential-shaped values and the malformed mixed-value regression. Inspect Objects and YAML through authorized test tooling. Verify raw canaries AND their SHA-derived secret fingerprints absent in both; fixed withheld remains and safe references/plain values survive. | Never retain an unsafe payload as evidence. On exposure stop immediately and follow incident containment below. Legitimate bundle/image digests are allowed and must not be mistaken for secret fingerprints. |
| Changed authority | Revoke the config grant, remove test membership, and expire/revoke a dedicated test credential using the owner's approved test mechanism. Issue a **fresh** explicit request and refresh/reopen Config; each must deny, keep names where still permitted, and provide no new values. | Prior authorized values cannot be retroactively erased from a user's clipboard/download. Hide/reload existing UI values; verify fresh server refusal. Restore only approved test grants afterward. |
| Foreign selectors | Using only pre-approved synthetic IDs, request a bundle belonging to the other test env/org, wrong release, empty/dual selector. Confirm refusal without foreign projection; never probe customer IDs. | Expect tenant/binding refusal (NotFound/PermissionDenied) or InvalidArgument for invalid selectors. Record codes only. |
| Compare and audit | Explicitly compare the two approved same-env releases. Exactly two independently authorized reads, selected bundle pinned immutably, both digests match records and both authorized attempts have audit row IDs. Withheld fields say `secret value not compared`. Cancel/switch org/env while pending; no stale values appear. | One failed read must not yield a partial comparison. Do not infer secret equality from identical markers. |
| Fail-closed dependency | Only in an owner-approved isolated test deployment, make the test audit sink refuse a write and repeat the authorized click; no projection may return. Verify sanitized failure and audit-system incident evidence. | Never disable/tamper with shared production audit infrastructure to test this. If no isolated injection is available, retain the unit proof and mark live fault injection **not executed**. |
| Compatibility/static | Against an approved older test server or controlled test transport returning Unimplemented, explicit values click explains fallback and leaves names. Static frontend runtime config displays the server's unsupported reason. | Do not downgrade production or fake a supported static resolver. |

The acceptance record is complete only when each applicable row has an observed
verdict and evidence IDs. Unavailable registry/audit access, unsupported fault
injection or insufficient observability is recorded as unknown/not executed and
escalated to the owner; it is never silently marked passed.

## Recovery and exposure containment

For a UI interoperability failure **without unsafe exposure**, stop the rollout
and have the release owner ship a compatible fix-forward that removes/disables
only the Config **values action/viewer**, leaving the existing names-only Config
tab readable. There is no new runtime feature flag promised here. An equivalent
owner-approved gateway containment can refuse `GetWorkloadConfig` while leaving
names-only read RPCs intact. Do not widen grants, weaken server authorization,
redaction, digest verification or auditing to make the UI green. Additive proto
fields remain compatible with older clients; no schema down-migration is needed.

If any raw secret/fingerprint or cross-tenant result is observed, immediately stop
further reads/testing, restrict the values endpoint through the incident owner's
approved containment, and notify security/release owners. Treat already delivered
bytes as exposed: assess recipients/caches/downloads and rotate affected credentials
under incident policy. Disabling UI alone is not containment for an unsafe server.
Preserve sanitized IDs/timestamps; never copy exposed bytes into issue comments.

The author does not roll back production. A coherent rollback of client/server
versions is considered only by the release owner under incident policy, with
compatibility and evidence preservation checked, and only as a safe pre-exposure
recovery choice—not as a way to undo bytes already disclosed. After containment,
fix forward, re-run scoped proofs and repeat the approved live acceptance before
re-enabling values. No production actions were taken to write this document.
