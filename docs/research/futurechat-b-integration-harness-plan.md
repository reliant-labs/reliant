# FutureChat B: private real-adapter integration harness

Status: parent approved the three narrow Reliant authority tables plus CP-local
claim boundary on 2026-10-27. Parent assigns production implementers; B owns only
harness fixtures/tests. Reviewed candidate stacks have been assembled privately through repaired wait
`2dcef942` (local head `d26e29ba`); no production registration was added by B.
Infrastructure tests ran as recorded below; no end-to-end success is claimed.
This is private Reliant/control-plane code, not a Forge pack or a new queue framework.

Approved first slice starts from a real SQL-bound publication and exercises actual
reviewer completion -> intent -> CP HTTP/check claim -> durable author inbox.
Existing SQL publication/enrollment adapter tests remain separate evidence, not a
claim that this slice performs a real git push. Repaired wait `2dcef942` and trusted
per-head authority `f41e7372` are approved and assembled; actual coherent flow still
awaits reviewed publisher SQL scope fixes and trusted completion/history adapters.
Do not weaken the literal github.com push
guard: any future validated GitRemote builder/test dependency-injection change
needs a separate plan and approval before editing GitPublish.

## Workspace and frozen inputs

Own workspace: `futurechat-b-integration`, branch `test/futurechat-b-integration`.
Root: `/home/workspace/.reliant/worktrees/reliant-labs/futurechat-b-integration-0b142c20`.
B owns the plan and new `internal/privatepipelineharness` test files in each
checkout. Main checkouts remain untouched. Initial local main was stale
`9f51649e`; B aborted only its own paused assembly and fast-forwarded its own branch
to inspected origin main `a37881fef1e658779ed02cf716ac03372c21f5eb`. This avoided an
unrelated pinner-context conflict; no launcher behavior was hand-resolved.

Verification checkpoints:

* Exact pinned canonical `forge generate --skip-validate` completed on combined
  proto/schema; canonical proto SHA before/after was
  `78e9af602bfd7e5c833c19b9572b284a900893d086732512d7b4bf0b0b529dfd`.
  Generated merge output checkpoint `36f530c8`; this is NOT full build proof.
* `GOWORK=off go test -short -tags integration ./internal/privatepipelineharness`
  passed on assembled stack (0.035s). SQL/Temporal deliberately skip in this run;
  only boundary fixture tests actually execute.
* `RUN_FUTURECHAT_B=1 GOWORK=off go test -tags integration -run
  '^TestFutureChatBInProcessSQL$' -timeout 120s ./internal/privatepipelineharness`
  passed (0.646s): concrete SDK in-process workflow/activity + real migrated SQL.
  This is NOT server history/CAN, author-wait, publication or approval proof.
* No local Temporal CLI is installed; the explicit real-server fixture fails
  its prerequisite rather than downloading or claiming an in-process substitute.
* CP1420/1421 assembled privately as `1c8fd532` / `27419c57`; final combined
  regeneration still due. Independently approved CP SQL journal `3b1a38ff`
  assembled as `4bc13479`.
* Actual cross-process CP boundary test passed (3.779s): migrated CP SQL, sealed
  fixture credential and real credential service, actual Connect handler/gate,
  bridge + SQL claim, fake GitHub only; exact create, duplicate denial and exact
  reconcile, one check POST. This does not attest upstream reviewer authority.
* Frozen harness-only milestones: Reliant `8466d94dfb1b132d6f5f5c11bdb469c7a786c287`
  (new fixture files only), CP `911d0e1e45c5cbb47de2e93b3c262bcedb8298ee`
  (one new test helper only). These are private commits, not pushed/queued.
  CP helper is compiled with `go test -c`, run directly on a random loopback
  listener, gated by a parent-generated secret never printed, stopped by its
  owned stdin. It uses actual CP_TEST_POSTGRES_URL semantics and rejects5434.

Forge friction: configured merge driver pointed at a vanished cache executable;
reported once (`9f4ee505-6623-4ca7-bca6-fb54edfd8dc6`). Verified exact pinned cache
binaries and used per-command driver overrides, not git config edits. Grants lacked
merge attributes, handled by a per-command `/tmp` attributes file. Structural proto
outputs needed canonical generation, not fingerprint conflict suppression.

