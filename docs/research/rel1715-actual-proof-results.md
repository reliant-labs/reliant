# REL1715 actual prepared-harness proof results

## Exact identities

Owned Reliant checkout:
`/home/workspace/.reliant/worktrees/reliant-labs/rel1715-harness-union-11b65d47/reliant`,
branch `test/rel1715-harness-prepared`.
Frozen capability base `90ea0a30b143b802a6068b1249b3ab41176e8dd4` plus exact
selected harness snapshot from `ee3af0b7`. Independently security-approved output-
only fix `a8078ed4b8d4def1f06d27a8aa828bef97498174` copied as `82cdf10b`.
No harness-owned production verifier/runtime lifecycle edits.

CP helper checkout:
`/home/workspace/.reliant/worktrees/reliant-labs/rel1715-cp-harness-2700af5b/control-plane`.
Helper commit `58c378ba39c11e72cca0d64f6a8a7ad27f75c8e1`, tree
`9497183115cac43d6d25edf6f89fcada99013e45`, production parent exactly
`5b65f0607fb87a8ecf4a8bcf8fb169308b8b2b3a`, tree
`e33c663e884eec56717ee7ae8756354aca1e6b8d` (same reviewed landed capability).
Only two integration-tagged helper files were imported from reviewed `5901d327`.

Verified official Temporal CLI 1.5.1 was restored only under
`/tmp/futurechat-b-tools/temporal-1.5.1/temporal`. Release archive SHA256
`ddc95e08b0b076efd4ea9733a3f488eb7d2be875f8834e616cd2a37358b4852d`,
same-release checksum file SHA256
`1b674a6e62e2e582cfd5cd84fa09f58844a2507d4621e0f4278a587ef9095a90`.
Both checked before extracting only a safe regular `temporal` member. Runtime
fixture additionally enforces executable SHA256
`eb4f6ddaa41cadf58c7a77aa50c4bd932d6739fcef04ac76c7a679eaff2f5e6a`
and `--version`. Private loopback server, namespace, queue and temporary SQLite;
private migrated SQL databases. No existing stack reused.

## Actual runs, not skipped proofs

| Scope | Judged code identity | Actual result |
|---|---|---|
| Two-round same-author repair, no reviewer CAN | `335f9edf6a6feb32873afc912d364ec35488c398`, tree `7cccc21cf08b74fb1822fafd5c609f25d0ba85cd` | PASS 85.748s |
| Combined two-round repair with real CAN in BOTH reviewers | `7b81dd85462764be0929fc5b58d719ea0d3e3207`, tree `0117bb7fdd3d047c180cd20af257a70e05a6b912` | PASS 90.129s |
| Output-expression adapter unit suite after supplied test-only strengthening | `5eebcd445c25cfebe7f04f8cd1df76b43449dd13`, tree `49ec9bda84deb9dac3a991abc1b0207eaaeb58c5` | PASS (cached) |
| Actual failure-terminal and stale-head regressions plus pure Postgres/GraphQL safeguards | `00ecb634ee61e17e6a68b0030e14bbed42774cb0`, tree `bc4b07cb66cc8faa891d22c95899b6f834a384e4` | PASS 64.366s |

The combined 90.129s proof predates test-only strengthening
`6c0cce5b19af0d5b79b8dafcc0176addad0f11dd` (local copy `5eebcd44`). That
follow-up changed adapter tests only, not production or the judged harness flow.
The negative-only late-doorbell assertion was subsequently added in `00ecb634`;
positive-path delivery errors remain fatal and the combined positive result is
not misattributed to a later tree.

## Exact commands

All actual fixture invocations removed every environment key starting with `PG`
(case-insensitive) and both `CP_TEST_POSTGRES_URL` / `FORGE_TEST_POSTGRES_URL`
before executing Go. This was done with this exact environment launcher:

```sh
python3 -S -c 'import os; e={k:v for k,v in os.environ.items() if not k.upper().startswith("PG") and k not in ("CP_TEST_POSTGRES_URL","FORGE_TEST_POSTGRES_URL")}; e.update(RUN_FUTURECHAT_B="1",TEMPORAL_CLI_PATH="/tmp/futurechat-b-tools/temporal-1.5.1/temporal",CP_CHECKOUT="/home/workspace/.reliant/worktrees/reliant-labs/rel1715-cp-harness-2700af5b/control-plane",GOWORK="off"); os.execvpe("go",["go","test","-tags","integration","-run","^TestFutureChatBChangesWakeSameAuthor$","-timeout","180s","./internal/privatepipelineharness"],e)'
```

Combined CAN used the identical launcher/environment with Go args:

```sh
go test -tags integration -run '^TestFutureChatBRepairWithReviewerCAN$' -timeout 240s ./internal/privatepipelineharness
```

Scoped negatives used the identical launcher/environment with:

