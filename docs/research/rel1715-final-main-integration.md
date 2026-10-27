# REL1715 final current-main integration candidate

## Immutable inputs

- Prepared source: `635cd9b0b7e0381d8c6ea9880339be1570cfcd92`, tree `00d6bf836a9db48f20f09a59d7b6c531ff73cd0d`.
- Main fetched once: `f72ad0df51430621bdc02d96b6c4e48f458c7cf5` (#1623 global window).
- Source/main merge base: `a928b594f5339d3f42f3a9e42f7230b9a30da21b`.
- New isolated worktree: `/home/workspace/.reliant/worktrees/reliant-labs/rel1715-final-candidate-9e9243e5/reliant`.
- Branch: `worktree/rel1715-final-prepared`.
- Method: ordinary merge of main into the exact prepared source. The reviewed proof checkout remains frozen and unmodified.

## Exact delta and resolution inventory

There are 167 changed source paths and 124 changed main paths relative to their merge base (counting deleted/added paths separately, without rename folding). Their **only intersection is `gen/forge_descriptor.json`**. Every source-side and main-side changed file/deletion outside that shared generated descriptor was compared to its respective immutable parent and preserved byte-for-byte. There were zero owned-file conflict resolutions.

Exact inventories are reproducible without relying on a moving branch:

```sh
git diff --no-renames --name-status a928b594f5339d3f42f3a9e42f7230b9a30da21b 635cd9b0b7e0381d8c6ea9880339be1570cfcd92
git diff --no-renames --name-status a928b594f5339d3f42f3a9e42f7230b9a30da21b f72ad0df51430621bdc02d96b6c4e48f458c7cf5
```

For this integration commit, the delta against the prepared source is exactly the 124-path main inventory above (with the descriptor projected from BOTH parents), plus this integration report. Against main it is exactly the 167-path source inventory above (with the descriptor projected from BOTH parents), plus this report. No other changes were made.

Main's #1623 UI/Electron/config changes and chat/worktree API, handler and store changes are preserved. Main's earlier #1707 bot defaults and #1711 enqueue remain in ancestry. Source output-expression verifier fix, security test strengthening, harness fixtures and attributed proof receipts are unchanged. No runtime/osutil lifecycle changes were authored or imported beyond these exact parents; #1705 remains separately owned. No CP checkout, dependency pins, vendored CP contracts or production CP wiring were changed.

## Generated conflict resolution

Only the generated descriptor conflicted. It was not hand edited. Exact `forge.yaml`/`go.mod` pin coherence passed; `task deps:protoc-gen-es` installed the lockfile-pinned plugin. Canonical generation passed:

```sh
env GOWORK=off GOTOOLCHAIN=go1.27.3 GOFLAGS=-trimpath \
  FORGE_NO_MERGE_DRIVER=1 FORGE_NO_GIT_HOOKS=1 \
  go run github.com/reliant-labs/forge/cmd/forge@v0.1.47-0.20261027025742-2372776cf3d4 \
  generate --heal --force --no-cache
```

All five declared generation hooks and validation ran. No unstaged generator leftovers remained after staging the descriptor. The inherited generated merge driver still names an absent old binary; no shared git configuration was altered.

## Migration/proto safety

Main's migration high-water mark remains `20261027063137_trigger_events_unlaunched_index.up.sql`. The six already rebased private migrations range from `20261027083800` through `20261027083808`, in the same dependency order and with the previously captured SQL hashes. **No migration rename or SQL edit was necessary or performed.** All main migration files and source private migration bytes remain unchanged. No proto input conflict or tag renumbering occurred.

## Validation on this assembled candidate

Only bounded scoped checks were run, sequentially:

```sh
go test -short -p 1 ./internal/handlers/chat ./internal/reviewpublisheradapter ./internal/workflow/validation ./internal/workflow/yaml ./internal/triggers/triggerspec
go test -short ./internal/store -run 'Test(PRHandoff|PRReviewWait|ReviewAuthority|ReviewAdmission|TriggerAdmission|ChatListAndSearch)'
GOWORK=off go test -short -tags integration ./internal/privatepipelineharness
go test -short ./internal/app -run 'Test.*(Temporal|Namespace)'
go build ./internal/app ./internal/handlers/chat ./internal/reviewpublisheradapter ./internal/workflow/runtime
```

All PASS. The selected SQL tests use the real isolated store harness; the integration-tagged `-short` run compiles the full private pipeline harness but runs only pure safeguards. No actual heavy Temporal/CP pipeline proof was rerun on this merged tree. Existing exact proof identities/results remain attributed in `docs/research/rel1715-actual-proof-results.md`, not relabeled as this candidate's results.

Exact-pin `lint --no-fix --conventions --migration-safety`, scoped to `internal/handlers/chat`, `internal/reviewpublisheradapter`, `internal/store`, `internal/workflow`, `internal/app` and `proto/reliant/v1`, **PASS**: two gating lanes passed, two advisory findings not shown by quiet mode.

Inventory fingerprints (SHA-256 of exact `git diff --no-renames --name-status` output from the commands above): source `7ac676eeb75dfc92ece72fd6a5d4c214c351d5cea3cb0a4cfcbec29052a5d152`; main `b57b1c3f4098be3c21935107faa8ea14c9c300cac3fb551a432bd3e4c7a0dc7b`. Canonical resolved descriptor blob: `a87bf9b85208f7bdff6cf7ad8217c3e45232c754`.

An inventory Python invocation emitted environment `sitecustomize` memory-allocation warnings but completed the byte-preservation assertions successfully (exit 0). Subsequent inventory commands use `python3 -S`; no process was killed or private cache introduced.

## CP helper and shipping boundary

CP helper `58c378ba39c11e72cca0d64f6a8a7ad27f75c8e1` is intentionally a separate test-only setup, not present on CP main. The source proof receipts identify its two integration-tagged files and exact production parent. This candidate does not introduce a production dependency on that helper or copy any CP production history.

The private reviewer/handoff capabilities remain off by default: no publisher activation, production workflow registration, or public internal-node exposure was added. Existing PR #1715 was read-only observed as draft, head `ef57d9add8380f155a82bf1654b4513612f1b16b`, branch `worktree/review-publisher-adapters-1793083910`, base `feat/private-review-sql-authority`. No push or PR mutation occurred. Parent independent FINAL integration review must precede the parent's lease-push/retarget/readiness/queue operations. No whole-repo gate, merge or release was run.