| Piece | Inspected actual commits | Required input, not just last commit |
|---|---|---|
| REL1709 original-author binding | `bb4b8d13` | one commit; migration, store and opt-in adapters |
| REL1708 publisher authorization | `02941834` | one commit; pure core only |
| REL1710 admission/CAN | `561b7ace` | `b4effbc1`, `7cf8c772`, `cfc82cb8`, `90646038`, `4a101375`, `561b7ace` |
| REL1711 native enqueue | `58db3db8` | `fda5a49c` plus `58db3db8`; last commit alone lacks executor/catalog registration |
| CP1420/1421 checks | `66e3a8b0`, `ccfbcc47` | both, in control-plane checkout |
| Author wait | `f90221ad` inspected READ ONLY | NOT eligible for cherry-pick; wait for approval of repaired exact head |

`f90221ad` includes continuity commits `06e106c5`, `31585b9f`; admission's
`cfc82cb8`, `90646038` contain the same continuity work. Do not apply both copies.
The local wait branch still pointed to `f90221ad` at inspection. The abbreviated
`22e2` was ambiguous locally (tree/blob), not a resolved approved commit.

Before assembly, freeze the repaired head and review approval, verify its diff
against `f90221ad`, and re-check all candidate ancestors against updated main.
None of the four inspected Reliant candidate heads was an ancestor of local main.
No unrelated historical branch merges should be imported merely to obtain a file.

## Actual API/dependency graph

```text
Temporal author execution + real DB chat/root/worktree/repo
  -> handlers.GitPublishActivity.Execute
       .WithLedger(real writeledger)
       .WithPRHandoff(prhandoff.New(...))             [explicit harness opt-in]
  -> real daemon git.publish_prepare / git.publish
  -> prhandoff.Published(PublishInput)
       Authors.Resolve/Checkout/Available
       GitHub.Repository/BranchHead                  [catalog + HTTP runner]
       store.PRHandoffStore.RecordPublication       [SQL]
  -> prhandoffadapter.Receipts.RunAuthenticated
       real ledger-decorated github/pr.create@1
       ActivitySite(actual Temporal activity context)
       prhandoff.Created(CreateInput)
       store.PRHandoffStore.Enroll                   [SQL immutable ownership]
  -> typed pr_review_wait runtime node
       Activities -> Service.Arm/Ready/Read/Consume
       real SQL subscription/inbox + runcontinuity + exact signal target
                                                    [repaired head required]

Authenticated normalized trigger event
  -> triggers intake admission -> real launch -> BindTriggerAdmissionRun
       trigger_events stores scope/key/head/root/run, NOT review authority
  -> MISSING: policy-bound review assignment transaction
  -> actual approved reviewer graph on separate root/execution
  -> MISSING: trusted terminal graph/completion adapter
       must load immutable graph digest and typed approved result
  -> reviewpublisher.Prepare(assignmentID, exact Binding)
       MISSING ReviewSource.LoadReview              [SQL authority]
       MISSING HeadSource.CurrentHead               [real provider adapter]
       MISSING IntentStore.PrepareIntent            [atomic SQL recheck]
  -> MISSING server-only intent publisher
       revalidate assignment/policy/generation/completion + fresh provider head
       POST internal PublishReviewCheck, or ReconcileReviewCheck on ambiguity
  -> CP GitCredentialInternalService handler          [actual Connect HTTP]
       RequireInternalService + UserDirectory
  -> real reviewcheckbridge.Service.Publish/Reconcile
       owner credential -> fresh repository push/ID check
       App install -> checks:write, exact one-repository mint
       MISSING CreateJournal.ClaimCreate              [CP SQL before POST]
  -> fake GitHub check-runs HTTP endpoint, exact head + ReliantReview + external ID

MISSING server-attested outcome adapter
  -> SQL-authorized assignment/completion -> prreviewwait.Service.Publish
       NOT an invented GitHub PR review, NOT a signal carrying verdict
  -> same author root/run wakes, reads/consumes durable inbox
  -> changes: same author repairs and GitPublish refreshes head by CAS, waits again
  -> approval + confirmed check receipt: real catalog github/pr.enqueue@1
       writeledger -> registered githubqueue executor
       fresh GraphQL subject read -> enqueuePullRequest(expectedHeadOid)
       ambiguity -> membership read, never blind mutation retry
```

