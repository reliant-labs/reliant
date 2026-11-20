# PR records: webhook-fed pipeline state with head-bound checks

Owner-approved direction (2026-11-20 16:41Z): build §5.1 A of the pipeline-scale design
(webhook-fed PR records and head-bound check runs) as a **product** feature in reliant. This note is the plan for it: data model, event → record
transitions, API, how workflows use records, reconciliation, the migration and flip plan, and
the product shape. Phase 1 (shadow write) is being implemented now; everything after it is
planned here and waits for its own go.

Decisions that change a public surface are listed in §9 with a recommended default. Phase 1
proceeds on those defaults.

## 0. Settled inputs (not re-derived here)

- **One verdict per head** (cp `docs/pr-pipeline.md`, "One verdict per head"). The verdict of a
  head is the FIRST `<!-- review-passed|review-changes sha=<head> -->` marker on a review of
  exactly that head (`commit_id == head`) by a `marker_authors` login, unless an evidence
  re-review (`<!-- review-rereview sha=<head> of=<id> -->` by the bot) supersedes it. Labels are
  derived from it, never the reverse.
- **Carry** across merges of main is decided by `scripts/pr-pipeline/merge-carry.sh` (agent
  98e9d6f6). A carried pass is posted as a normal marker review on the new head
  (`## review:passed (carried)`, with `<!-- merge-regen of=... -->`). Records hold that verdict;
  they never re-implement the classifier.
- **Leases** today are git refs on origin (`refs/pr-review/lease/<n>/<sha>`,
  `refs/pr-fix/lease/<n>`), saved verdicts are `refs/pr-review/verdict/<n>/<sha>`. GitHub sends no
  webhook for those refs, so they reach records only when workflows write records (phase 2).
- **The durable inbox** (reliant bug 28919772, worktree `fix/durable-github-webhook-inbox`,
  table `trigger_delivery_inbox`): the receiver verifies, normalizes and inserts a delivery
  first, then a worker claims it under a fenced lease, fans it out and settles it. The stored
  payload is the *normalized* delivery (parsed events), and it is cleared on completion. Records
  are fed from that worker, inside the lease, never from a second intake.
- **The head-bound `Reliant review` check** exists on reliant and forge as an Actions job that
  reads the markers (`scripts/ci-review-marker.sh`); cp#3149 adds it to control-plane. The
  App-written `Reliant review (preview)` check is report-only (cp pr-pipeline.md, "The preview
  check").

## 1. Data model

Five tables in reliant's Postgres, all `forge:no-entity` (the ORM keeps their stores; no
generated CRUD, no generated pages). Facts are stored, policy is evaluated at read time: the
records never encode our `marker_authors` list or our label names.

### 1.1 `pr_records`: one row per pull request (repo, number)

| Column | Notes |
|---|---|
| `id` TEXT PK | |
| `provider` TEXT | `github` |
| `account_key` TEXT | the App installation id (routing and authz) |
| `repository_id` TEXT | GitHub's numeric repo id: stable across renames |
| `repository` TEXT | `owner/name` as of the newest snapshot (display, lookup) |
| `number` INT | |
| `state` TEXT | `open` / `closed` / `merged` |
| `draft` BOOL | |
| `head_sha`, `head_ref`, `base_ref`, `base_sha` TEXT | the current head |
| `author_id`, `author_login` TEXT | numeric id is the identity, login is display |
| `title` TEXT | capped |
| `labels` JSONB | the label set as of the newest snapshot (observed, not desired) |
| `merged_at` TIMESTAMPTZ NULL, `merge_commit_sha` TEXT | |
| `snapshot_at` TIMESTAMPTZ | the PR's own `updated_at` in the newest applied snapshot: the ordering guard (§2.2) |
| `observed_at` TIMESTAMPTZ | when Reliant last applied anything to it |
| `created_at`, `updated_at` | |

`UNIQUE (provider, repository_id, number)`.

### 1.2 `pr_record_heads`: one row per (record, head sha)

The unit of verdicts, checks, and later leases and dispatch.

| Column | Notes |
|---|---|
| `id` TEXT PK, `record_id` FK | |
| `head_sha` TEXT | `UNIQUE (record_id, head_sha)` |
| `first_seen_at` | |
| `superseded_at` NULL | set when a newer head becomes current |
| `previous_head_sha` TEXT | from `synchronize`'s `before` (feeds carry and supersede) |

