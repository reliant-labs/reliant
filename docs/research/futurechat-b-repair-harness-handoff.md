# FutureChat B: two-round harness handoff

## Status and porting boundary

This is a test-only readiness handoff, NOT a passing two-round or combined-CAN
proof. The owner approved an output-only verifier allowance; f632 owns that
production fix on frozen union `90ea0a30`. B has not changed verifier/hook code
and will not rerun the heavy flow until the independently reviewed fix arrives.

Owned workspace:
`/home/workspace/.reliant/worktrees/reliant-labs/futurechat-b-integration-0b142c20`.
Branch in both repos: `test/futurechat-b-integration`.

The test-delta commit accompanying this document has source parent
`4219a9a3f6fdb40b665145b8504a6ef7c6b7a400`. Apply ONLY that new test commit to
an assembled union, not the entire B branch: the branch contains cherry-picked
copies of already-reviewed component work. Do not switch or edit main checkouts.

Required existing B test files are under `internal/privatepipelineharness`:
resources/Temporal-owned-process guard, CP subprocess helper client, strict
GitHub HTTP boundary, real SQL runtime registrations and publisher fixture.
Their frozen milestones are `8466d94d`, `d5c4ca39`, `45e35364`, `6a3695aa`,
`50ec9daf`, `9300a6f8`, `40ceff72`, `6b0300bd`, `6fafb239`, `c4b468d3`,
`c99c21a5`, `0cf4eb0b`, `21acbaa0`, `70432349`.
CP helper prerequisite: CP `911d0e1e` plus DSN safety fix `5901d327`, on reviewed
CP1420/1421 and SQL claim `3b1a38ff` (B local copy `4bc13479`).

Production/API prerequisites already present in B's source parent:

| Reviewed source | B local copies / purpose |
|---|---|
| REL1709 `bb4b8d13` | original-author SQL publication/enrollment, provider/author adapters |
| REL1710 reviewed chain through `561b7ace` | real enqueue/reserve/event projection/run binding APIs |
| Wait `f90221ad` + `2dcef942` + authority `f41e7372` | actual typed `await_pr_review`, SQL inbox and authority |
| SQL `fb8b69db` + `5923dee4` | `4af79426` / `477fd7cd`, policy/assignment/intent/receipt binding |
| Publisher `2587b2c8` + `3f6bbf12` + `27e54611` | `d1e16f05` / `34f6647f` / `ddd2d531` |
| Completion `ce888fad` TOGETHER WITH `c6d09126` | `5ca5f401` / `8277932f`; ce888 alone is unsafe |
| Guarded wait `17094e0b` | `e1b758dd`, atomic SQL authority+inbox; calls use root context, not ambient tx |
| CAN `9f01bfed`, `a4f2b4ae`, `bbdf6e9e` | `f94b04f9`, `4a69ac87`, `6499f2e0`; fixture supplies real History after API change |
| Landed queue identity `0a0bb94d` | `c2a1bf1e`, exact GraphQL identity query/output schema |

The integration-only pressure seam `4219a9a3` is build-tagged `integration`, not
production code. It is not yet used to claim combined reviewer/author CAN.
Later optional hardenings `91ccb5a8` / `d8b424e1` are NOT in B's source parent;
check frozen union ancestry rather than importing duplicate copies blindly.

## New test-only delta

* `repair_round_integration_test.go`: real blocked-wait SQL observation; byte-
  identical revision-1 reviewer graph; deterministic external model boundary;
  actual original-author repair activity invoking real ContinuationPublisher;
  second reviewer run/admission/assignment/history/check/inbox delivery.
* `publisher_boundary_integration_test.go`: `changes` scenario now exercises
  wait A -> repair -> wait B in the SAME author DynamicWorkflow execution.
  Reviewer A and B use one immutable graph and policy revision 1. Actual paths
  never insert completion evidence directly; boundary-only test remains explicit.
* `cp_roundtrip_integration_test.go`: current provider head B, separate provider
  check IDs for each publication, real branch GET projection, queue head binding.
* `queue_oracle_integration_test.go`: same exact approved operations and strict
  variable/auth checks; expected SHA comes from fixture provider state. Rejection
  cases seed a successful check and require HTTP400, so missing-check rejection
  cannot accidentally prove the allowlist.
* `dynamic_runtime_integration_test.go` and both reviewer fixture loaders:
  `HasTemplates = strings.Contains(definition, "{{")`, matching actual loader
  `load_workflow.go:212`. No false flag to bypass production verifier.

## Intended two-round flow and assertions

