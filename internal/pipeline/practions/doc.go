// Copyright (c) 2025 Reliant Labs

// Package practions is the pull-request pipeline's server actions,
// `reliant/pr.<verb>@1` (pipeline build plan M5; redesign §4.2, §4.6, §5,
// §6.1). Each one reads the PR's pipeline state (pr_pipeline and its
// verdicts, attempts and decisions) and its head on GitHub, asks
// internal/pipeline/prpolicy what it means, and writes the answer with a
// compare-and-set on the row's version:
//
//   - pr.next decides what the PR needs next: review, fix, enqueue, close,
//     escalate or none. A low-risk head needs no model: pr.next writes its
//     verdict itself (rule no_model_low_risk or generated_only).
//   - pr.verdict records a reviewer's verdict. It is the only way a model
//     verdict exists, and it writes one only for an engine-owned review node
//     (role: reviewer) of a built-in workflow or of the repository's own
//     file read at its base branch, from a session that never pushed to the
//     PR (§5, G2).
//   - pr.checked records the fixer's scoped check and says fix, push or
//     escalate; pr.pushed records the pushed fix attempt.
//   - pr.close closes a superseded or duplicate PR with its evidence;
//     pr.needs_person hands the PR to a person (an Inbox item and one PR
//     comment, re-paged every two hours).
//
// Every GitHub write goes through one writer (writer.go). In a repository
// whose mode is shadow it writes nothing and records what it would have
// written, in the action's output, the log and a metric; in mode on it
// writes as the Reliant GitHub App's bot. Mode off refuses every action.
//
// Nothing runs unless the server's pipeline.enabled switch is on: the
// dispatcher (internal/serveraction) refuses every call before a handler
// is reached.
//
//forge:exclude-contract: server-action handlers bound into internal/serveraction's dispatcher; every collaborator (Store, GitHub, Inbox) is a consumer-declared interface in deps.go
package practions