Important distinctions:

* REL1708 `Receipt` is durable INTENT preparation, not GitHub publication.
* CP `FOUND` is an exact check binding observation, not upstream review authority.
* Existing `TrustedReviewProducer.Deliver` refetches a numeric GitHub PR review,
  authorizes its reviewer and uses its numeric ID as sequence. It cannot consume
  a check-run or a server graph result. Do not fabricate that review to join flows.
* Native enqueue checks freshness and native queue eligibility, not reviewer policy.
  The private coordinator must enforce policy before invoking it.
* `WorkflowStatusActivity.markWorkflowCompleted` already transactionally persists
  terminal status, generic success/failure outcome and terminal-event outbox. Those
  facts alone do not attest `approve` versus `changes` or an approved graph digest.

## Decision needed: durable ownership boundaries

### Options

1. Extend `trigger_events` with policy, reviewer assignment, completion and intent
   columns. Fewer tables, but mixes generic admission bookkeeping with private
   review authority, and couples retention and every policy revision to events.
2. **Recommended:** narrow Reliant policy/assignment/intent persistence referencing
   existing handoff and admission rows, plus a CP-local create-claim receipt table.
   Two databases retain distinct responsibilities; no duplicate generic outbox.
3. Make CP call back to a Reliant claim RPC. Avoids a CP table but adds a new
   authenticated reverse RPC and availability dependency during credentialed
   mutations. A supplied intent ID is not a durable claim and is not proof of review
   authority. This cannot be achieved by passing a callback object across processes.

Parent approved option 2; production implementation owners are being assigned.
Devil's advocate: this introduces tables and distributed reconciliation; it still cannot atomically
fence GitHub with a DB transaction. Mitigate with immutable subject binding,
pre-write revalidation, CP at-most-once create claim, exact observation, no uncertain
success, and native `expectedHeadOid`. Do not claim to eliminate the remote race.

One Go object cannot implement both sides in this deployment: CP and Reliant
have separate processes/databases, and Go `internal` import rules disallow importing
both repos' internal services into a third integration package. Test through HTTP.
A shared SQL handle in a test would conceal the real boundary.

## Minimum proposed schema, not yet implementation

Use migrations created by each repo's migration command; no invented timestamp.
Exact table names below are proposed, not existing API.

**Reliant, canonical private review state:**

1. `review_policies`: owner ID + repository ID key; enabled; monotonic revision;
   immutable effective workflow/dependency digest; canonical check name. Updating
   policy locks/invalidates outstanding authority in the same serialization domain.
   Only a trusted provisioning function/test fixture may write; no model action.
2. `review_assignments`: ID; FK handoff binding; FK admission event; owner/repository/
   PR/full SHA; original author root/run; exact reviewer root/run; policy revision/
   digest; generation; cancelled/superseded flags; structured verdict, terminal
   evidence identity/digest and graph-succeeded/final state. Snapshot identity is
   immutable. Positive generation; strict SHA/digest/verdict constraints; uniqueness
   for binding+generation, and admission/reviewer identity as appropriate. Current
   generation advances under lock on the canonical binding, not arrival order.
   Reviewer must differ from author. Persist result from runtime completion, never
   from an arbitrary `PrepareInput`, webhook payload or test `LoadReview` stub.
3. `review_publication_intents`: ID; unique assignment; immutable binding and
   canonical payload/digest; state prepared/publishing/uncertain/confirmed/blocked;
   CP external ID fixed from assignment/generation/intent identity; check ID/URL
   only after confirmation; reconciliation diagnostics bounded/nonsecret. Repeated
   Prepare returns the same ID; conflicting payload cannot overwrite. This is the
   REL1708 IntentStore and recovery record, not a general-purpose outbox.

`PrepareIntent` serializable transaction loads and rechecks policy row, binding
head/current generation, exact assignment/reviewer/author, completion, cancellation
and payload equality. SQL concurrent uniqueness is authoritative. DB availability
errors cannot turn into disabled, missing or approved state. Terminal callback
must be idempotent for the same evidence and reject conflicting rewrites.

