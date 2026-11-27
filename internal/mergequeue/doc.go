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
//  2. build: candidate.Build squashes them onto the base, pushing
//     refs/heads/reliant-queue/<batch>;
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
// The queue.enqueue@1 server action ([EnqueueHandler]) puts an approved PR
// head in the queue, recomputing admission on the server.
//
// Nothing constructs a Scheduler yet: it is wired into the worker, behind
// pipeline.enabled, once the gate runner (M9) and the lander (M11) merge.
//
//forge:exclude-contract: the Scheduler is driven by the worker, not called through a contract; its seams are the small consumer-side interfaces in deps.go (Store, Gate, Lander, Workdirs, Configs, LeaderLock), and it does no I/O of its own
package mergequeue