```sh
go test -tags integration -run '^Test(FutureChatB(FailedGraphNeverApproves|StaleHeadCannotPublish)|PrivatePostgresGuard|QueueOracleRejectsForbiddenOperations)$' -timeout 240s ./internal/privatepipelineharness
```

Cheap adapter check:

```sh
GOWORK=off go test -short ./internal/reviewpublisheradapter
```

## What the combined proof actually establishes

One immutable, byte-identical reviewer graph and policy revision 1 serve heads A
and B. The external model activity returns changes for A, approval for B; verdict
comes from actual runtime node output, not a completion record inserted by tests.

For each reviewer the actual DynamicWorkflow loop reaches the engine's real
continue-as-new boundary. The integration-only existing pressure seam raises soft
pressure after the first model response, clears it only when the actual successor
loads, and never schedules a fabricated continuation. Real server history
resolution verifies one bidirectional continuation edge, terminal successor and
preservation of the private registered workflow type. SQL assignment retains its
original initial execution; the actual assignment resolver authenticates the
successor, and the private post-graph hook/history verifier produces evidence.

Real SQL Prepare -> fixed internal CP HTTP -> real sealed credential service,
operation-specific bridge and immutable create journal -> exact-head check receipt
-> atomic GuardedWaitStore -> original author's actual typed await node.

A's failing review is consumed by the original author. Its run-node external Git
fixture validates the actual activity execution, SQL consumed changes, registered
worktree/repository and zero prior queue mutations, then calls the REAL
ContinuationPublisher and provider branch read/CAS to advance A -> B. Old A intent
publication is rejected after the advance. B's reviewer uses the same policy/graph,
approves B, the original author consumes it and completes. Original author root
and run remain unchanged. Two distinct check creates (A failure/B success), one
native expected-head-B enqueue mutation, repeat enqueue observes membership.

SQL subscription ARMED is observed before each delivery. This proves **durable
arm-before-delivery**, NOT that the workflow coroutine had already reached a
blocked `workflow.Await`. No blocked-Await scheduling guarantee is claimed.

The author itself does NOT continue-as-new in this combined test; reviewer CAN
is combined with same-run author repair. Separate original-author continuity
proofs must be attributed separately, not presented as this test's coverage.
No old-history replay compatibility proof is claimed.

## Declared external and setup boundaries

* Model, GitHub and Git push are external fixtures. No live API, installation
  grant or real user shell/Git push executes. Repair still exercises real caller
  identity, ownership, provider reads, continuation normalization and SQL CAS.
* Initial publication/enrollment is a SQL receipt fixture. Admission uses actual
  enqueue/reserve/outcome/run-bind APIs, but initial webhook/launcher transport is
  trusted fixture setup, not a complete webhook-to-daemon proof.
* Immutable workflow loading is trusted fixture configuration with faithful
  `HasTemplates` metadata. Actual graph execution, history, terminal status,
  completion authority and stores remain real.
* Enqueue is performed by the test's trusted server-coordinator after the actual
  author completes. It is NOT an author-graph enqueue node; no such claim is made.
* External machine availability for the replaced repair Run step is explicit;
  real SQL status/checkpoint/cleanup activities are not success-only stubs.

## Failures found and fixed (not discarded as flaky reruns)

1. First prepared two-round attempt failed 122.690s because author graph's Run
   node required PreflightDaemonCheck but the author worker had not registered
   the declared external machine boundary. Narrow test-only registration fix
   `335f9edf`; no production change.
2. First CAN fixture used a nonexistent `.outputs` loop nesting and a loop that
   exited before pressure could reach a continuing boundary. Corrected actual
   flattened loop projection and two-iteration condition. No invented history.
3. Next CAN fixture omitted graph `outcome: success`; actual verifier correctly
   returned GraphSucceeded false. The immutable graph now declares its outcome;
   both heads use the same bytes and revision.
4. SDK WorkflowRun.GetRunID changes after Get follows CAN. Captured initial run
   immediately for immutable SQL enrollment/history proof instead of accidentally
   proving only the final run. Actual final proof validates the original chain.
5. Failure-negative run exposed a legitimate post-commit doorbell race: real
   Publish.Put commits the unavailable outcome, author consumes and completes,
   subsequent exact-recipient history sees terminal and returns ErrUnavailable.
   Negative-only helper requires confirmed failure check, exact durable consumed
   provider_sequence, unavailable outcome, same original terminal COMPLETED run
   and zero enqueue; every positive delivery error remains fatal. Actual code
   path is `prreviewwait/service.go` Put before Resolve/!Live/Unavailable return.
   First helper query incorrectly used `sequence`; corrected to actual
   `provider_sequence`, then the exact negative suite passed. No general error
   suppression or production behavior change.

No gates, activation, PR queue, merge or release ran. Independent final harness
review is still required before shipping; these receipts are scoped proofs only.