**CP, operation-level idempotency only:**

`review_check_create_claims`: primary key `(app_id, repository_id, external_id)`;
immutable `head_sha`; created timestamp. `ClaimCreate` = atomic insert-before-POST;
any existing key returns false, including another SHA. No TTL/reset/delete-on-error.
Minimum existing interface needs no successful-write column: exact published
receipt/recovery belongs to Reliant intent, CP reconcile observes GitHub. A lost
response keeps the CP claim even after process recreation. CP does not store or
pretend to verify reviewer assignments. No cross-database foreign keys.

## Minimum implementation files and ownership approval request

No files in this section have been written. Parent assigns implementation owners;
publisher's former owner is inactive. Proposed ownership avoids modifying frozen PRs.

| Owner to assign | Exact new/owned file set | Purpose |
|---|---|---|
| Private review persistence owner | Reliant `db/migrations/<generated>_private_review_authority.up.sql`; `internal/store/review_assignment_repo_ext.go`; `internal/store/review_publication_intent_repo_ext.go`; `internal/reviewpublisheradapter/persistence.go` | Provision/assign, trusted completion persistence, `LoadReview`, transactional `PrepareIntent` |
| Publisher/adapter owner | Reliant `internal/reviewpublisheradapter/contract.go`, `github.go`, `completion.go`, `publish.go`, `wait_outcome.go` | Fresh head from real provider runner; terminal evidence; actual CP wire client; reconcile; durable wait outcome; no app provider wiring |
| Runtime seam owner | Reliant `internal/workflow/runtime/activities/handlers/workflow_status.go`; `internal/workflow/runtime/activities/handlers/deps.go`; runtime terminal call site if needed | Optional trusted completion hook with exact execution and approved typed result. Inspect callback ordering/terminal evidence before deciding edits; generic Outcome cannot substitute |
| Admission seam owner | Reliant `internal/triggers/admission.go` and/or `admission_redrive.go` ONLY if existing persisted run binding cannot be consumed explicitly | Atomic assignment from canonical event/execution; prefer new private adapter over general intake changes |
| CP idempotency owner | CP `db/migrations/<generated>_review_check_create_claims.up.sql`; `internal/db/review_check_create_claims.go` | Real SQL `CreateJournal`, no existing release ledger reuse |
| Harness owner (B) | Reliant `internal/reviewharness/{harness_integration_test.go,github_integration_test.go,flow_integration_test.go}`; CP `internal/handlers/git_credential_internal/review_harness_fixture_test.go`; private `scripts/test-futurechat-b.sh` | Cross-process fixture and assertions; integration build tags + explicit opt-in |

Generated projections/grants are generated from inputs in the private checkout,
never patched. No edits to app registration, production config, scopes, permissions,
public tools, live policies, rulesets, gates or releases. If a completion hook would
require production registration, stop for a narrower seam and approval.

Additional concrete harness obstacle: `GitPublishActivity` builds an explicit
`https://github.com/...git` URL; daemon `publishURLAllowed` is package-private and
its default permits only that URL. Existing publish activity tests use a fake
router, not a real push. To claim actual publish, the harness needs a test-only
helper in `internal/toolexec/daemonruntime/futurechat_b_fixture_test.go` dispatching
real commands to a loopback smart-HTTP `git http-backend` remote, with a narrowly
asserted test destination translation. Reuse the existing command registry; do not
fake `Pushed=true`. Keep this helper in a separate test process so its URL policy
override cannot affect parallel tests or production. Approval required if achieving
this instead needs an owned production transport seam.

## Executable harness construction after prerequisites land privately

1. Apply reviewed candidate deltas in the isolated checkouts, then the approved
   repaired wait head. Resolve generated conflicts from combined proto/migrations.
