# REL1715 frozen capability union

## Inputs and scope

- Main parent: `a928b594f5339d3f42f3a9e42f7230b9a30da21b` (current `origin/main` when this isolated worktree was created).
- Capability parent: `ef57d9add8380f155a82bf1654b4513612f1b16b` (existing PR #1715).
- Ordinary merge, not duplicated cherry-picks. No separate harness-agent fixtures included.
- Worktree: `rel1715-frozen-union`; branch: `worktree/rel1715-frozen-union`.
- No app activation, queueing, merge gate, merge, or release is authorized by this milestone. Independent final union review remains required.

## Resolution inventory

The only overlap/conflict was `gen/forge_descriptor.json`. It was resolved by canonical generation, not hand editing. There were **no owned-file conflict resolutions**. In particular, main's owner-pinner/auth/providers and Chat A changes were not rewritten. The source's namespace continuity changes apply without overlaps. Main had no Workflow field 20 or Node field 29, so source feature tags remain unchanged; no main fields or reservations were renumbered.

Before freezing, byte comparison verified all 38 source-added/changed test files equal the capability parent and all 497 main-changed paths equal the main parent, except the intentionally regenerated descriptor. Thus #1707 bot defaults and #1711 enqueue, as present in the main parent, remain intact.

## Explicit migration rebases

Only these six source-only private migrations were rebased, individually and in dependency order. No `--all-pending` operation was used. Every main migration remains unchanged. The first three source timestamps were below main's migration high-water mark; moving all six in order keeps their dependencies ordered after main.

| Original timestamp | New timestamp | Stem | SHA-256 of unchanged SQL bytes |
|---|---|---|---|
| 20261027050646 | 20261027083800 | pr_author_handoff | b6c086098dde6959ecb5407bb4f3878f39cddc4d6485be692bf71ea0aad35190 |
| 20261027051513 | 20261027083802 | trigger_admission | 0fa879fbf374f94d2315eaffbbc0934358f112e05f7bea69e0a2a48be4efc97e |
| 20261027060848 | 20261027083803 | pr_review_wait | d25a57ea965ad1e844295f36c7229d94481b708e1e3ca2cad4e221bad5ca1137 |
| 20261027064217 | 20261027083805 | pr_review_wait_requests | acc1afa59966414b8fcf2f00af9323b76017cea6a6db093314620a341633e794 |
| 20261027065510 | 20261027083806 | private_review_authority | 18219a7a005c9c87c4c91106f21c0cc827640315283877d1f0291aee510d5885 |
| 20261027070025 | 20261027083808 | pr_review_wait_authority | f13b5435a23dbcd64c135c42c30956882f54de3fe932c3fa751876ab3c62e7f5 |

Each command used `go run github.com/reliant-labs/forge/cmd/forge@v0.1.47-0.20261027025742-2372776cf3d4 db migration rebase db/migrations/<one-original-file>.up.sql`. Byte equality was asserted against `git show ef57d9ad:<original-path>`; SQL bounds and guards were not edited.

## Canonical generation

`forge.yaml` and `go.mod` both pin `v0.1.47-0.20261027025742-2372776cf3d4`. Pinned ES dependencies were installed with `task deps:protoc-gen-es`. Generation passed using:

```sh
env GOWORK=off GOTOOLCHAIN=go1.27.3 GOFLAGS=-trimpath \
  FORGE_NO_MERGE_DRIVER=1 FORGE_NO_GIT_HOOKS=1 \
  go run github.com/reliant-labs/forge/cmd/forge@v0.1.47-0.20261027025742-2372776cf3d4 \
  generate --heal --force --no-cache
```

All five configured hooks ran. Generation's validation succeeded. No generated output was hand edited. The inherited git merge driver referenced a missing older binary; no shared git configuration was modified to work around it.

## Scoped validation

PASS:

- `go test -short` on `internal/prhandoff`, `prhandoffadapter`, `prreviewwait`, `reviewpublisher`, `reviewpublisheradapter`, `runcontinuity`, `triggers`, `triggers/triggerspec`, `integrations/webhook`, `launch`, `temporal`, `workflow/runtime`, `workflow/validation`, `workflow/yaml`, and `workflow/runtime/activities/handlers`.
- Real Postgres private tests: `go test -short ./internal/store -run 'Test(PRHandoff|PRReviewWait|ReviewAuthority|ReviewAdmission|TriggerAdmission)'`. The current `storetest.New` harness runs real isolated databases under `-short`; it does not skip these tests.
- `go test -short ./internal/app -run 'Test.*(Temporal|Namespace)'`.
- `go build ./internal/app ./internal/workflow/runtime ./internal/reviewpublisheradapter`.
- Exact-pin `lint --no-fix --conventions --migration-safety` scoped to the changed feature directories/proto: two gating lanes passed, one advisory reported by quiet mode.

Existing non-short Temporal proofs also PASS with `TEMPORAL_CLI_PATH=/tmp/temporal-cli-go-sdk-1.50.0`: `go test ./internal/reviewpublisheradapter -run 'TestActual' -timeout 6m` (59.528s) and `go test ./internal/workflow/runtime -run 'TestActualDynamicPrivateContinuation|TestContinueAsNew(MissingRegisteredTypeFailsClosed|RegisteredTypeVersionGate)|TestPRReviewWait' -timeout 4m` (3.292s). These exercise the original completion/failed-branch, private dynamic hook, same SQL assignment across CAN, and runtime private continuation/wait tests, not agent 389's new two-round fixtures. No whole-repo gate was run.

## Feature-off boundaries

The production git-publish handoff remains nil by default; no application wiring calls `WithPRHandoff`. The private reviewer workflow is not registered by production app wiring. The internal await node retains its source experimental/hidden metadata. This union adds capabilities, not activation.

## Known blocker — do not queue

Parent/harness investigation identified a semantic blocker in `internal/reviewpublisheradapter/completion.go`: rejecting YAML containing `{{` can reject an actual static `call_llm` graph's declared runtime CEL verdict output even when `HasTemplates=false`. Reviewer `f632` is diagnosing the smallest safe fix. **This union intentionally does not change the verifier without authorization.** Scoped passes do not establish the real same-author two-round repair-loop proof. Harness agent `389343ba` owns those new fixtures separately. Resolve/review that blocker and obtain independent FINAL union review before queueing or representing this feature as complete.
