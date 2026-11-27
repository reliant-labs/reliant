// Copyright (c) 2025 Reliant Labs

// Package mergequeue is the Reliant merge queue's scheduler (pipeline
// redesign §3.1–§3.4; MVP tasks M7 and M8).
//
// One worker drives each enrolled repository at a time: the [Scheduler]
// holds a session-level Postgres advisory lock keyed by the repository's id
// ([LeaderLock]) for as long as it drives it, and a second worker that asks
// for the same repository is told no. Each [Scheduler.Tick] of a repository
// it leads takes the next batch through its whole life:
//
//  1. pick: the admissible entries (DB state, [Admission]), at most
//     queue.batch_max of them by priority then enqueue time, an urgent one
//     (priority >= 90) alone;
//  2. build: the [Builder] squashes them onto the base, pushing
//     refs/heads/reliant-queue/<batch>, as a job on a gate pool daemon;
//  3. gate: the [Gate] judges the candidate (M9's gaterun.Dispatcher behind
//     an adapter);
//  4. record: queue_batches, main_health and, on an ejection, the PR's
//     pr_pipeline row woken (next_action=decide).
//
// Shadow mode (pipeline_repos.mode = shadow) builds, pushes and gates, and
// never lands: a passing batch stays passed, its would-land set recorded in
// verdict_json.would_land, and the next batch builds on that candidate (the
// simulated base), the main the queue would have made. In mode on a passing
// batch goes to the [Lander] (M11); only a gate result that is landable
// (passed inside an isolation boundary) ever reaches it.
//
// In mode on a head lands only while the run that approved it holds the
// owner's land grant (internal/pipeline/grants): admission refuses one whose
// approver does not (land_not_granted), and every entry is checked again
// just before the lander's first squash merge, so a grant revoked while the
// batch gated abandons it and its entries wait. Shadow never lands and asks
// for no grant.
//
// The queue.enqueue@1 server action ([EnqueueHandler]) puts an approved PR
// head in the queue, recomputing admission on the server.
//
// The queue is coordination only: DB state, batching and bisect decisions,
// verdicts and scheduling. It holds no repository bytes — no git, no
// checkout, no file contents (owner, 2026-11-27): every code operation (a
// candidate build, a head's changed paths, a gate) is a job on a daemon of
// the repository's gate pool, and the queue sees only the ids, paths and
// verdicts the job reports (internal/mergequeue/buildjob), which it checks
// against what it asked for. internal/architecture's
// TestServerRolesRunNoCode holds every package the server roles reach to
// that.
//
// Nothing constructs a Scheduler on a server yet: the worker started it
// behind pipeline.enabled until it was found building checkouts in the
// worker pod, and it returns once the daemon-side builder exists.
//
//forge:exclude-contract: the Scheduler is driven by the worker, not called through a contract; its seams are the small consumer-side interfaces in deps.go (Store, Gate, Lander, Builder, Refs, Configs, LeaderLock), and it does no I/O of its own
package mergequeue
