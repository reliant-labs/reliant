// Package prpolicy is the pipeline's pull-request policy: what `pr.next@1`
// and `pr.verdict@1` decide about a PR, as pure functions over data the
// caller has already read (redesign §4.2, §6.1, §10.1). Nothing here reads a
// database, GitHub, git or the clock; the actions that call it (pipeline
// build plan task R11) read the state, ask this package what it means and
// write the answer.
//
// # Part 1: review and fix policy
//
//   - Risk ([AssessRisk], [ClassifyRisk]): a PR's risk tier, low, medium or
//     high, decided from its changed files alone: their paths, kinds and
//     size. A change that is only documentation, or only generated files the
//     gate regenerates, is low and needs no model (rule no_model_low_risk,
//     or generated_only). Migrations, protobuf, auth and credential code, CI
//     configuration, `.reliant/**` and go.mod are high. Every tier comes
//     with reasons in plain words, which the PR's status shows as they are.
//   - Convergence ([Converge]): a later review round blocks only on a prior
//     blocker that is still there (matched by its stable id) or on a blocker
//     in code changed since the prior round's head. A blocker raised for the
//     first time on unchanged code is a late finding, recorded as a nit, so
//     a review converges instead of finding something new every round.
//   - Signatures ([FailureSignature], [FindingsSignature]): a stable hash of
//     what a fix is trying to clear, the failing checks and tests or the
//     blocking findings, normalized so that timestamps, durations, temp
//     paths, addresses and line-number churn do not make one failure look
//     like another.
//   - Attempt caps ([NextAttempt], [ReviewRoundsExhausted]): fix attempts are
//     counted per signature and start again at a new one; past the cap the
//     PR needs a person.
//
// # Part 2: queue-facing policy
//
// Superseded and duplicate PR detection, the owner decisions a head
// requires (from pipeline.yaml `owner_approval`), emergency-land
// eligibility (§3.8) and priority (its sources and aging) live in the same
// package, in their own files.
//
// Thresholds that are package constants here (the risk tier's size limits
// and path rules) are expected to become pipeline.yaml knobs; until then a
// repository changes them only through this package.
//
// Nothing imports this package yet: R11's actions are its first reader.
//
//forge:exclude-contract: pure policy functions over caller-supplied data; no I/O, state or time
package prpolicy