Phase 2 adds (additive columns): `verdict`, `verdict_source` (`review` / `carry` / `rereview`),
`verdict_review_id`, `verdict_at` — the verdict as *written* by the review workflow through the
records API — and `check_run_id` (the App's `Reliant review` run on this head).

### 1.3 `pr_record_reviews`: verdict evidence (review markers)

One row per GitHub review that carries a pipeline marker. Facts only.

`(record_id, head_sha, review_id UNIQUE per repo, kind ∈ passed|changes|close|rereview,
of_review_id, author_id, author_login, author_is_bot, submitted_at)`.

The verdict of a head is computed from these rows with the one-verdict rule and a caller-given
set of trusted authors (§3). The rule is order-independent (first by `submitted_at`, then by
review id), so out-of-order deliveries cannot change it.

### 1.4 `pr_record_checks`: check runs on heads

`(provider, repository_id, head_sha, name, app_slug)` unique; `check_run_id`, `status`,
`conclusion`, `completed_at`. The newest run (highest `check_run_id`) wins, so a re-run replaces
the older run and an older delivery arriving late changes nothing. Keyed by sha, not PR: GitHub
reports checks per commit, and a merge-group sha has no PR.

### 1.5 `pr_record_deliveries`: applied-delivery log

`(provider, delivery_key, event_index)` unique, plus `event_type`, `record_id`, `applied_at`,
`outcome` (`applied` / `stale` / `ignored`). The dedupe key for idempotent projection (the inbox
already dedupes deliveries; this makes a *retried* projection after a partial failure a no-op)
and the audit trail the diff report and the dispatch-loss SLO read. Pruned after 14 days.

### 1.6 Later phases (additive tables, named now so the shape holds)

- `pr_record_leases(head_id, kind ∈ review|fix|merge, holder, token, expires_at)`: compare-and-swap
  leases replacing `refs/pr-*/lease/*`.
- `pr_record_dispatches(record_id, head_sha, stage, run_id, dispatched_at)` with
  `UNIQUE (provider, repository_id, number, head_sha, stage)`: **idempotent dispatch**. A stage
  starts only by inserting this row; a second trigger for the same key finds it and starts
  nothing.
- `pr_record_attempts(record_id, head_sha, stage, attempt, outcome)`: fix attempts, re-kick counts
  (replaces "3 attempts" counted from comments).
- `pr_record_queue(record_id, head_sha, queue, position, entered_at, left_at, reason)`: merge-queue
  position (GitHub `merge_group` and `pull_request.enqueued/dequeued`; cp-merge writes its own).
- `pr_record_edges(from_record, to_record, kind ∈ overlap|depends_on, detail)`: the overlap graph
  and §4.4's declared dependencies.

## 2. Event → record transitions

### 2.1 Where the projector runs

```
GitHub ──► /integrations/github/events ──► verify + Parse ──► INSERT trigger_delivery_inbox ──► 200
                                                                         │ (worker claims, lease)
                                                                         ▼
                                              ┌── prrecords.Projector.Apply(events)   (this plan)
                                              └── trigger routing / fan-out           (existing)
                                                                         ▼
                                                                  settle (lease-fenced)
```

The inbox worker calls a **delivery consumer** seam with the delivery's provider, delivery key
and normalized events, before routing. The projector is idempotent on
`(provider, delivery_key, event_index)`, so a delivery retried after a crash or a failed fan-out
re-applies nothing. A projector error makes the delivery retry like a routing error (trigger
events dedupe by delivery id, so the retry fires nothing twice). Only repositories in scope
(§9 D1) are projected; everything else is a no-op.

The seam is one small interface declared at the inbox (consumer side):
`ConsumeDelivery(ctx, provider, deliveryKey string, events []webhook.Event) error`. Until the
inbox merges, the projector ships unwired (dark) and a follow-up PR registers it. **No second
intake.**

### 2.2 Ordering and idempotence

GitHub does not order deliveries. Rules:

- **PR snapshot** (`pull_request.*`, and the PR object on `pull_request_review.submitted`): a
  full snapshot strictly newer (by the PR's own `updated_at`) replaces state, draft, head, base,
  title and labels. A review event's slim PR (no `merged`) only creates a missing record, and a
  full snapshot of the same second completes it (`snapshot_full`).
- **Same-second ties.** `updated_at` has one-second resolution, so two events of one PR can tie.
  Then each applies only its own transition (`core.PRRecordDelta`): `labeled`/`unlabeled` add or
  remove one label; `closed`/`reopened` set the state (a merged record never leaves merged);
  `ready_for_review`/`converted_to_draft` set draft; `edited` sets title and base;
  `synchronize` moves the head only from its `before`, and then follows one known push over the
  new head (two pushes in one second, applied out of order, still end on the last). What a tie
  cannot order (three pushes in a second out of order; a label added and removed in one second)
  is corrected by the reconciler (§5).
- **Heads**: a head row is created for every sha a snapshot or `synchronize` names; the current
  head is the record's, and every other gets `superseded_at`.
- **Reviews and checks**: insert-or-ignore by review id; checks keep the highest run id. Both are
  commutative.
- **Closed / merged** is terminal for automation but reversible (`reopened`), guarded by
  `snapshot_at` like any other state.

### 2.3 Transition table

| Event | Record change |
|---|---|
| `pull_request.opened` / `reopened` / `ready_for_review` / `edited`† / `converted_to_draft`† | upsert record from the snapshot; ensure the head row |
| `pull_request.synchronize` | upsert; new head row with `previous_head_sha = before`; old head `superseded_at` |
| `pull_request.labeled` / `unlabeled` | label set (snapshot or tie delta, §2.2) |
| `pull_request.closed` | `state = merged` (with `merged_at`, `merge_commit_sha`) or `closed` |
| `pull_request.enqueued`† / `dequeued` / `auto_merge_disabled` | phase 1: snapshot only; phase 2: `pr_record_queue` |
| `pull_request_review.submitted` | snapshot (slim PR: `merged` absent, so state is not downgraded); a `pr_record_reviews` row for each pipeline marker in the review's `markers` (§4.1) |
| `check_run.completed` / `rerequested` | `pr_record_checks` by head sha |
| `check_suite.*`, `push`, `merge_group`† | phase 2 (queue and main moves; `push` to the base feeds overlap staleness) |

† not yet emitted by the parser; adding an event type is additive (a new projection YAML).

## 3. API surface

### 3.1 Phase 1 (read only)

`reliant.v1.PullRequestRecordService` (`proto/reliant/v1/pull_request_record.proto`; withheld from the SDK in `proto/public-api.txt` until the flip). List resolves the repository identities the caller's grants reach before it pages (a name can have named a deleted repository), with a `(number, repository id)` cursor:

| RPC | Request | Returns |
|---|---|---|
| `GetPullRequestRecord` | `repository` (`owner/name`), `number`, `trusted_authors[]` (optional) | the record, its heads, the current head's reviews and checks, and the current head's **computed verdict** under `trusted_authors` |
| `ListPullRequestRecords` | `repository`, `optional state`, `optional updated_after`, page token | records with their current head's computed verdict |

**Authz**: the caller must hold a fresh GitHub access grant for `(installation, repository_id)`
(`integration_event_access`, the same check that gates event routing). No grant, no rows.
`auth_required` stays on. An access token needs the existing triggers read scope.

**CLI**: `reliant github records get|list|diff --repo owner/name [--pr N] [--json]`. `diff` is the
shadow report (§6): it lists records and compares each current head's computed verdict and
`Reliant review` check with the observed labels. The label vocabulary and trusted authors are
flags whose defaults are Reliant's built-in review conventions (`review:passed`,
`review:changes`, `merge:queue`, `hold`), so the server API stays policy-free.

### 3.2 Phase 2 (write; behind its own owner go)

- `WritePullRequestVerdict(repo, number, head_sha, verdict, source, review_id, lease_token)`:
  fenced by the head's review lease; first writer wins per head (the one-verdict rule moves from
  "first marker" to "first record write", with the marker still posted as its projection).