2. Start one private Postgres container by recorded unique name/allocated port,
   or use a supplied private server. Never use port 5434 or real product records.
   Reliant `db.SetupTestDBWithRawDB`/`db.NewTestDatabase` or `storetest.New` provide
   migrated isolated databases; CP `internal/testdb` provides its separate DB.
   Actual current `db/testutil.go` uses Forge pgtest private migrated database
   clones and explicitly ignores `DATABASE_URL`; the override is
   `FORGE_TEST_POSTGRES_URL` (maintenance database `postgres`). Prefer this real
   repo API to a new Docker provisioner. Never override it with port 5434.
   Seed actual project/repo/worktree/chat/workflow, policy, users and encrypted
   fixture credentials. These are setup inputs, not forged result/receipt rows.
3. Start a local Temporal test server using `testsuite.StartDevServer` (existing
   precedent: `internal/triggers/syncer_test.go`, runtime CAN tests), allocated
   address and fresh namespace. Register the real runtime and activities on a
   private queue. SDK `NewTestWorkflowEnvironment` can support deterministic
   activity ordering tests, but not substitute mocked histories/client descriptions
   for real exact-run/CAN proofs in the main integration scenario.
4. Launch CP's gated test-helper process from inside its own module so it can
   construct actual internal handlers/bridge/SQL journal/credential service.
   It publishes only a loopback URL/readiness record and exits on parent teardown.
   Real Connect client calls it. Test auth may inject canonical service claims at
   the auth boundary; wrong/missing/human/machine claims still exercise the real
   gate. Do not label this a JWT cryptographic-auth end-to-end proof.
5. Fake GitHub is the only external service boundary. Loopback httptest serves
   canonical repo/branch/PR reads, PR create, App installation/token/repository
   responses, check-runs/reconcile and GraphQL queue. Transport must reject every
   unknown host/path; original API hostname is asserted before loopback routing.
   Do not weaken production netguard to allow localhost. Use the existing HTTP
   client seam; verify no unguarded fallback can reach live GitHub.
6. Run real author graph publish -> catalog PR create -> wait. Execute separate
   policy-pinned reviewer graph. Its approved terminal route emits structured
   approve/changes/incomplete result; load/persist via real callback. A deterministic
   reviewer input may be supplied at the external LLM boundary, not as an adapter
   returning trusted Completion or by directly inserting terminal result SQL.
7. Prepare/publish/reconcile through real adapters. Deliver server-attested outcome
   through durable wait service; author reads it. Invoke native catalog action only
   for the approved, current, confirmed intent. Fake GitHub asserts successful check
   on that exact SHA before accepting its native queue mutation.

Proposed runnable entrypoint once implemented:

```bash
# from the isolated reliant checkout; helper script provisions BOTH DB fixtures,
# loopback servers and test subprocesses, uses recorded PIDs and removes only
# its own container/anonymous volumes. No gate/merge/release commands.
RUN_FUTURECHAT_B=1 CP_CHECKOUT=../control-plane ./scripts/test-futurechat-b.sh
# script ultimately runs this one Go package, not a repo-wide suite:
RUN_FUTURECHAT_B=1 DATABASE_URL="$PRIVATE_RELIANT_DSN" \
  go test -tags integration -run '^TestFutureChatB' -timeout 4m ./internal/reviewharness
```

These are a concrete target command contract, NOT commands that work today. Helper
binary compilation/startup belongs in the script; test receives loopback endpoints,
private DB DSNs and fixture handles. CP imports stay inside CP, Reliant imports stay
inside Reliant. No test imports both internal trees. Heavy work runs in background.
Opt-in fixture prerequisites fail loudly when requested; default suites skip with a
specific reason. No success-only fallback when a prerequisite is absent.

## Required assertions / controlled failure matrix