1. Start actual author DynamicWorkflow before its worker; use actual initial run
   ID for immutable SQL publication/enrollment. Seed registered worktree and repo
   rows for the real author adapter. Initial publication remains a SQL receipt
   fixture, not an actual git push.
2. Start reviewer A and bind real admission via Enqueue/Reserve/UpdateOutcome/
   BindRun APIs. Assignment uses actual catalog GitHub head reads.
3. One static reviewer graph invokes actual runtime `call_llm`; the external
   model activity fixture returns `changes` for provider head A, then `approve`
   for B. Graph output derives `review_verdict` from that node's response_text.
   Revision remains 1 and YAML/JSON bytes remain identical both rounds.
4. Real private hook + exact server history -> SQL terminal evidence -> core
   Prepare -> actual CP HTTP/credential/SQL journal/check -> confirmed receipt.
5. Observe author A subscription ARMED in SQL before delivery. Actual typed
   wait consumes changes and proceeds to `run` node. That node's external Git
   fixture checks actual Temporal identity and SQL consumed changes, then moves
   the fake provider branch to B and invokes real ContinuationPublisher.
6. Observe author B subscription ARMED before starting reviewer B. Old A intent
   must fail current-head revalidation. New admission/assignment generation,
   actual graph/history/check B -> same author resumes and completes approved.
7. Assert original author root/run unchanged, binding current head B, consumed
   generation B, two distinct check create attempts (A failure/B success), zero
   enqueue before B, then one expected-head-B native queue mutation and duplicate
   invocation observes membership.

`prhandoffadapter.ExecutionReader` names its method DescribeWorkflowExecution but
expects SDK's structured WorkflowExecutionDescription. Actual SDK supplies that
through DescribeWorkflow. A test-only method-name translation calls the REAL SDK
method and invents no availability facts. Branch binding uses concrete
`store.PRHandoffStore`, not an ad-hoc SQL reader.

## Honest external boundaries

* Git push and command execution are a fixture boundary: ExecuteRunStep does NOT
  execute a shell. Real activity caller, author/worktree ownership, provider
  re-read, continuation proof and publication CAS remain actual adapters.
* LLM and GitHub are external deterministic fixtures; no live API or token grants.
* Enqueue is invoked by the test's trusted server-coordinator AFTER actual author
  completion, not by a node of the author graph. Do not claim author-owned enqueue.
* Initial webhook/launcher projection is trusted test setup through real admission
  APIs; it is not an end-to-end webhook/daemon/git transport proof.

## Prior results and blocker

Actual approve path with both DynamicWorkflow graphs, guarded SQL inbox, CP HTTP
and strict native queue oracle passed 35.107s on the pre-output-only graph.
Earlier negatives (first changes wake only, failure verdict, stale head) passed
65.013s before changing `changes` into the two-round fixture.
Those results DO NOT prove the new two-round fixture.

The two-round attempt (`746e0333-aff0-4d15-a924-b8d546d5d957`) failed at actual
CompletionActivity after CallLLM returned changes. `completion.go` rejected any
raw YAML containing `{{`, including approved output CEL; the real loader also
sets HasTemplates true for those bytes. No literal verdict or policy-revision
workaround is used. f632 owns the reviewed output-only fix. Brief failure evidence
also exists at `/tmp/futurechat-b-repair-blocker.md`; this checked-in document is
its durable handoff, not a claim the temporary file must survive.

## Commands after the reviewed production fix is supplied

First, apply only the reviewed production fix and this test delta onto frozen
union; compare file paths and preserve canonical generated inputs. Never edit
production completion policy from this harness branch.

Pure/compile check (no SQL/server proofs):

```sh
GOWORK=off go test -short -tags integration ./internal/privatepipelineharness
```

Actual two-round proof, with BOTH external Postgres overrides and ALL PG*
environment variables removed before invocation:

```sh
RUN_FUTURECHAT_B=1 \
TEMPORAL_CLI_PATH=/tmp/futurechat-b-tools/temporal-1.5.1/temporal \
CP_CHECKOUT=/home/workspace/.reliant/worktrees/reliant-labs/futurechat-b-integration-0b142c20/control-plane \
GOWORK=off go test -tags integration -run '^TestFutureChatBChangesWakeSameAuthor$' \
-timeout 180s ./internal/privatepipelineharness
```

Verified official executable SHA256 enforced by fixture:
`eb4f6ddaa41cadf58c7a77aa50c4bd932d6739fcef04ac76c7a679eaff2f5e6a`.
Only owned random-port server/namespace/temp SQLite/private SQL clones are used.
No unknown process cleanup, gates, activation, PR, merge or release commands.