- `AcquirePullRequestLease` / `RenewPullRequestLease` / `ReleasePullRequestLease`.
- `DispatchPullRequestStage(repo, number, head_sha, stage) → {created | existing(run_id)}`.
- `SetPullRequestQueuePosition`, `RecordPullRequestAttempt`.

## 4. How workflows read and write records

### 4.1 Evidence the parser must keep (phase 1, additive)

The review body is capped at 16 KB in the normalized payload and the pipeline's markers are at
its **end**, so a long review loses them. The parser gains one generic, additive field on
`pull_request_review.submitted` (and `issue_comment.created`): `markers`, the body's HTML-comment
markers of the form `<!-- name k=v ... -->` (at most 16, each ≤ 256 bytes), extracted **before**
the cap. It is generic (any pipeline's markers), visible to filters as
`trigger.payload.data.review.markers`, and is what the projector reads.

### 4.2 Reads (phase 2)

1. **CEL-visible input.** An enricher (the existing `triggers.Enricher` seam) attaches
   `trigger.record` to every GitHub PR event fired to a trigger in a recorded repository: the
   record and its current head as of the moment the event was applied. Filters like
   `trigger.record.head.verdict == ''` replace label reads and the GitHub calls that fetch them.
2. **Server actions** `reliant/pr_record.get@1` and `reliant/pr_record.list@1`
   (`placement: server`, no machine), for a fresh read mid-run.
3. The CLI, for scripts (pr-fix.sh, cp-merge-gate.sh) during migration.

### 4.3 Writes (phase 2)

Server actions `reliant/pr_record.verdict@1`, `lease.acquire|renew|release@1`,
`dispatch@1`, `queue@1`, each mutating and workflow-only (like `check_run.create`). The record
then **projects** outward, in one place:

- the `Reliant review` check run on the head (App-written; created at dispatch `in_progress`,
  concluded from the verdict; a fault is `failure`, never `neutral`);
- the label set, written by the bot as a display projection: one diff-write per change instead
  of today's remove/add round trips;
- a record-changed trigger event (`reliant.pr_record.changed`) that workflows activate on in
  place of `pull_request.labeled`. This is what makes dispatch idempotent: the trigger fires
  per record transition, and a stage starts only through `dispatch@1`.

## 5. Reconciliation

Two loops, both in the worker, both cheap on quota:

1. **Deliveries** (engine-level, belongs with the inbox): page `GET /app/hook/deliveries` with
   the App JWT (no installation quota) since a cursor; any delivery that GitHub marks failed, or
   whose GUID has no inbox row, is redelivered (`POST .../attempts`). This closes the gap of a
   503 GitHub never retries. Scheduled every 5 minutes. Proposed to the inbox owner, not
   duplicated here.
2. **Records**: for each recorded repository, every 30 minutes, one GraphQL query lists open
   PRs (`number, headRefOid, isDraft, state, labels, updatedAt`, 100 per page). A record whose
   head, state or labels differ and whose `observed_at` is older than the settle window is
   corrected and logged as `reconciled` in `pr_record_deliveries`. The rate of corrections is
   the dropped-event SLO (§5.5 of the scale design). Reviews and checks are re-read only for
   heads the GraphQL pass marked changed.

Phase 1 ships loop 2 as report-only (the diff report names drift; nothing is corrected) if
quota allows; otherwise it lands with phase 2.

## 6. Shadow, diff report, and the flip plan

### Phase 1: shadow write (now)

- Records are written for the repositories in `pr_records.repositories` (server config, our
  three repos), from the inbox. Labels, markers, Actions checks and every workflow are
  unchanged; nothing reads records except the report.
- `reliant github records diff --repo <r>` classifies each open PR:
  `agree`, `label_missing` (verdict passed, no `review:passed`), `label_stale` (a verdict label
  with no verdict at this head, or the wrong one), `check_disagrees` (`Reliant review` conclusion
  vs verdict), `settling` (applied within the last 10 minutes), `held` (`hold`), and
  `record_missing` (labels with no record).
- pr-sweep (control-plane config, follow-up PR) runs the diff each sweep and posts the counts to
  the pipeline-health digest. Exit criterion per repo: **3 days with zero unexplained
  `label_*`/`check_*` rows** and zero `record_missing` for PRs opened after the shadow start.

### Phase 2: records written by the workflows (still shadow)

pr-review writes its verdict through `verdict@1` (beside the marker), takes the review lease
from records (beside the git ref; both must succeed), and opens/concludes the App
`Reliant review` check from the record. pr-fix and cp-merge write attempts and queue position.
The diff report gains "record verdict vs marker" and "record lease vs ref lease".

### Phase 3: flip one repository at a time

Order: **forge** (fewest PRs, GitHub queue, check already head-bound), then **reliant**, then
**control-plane** (cp-merge reads the most state). Per repo, a `pr_records.authoritative`
setting:

1. Readers (pr-review `authority`, pr-fix `head_verdict`, cp-merge `eligible.reviewed`,
   pr-sweep's buckets, the enqueue sweep) read the record instead of listing reviews and
   labels.
2. Dispatch moves from label triggers to `reliant.pr_record.changed` + `dispatch@1`.
3. The ruleset requires the App-bound `Reliant review` check (integration 1041943), including
   on merge-group shas (cp#3109's pattern); the Actions `Reliant review` job is retired after
   one clean week.
4. Labels keep being written, by the record projector only.

Rollback per repo: clear `authoritative`; the label triggers still fire because labels are
still projected, and the Actions check still runs until step 3's retirement.

### Phase 4

Retire the label-triggered activations and the marker readers. Labels stay as display.

## 7. The product primitive

What customers get, in order of value:

1. **PR records**: a webhook-fed, reconciled mirror of every pull request in repositories they
   enable, readable from workflows (CEL `trigger.record`, server actions) and the API, with no
   GitHub quota spent per read.
2. **Head-bound verdicts and the `Reliant review` check**: one verdict per head, a check a
   ruleset can require by App id, faults that never pass.
3. **Idempotent stage dispatch and leases**: "exactly one review per head", "one fixer per PR"
   as primitives, not as per-workflow git-ref tricks.
4. **Label projection**: labels as a readable mirror of state, never its source.
5. Later: overlap graph, declared dependencies and restack (§4.4 of the scale design), the
   stack-aware queue.

Enablement: per repository, from the repository's settings or a builtin pipeline activation
(§9 D1). Our policy (marker authors, label names, risk tiers) stays in our workflow config.

## 8. Phase 1 deliverables (being implemented)

| PR | Content |
|---|---|
| reliant A | Parser: generic `markers` on review and issue-comment events, extracted before the cap (§4.1) |
| reliant B | Migration (§1.1–1.5) + store + `internal/prrecords` projector (ordering §2.2, transitions §2.3) + read RPCs + CLI (`get`, `list`, `diff`). Projector unwired until the inbox lands |
| reliant C | The scope config (`RELIANT_PR_RECORD_REPOSITORIES`) and the projector registered as the inbox's delivery consumer (after the inbox PR merges) |
| control-plane D | pr-sweep posts the diff counts to pipeline-health (after B ships to prod) |

## 9. Decisions for the owner

Phase 1 proceeds on the recommended option of each.

| # | Decision | Options | Recommended |
|---|---|---|---|
| D1 | **Which repositories are mirrored** | (a) opt-in per repository; shadow uses a server allowlist of our three repos. (b) every repository of every installation of the App | **(a)**: storage and privacy scale with consent, and nothing is recorded for a customer who never asked |
| D2 | **API stability of the phase-1 read RPCs** | (a) ship `reliant.v1.PullRequestRecordService` as a normal v1 surface now (read-only, two RPCs, additive later). (b) ship it unlisted (no CLI/docs) until the flip | **(a)**: the read shape is small and facts-only; the write surface (phase 2) gets its own review |
| D3 | **`markers` on review/comment event payloads** | (a) add the generic field (additive, visible to every trigger). (b) keep it internal to the projector (a second, unexported parse) | **(a)**: other pipelines need the same evidence, and one parse is simpler |
| D4 | **Phase 2's workflow interface** | (a) server actions `reliant/pr_record.*@1` + CEL `trigger.record` + a `reliant.pr_record.changed` event. (b) CLI only | **(a)**: no machine needed for state, and dispatch becomes idempotent by construction |
| D5 | **App subscription to `merge_group`** (and `merge_queues: read`) | (a) subscribe at phase 2 for queue position. (b) infer from `pull_request.dequeued` + checks | (a), when phase 2 starts; an App setting, no money |

No option here costs money.