| Scenario | Proof from SQL + real executions + recorded provider requests |
|---|---|
| Approve | publication receipt -> immutable binding -> armed wait; separate exact reviewer assignment and terminal evidence; same intent reloaded; CP exact check; exactly one expected-head queue mutation |
| Changes -> repair -> approve | failing check; same author root/chat/worktree wakes, executes repair/push to new SHA; original owner unchanged; new generation/reviewer assignment; no replacement author/start; enqueue new SHA only |
| Stale before Prepare/publish/queue | change provider head at each read barrier; no successful stale intent/check/queue assertion; old generation outcome not consumed as new-head approval |
| Policy/digest/generation drift | change/revoke policy, cancel/supersede assignment or corrupt completion provenance at barrier; no authorization or queue mutation |
| Duplicate completion, concurrent replicas | two real SQL connections; identical intent ID; CP single claim/POST after restart; one consumed sequence; no duplicate repair/start |
| Lost check create response | provider records POST, transport loses response; restart CP + publisher; claim survives; exact reconcile FOUND required; NOT_FOUND/UNKNOWN/outage never frees claim or fabricates success |
| Lost enqueue response | provider queue membership committed, response lost; real executor re-reads membership; repeat invocation observes same entry, no blind mutation replay |
| Arm/read/consume races | outcome before Arm, between Read/Await, after cancellation and head refresh; gate operations with channels, not sleeps; repaired wait must reject stale consumption |
| CAN rollover | same namespace; verified history follows original to successor; exact run signal only; no signal-with-empty-run/latest fallback; stale/terminal original requires explicit attention |
| Outages/timeout | SQL failures, Temporal Describe/history/signal errors, provider timeout/redirect/rate limit; no optimistic approval; timeout does not turn into a new author |
| Forged inputs | wrong reviewer/root, raw text 'approve', missing typed outcome, forged binding/repo/owner, missing enabled policy; no CP write and no queue call |
| CP privilege boundary | real handler rejects noncanonical subject/role; exact repo/App/SHA/name/external ID checks; no credentials in responses/logs |

No fixed sleeps. Use channel/SQL barriers and bounded waits. Persisted state plus
provider counters are the oracle; a mocked `Prepare`, `LoadReview`, `ClaimCreate`,
`GitPublish`, native enqueue result or wait-success response is not acceptable.
For new regressions, use `prove_test` on implementation files with a narrow command;
build failure on baseline is not behavioral proof.

## Minimal conflict forecast (computed from candidate file sets)

* Author vs admission: `db/grants/readonly_gen.sql` only.
* Author vs inspected wait: `.forge/scaffolded.json`, grants,
  `internal/store/pr_handoff_binding_repo_ext.go`, `internal/store/store_gen.go`.
  Binding extension is owned code: preserve receipt CAS and wait ownership checks.
* Admission vs inspected wait: grants, `proto/reliant/v1/workflow_v2.proto`,
  Go/TS workflow proto projections and `gen/forge_descriptor.json`. Combine enum/
  message inputs, then generate; never resolve generated outputs by choosing a side.
* Admission and queue touch different app composition files: queue adds the
  `providers_worker.go` executor import; admission changes `providers.go` and
  `providers_api.go`. No shared path was found between those two candidate file
  sets. Keep namespace/history changes without enabling review production wiring.
* New harness tests/plan are additive; new authority tables/methods should avoid
  editing shared runtime/app files until the exact completion seam is approved.
* Recompute against repaired wait head before assembly. This forecast is not a
  cherry-pick attempt or a claim of textual-conflict-free integration.

## Approved scope and remaining handoff

Parent approved durable option 2 and is assigning inactive publisher work and CP
claim persistence. B owns only the gated harness fixtures/tests, not duplicate
production adapters. Completion authority must bind the trusted workflow definition
and dependency digest, exact run and structured output, never LLM self-attestation.
Initial policy provisioning is private owner/admin configuration, with no public API.
No new generic outbox is approved.

Remaining inputs: independently approved publisher SQL scope/receipt repairs and
trusted graph-completion/history/publication/outcome adapters. The first SQL head
`fb8b69db` was BLOCKED for assignment scope and arbitrary receipt binding; it was
NOT picked. Wait `2dcef942` and authority `f41e7372` were independently approved;
the latter is assembled as `169a0e26`. Frozen actual SQL+CP fixture rerun passed
7.815s; the three scoped repaired runtime wait tests passed 0.506s after authority
assembly, with no end-to-end claim. Parent assigned reviewer03419 to exact frozen
REL8466/CP911d; those files are held unchanged pending findings, with no pushes/PRs.

B will integrate actual implementations only after these prerequisites arrive.
Full real-git-push coverage remains a separate approval: do not edit
GitPublish URL construction or weaken the literal provider guard. Until these
prerequisites exist, this is a plan, not a working or passing end-to-end harness.
