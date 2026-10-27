# REL1715 isolated harness port inventory

## Identity and boundaries

- Frozen production base: `90ea0a30b143b802a6068b1249b3ab41176e8dd4`.
- Exact source snapshot: `ee3af0b7ab05e9e7b02d76b12f2ea895f426c910`.
- Test-only snapshot commit: `c2e176173efd149e4834f1d3c375fa0582c10639`.
- Worktree: `/home/workspace/.reliant/worktrees/reliant-labs/rel1715-harness-union-11b65d47/reliant`.
- Branch: `test/rel1715-harness-prepared`.

Only the selected files below were imported. No component history, generated files, production verifier fix, or CP changes were imported. The frozen worktree was not edited. Apart from the explicitly integration-build-tagged runtime seam, production code equals the frozen base. This inventory is the only newly authored documentation; both source research briefs are preserved verbatim as historical handoffs.

## Exact copied file inventory

Every listed file was checked byte-for-byte against `git show ee3af0b7:<path>` and by git blob ID. Paths in the first table are relative to `internal/privatepipelineharness/`.

| File | Exact source blob |
|---|---|
| README.md | 42796f5a78abb45de9b59ecb2ca3391b09e07a27 |
| continuity_integration_test.go | 92ef099cc233663166d5b833ccfa0be8c1e0345b |
| cp_process_integration_test.go | ecadbbf2d4442e11262854f541ecf3403055c818 |
| cp_roundtrip_integration_test.go | 96c5e09b685fdf74f8e0e1c6cca6f2f7ba847557 |
| dynamic_runtime_integration_test.go | b439438b1fcc06ffd541a01caa8cdbff34b17be5 |
| github_integration_test.go | 4b9f06bde1eaee58e0805dad8d41c77f4f2ae6d4 |
| postgres_guard_integration_test.go | e7dab42cd2a24cfc92b5542023e14db54108a9aa |
| publisher_boundary_integration_test.go | 99a383a2d3709958e7733cbce624c910c5347b04 |
| queue_oracle_integration_test.go | 5513d40cc793f5c8d4bac7e28697baa496bf757f |
| repair_round_integration_test.go | 27b98027e8d2a606e53ed2dfe247346557015097 |
| resources_integration_test.go | 60a5f62168a77a6369086a42ba19f98ed6c73727 |
| temporal_process_integration_test.go | 0fa8ae4b50b3f6077bc7119f1e8c251e7272e163 |

| Other copied path | Exact source blob |
|---|---|
| internal/workflow/runtime/private_pipeline_testhooks_integration.go | 84902108cf7af6f26890adf61cef99d2e52483df |
| docs/research/futurechat-b-integration-harness-plan.md | 933bc3ab87a3d13ad696f7fdbf1256b3e284cb1c |
| docs/research/futurechat-b-repair-harness-handoff.md | 312b4032ad47e791bee7db77c79a4210c9040517 |

The runtime seam starts with `//go:build integration`; it exposes only the existing per-chat history-pressure test override and is absent from production builds.

## Verification actually performed

`GOWORK=off go test -short -tags integration ./internal/privatepipelineharness` **PASS** (0.094s). This compiles the complete integration harness and runs only its short/pure boundary checks. No real SQL/Temporal/CP proof was run. No output-only verifier fix is included yet. This is readiness, not a passing same-author two-round or combined-CAN proof.

## Minimal CP helper integration — applied on isolated landed checkout

On a separate isolated checkout of the independently reviewed CP1420/1421 plus SQL-claim production stack, import only these two final files from CP `5901d327cd87b2658c5e90a9fde542d21b99073f`:

| CP path | Exact final blob |
|---|---|
| internal/privatepipelineharness/review_check_server_test.go | 096e9312ddf4f1bfc1b75e73ed3ef7c03625def1 |
| internal/privatepipelineharness/postgres_guard_integration_test.go | e7dab42cd2a24cfc92b5542023e14db54108a9aa |

Both are `//go:build integration` test files. The first originates in `911d0e1e45c5cbb47de2e93b3c262bcedb8298ee`; `5901d327` adds the DSN guard and corrects its call to `guardPrivatePostgres(t)`. The final guard file is byte-identical to the Reliant harness guard above. A pair of these test-only cherry-picks is also acceptable if neither helper already exists. Do not import B's production-copy history (`1c8fd532`, `27419c57`, `4bc13479`) onto an already reviewed assembled stack.

Required CP production APIs are the reviewed inactive operation-specific bridge, gated internal check RPC, sealed owner credential repository, and immutable SQL check journal (`reviewcheckbridge.NewSQLJournal`), corresponding to CP1420/1421 and SQL claim `3b1a38ff` (B's local duplicate `4bc13479`). Verify the actual reviewed stack has those APIs rather than cherry-picking duplicates. The helper composes those real implementations; it is not a production activation.

First validation after that test-only import: `GOWORK=off go test -short -tags integration ./internal/privatepipelineharness`. Later, only after the reviewed verifier fix is supplied and proof is authorized, set `CP_CHECKOUT` to that prepared CP checkout. The Reliant harness builds/starts the explicit `TestFutureChatBCPServer` helper itself. Keep external PostgreSQL overrides and PG* variables removed as specified by the source handoff. No production configuration, migration, or API change is needed for the helper port.

## Follow-up prepared identities and verification

B now owns this prepared checkout, not its historical source production copies.
The supplied output-only fix `a8078ed4b8d4def1f06d27a8aa828bef97498174`
was cherry-picked as `82cdf10b6215424104af35a46f3e73f877566047`, resulting tree
`dc85ef76a233964982de0d2dd265116fab9e61fe`. No verifier/hook code was authored
by the harness owner. Independent security review is pending; heavy proof is held
until explicit parent PASS.

Minimal CP checkout:
`/home/workspace/.reliant/worktrees/reliant-labs/rel1715-cp-harness-2700af5b/control-plane`,
branch `test/rel1715-cp-harness`. Exact landed production parent:
`5b65f0607fb87a8ecf4a8bcf8fb169308b8b2b3a`, tree
`e33c663e884eec56717ee7ae8756354aca1e6b8d`, identical to reviewed `da715fb9`.
Only the two helper files above were imported with exact listed blob IDs.
Test-only helper commit `58c378ba39c11e72cca0d64f6a8a7ad27f75c8e1`, tree
`9497183115cac43d6d25edf6f89fcada99013e45`. No prior CP component history copied.

Commands actually run on these prepared checkouts:

```sh
GOWORK=off go test -short -tags integration ./internal/privatepipelineharness
```

PASS CP 0.050s; PASS Reliant 0.128s. These compile and run pure safeguards only;
SQL/Temporal/CP integration proofs skipped under `-short`. No two-round or combined
CAN success is claimed. After machine reconnect the previously verified `/tmp`
CLI is absent; restore only the same approved official v1.5.1 artifact with both
fixed release hashes and the runtime executable hash before actual proof.

Future CP_CHECKOUT must name the new landed-capability checkout above, not the
historical FutureChat B checkout. Actual two-round command remains gated on the
reviewed output-only fix's explicit security PASS. All PG* and both external
Postgres overrides must be removed before that invocation.

No push, queue, gate, merge, activation, or release occurred during preparation.
