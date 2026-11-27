// Copyright (c) 2025 Reliant Labs

// Package buildjob is the wire contract of the merge queue's code
// operations: what the queue asks of a gate pool daemon, and what the daemon
// reports back. The queue (internal/mergequeue) runs on the worker and holds
// no repository bytes: no git, no checkout, no file contents (owner,
// 2026-11-27). Every operation that reads or writes a repository's code is a
// job on a daemon of the repository's gate pool, and the queue sees only the
// commit and tree ids, paths and verdicts the job reports.
//
// This package is plain data, so both sides import it: the worker's
// scheduler, and the daemon-side builder (internal/mergequeue/candidate).
package buildjob

// Entry is one queued PR at the head that was reviewed. In a result's
// Included it is the head that lands: the regen commit's, for a regenerated
// entry.
type Entry struct {
	Number int    `json:"number"`
	Head   string `json:"head"`
	Title  string `json:"title,omitempty"`
}

// BuildRequest asks for one batch's candidate: entries squashed in order onto
// Base, pushed to the batch's queue ref.
type BuildRequest struct {
	// Repository is owner/name.
	Repository string `json:"repository"`
	// BatchID names the batch: its candidate ref and the Reliant-Batch
	// trailer.
	BatchID string `json:"batch_id"`
	// Base is M, the commit the candidate starts from: main's tip as the
	// server read it from GitHub, or in shadow the would-be main.
	Base    string  `json:"base"`
	Entries []Entry `json:"entries"`
	// Generated and Regen are the repository's pipeline.yaml `generated:`
	// globs and `regen:` command.
	Generated []string `json:"generated,omitempty"`
	Regen     string   `json:"regen,omitempty"`
	// Shadow builds without writing to any PR: only the candidate ref is
	// pushed, and every other push (a regen commit) is reported in
	// WouldPush instead of made.
	Shadow bool `json:"shadow,omitempty"`
}

// Reason is why an entry left the batch.
type Reason string

const (
	// ReasonConflict: the head conflicts with M, in paths regen may not
	// settle (or the repository declares no regen). The PR needs a fix.
	ReasonConflict Reason = "conflict"
	// ReasonBatchConflict: the head applies to M but conflicts with an
	// earlier entry of this batch. It waits for the next batch.
	ReasonBatchConflict Reason = "batch_conflict"
	// ReasonRegenFailed: regen ran and failed (or was refused by the
	// sandbox, or timed out).
	ReasonRegenFailed Reason = "regen_failed"
	// ReasonNotGeneratedOnly: regen changed paths outside `generated:`.
	ReasonNotGeneratedOnly Reason = "not_generated_only"
	// ReasonHeadMoved: the PR is closed, or its head moved, so the regen
	// commit was not pushed.
	ReasonHeadMoved Reason = "head_moved"
)

// Ejection is an entry left out of the candidate.
type Ejection struct {
	Entry  Entry  `json:"entry"`
	Reason Reason `json:"reason"`
	// Paths are the conflicting paths, or for ReasonNotGeneratedOnly the
	// non-generated paths regen changed.
	Paths []string `json:"paths,omitempty"`
	// Detail is a human-readable note: regen's output tail, for one.
	Detail string `json:"detail,omitempty"`
}

// Regenerated is an entry kept by regenerating it: its new head is on the
// PR's branch (or, in shadow, would be).
type Regenerated struct {
	Entry     Entry    `json:"entry"`
	NewHead   string   `json:"new_head"`
	Base      string   `json:"base"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// Push is a push a shadow build did not make.
type Push struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

// BuildResult is a built candidate.
type BuildResult struct {
	Base string `json:"base"`
	// Commit is the candidate's tip, pushed to Ref. Empty when every entry
	// was ejected (nothing is pushed then).
	Commit string `json:"commit,omitempty"`
	Ref    string `json:"ref,omitempty"`
	// Trees are T_1..T_n: Trees[i] is the tree after squashing
	// Included[0..i] onto Base. The lander checks main's tree against them
	// through the GitHub API after each squash merge.
	Trees       []string      `json:"trees,omitempty"`
	Included    []Entry       `json:"included,omitempty"`
	Ejections   []Ejection    `json:"ejections,omitempty"`
	Regenerated []Regenerated `json:"regenerated,omitempty"`
	WouldPush   []Push        `json:"would_push,omitempty"`
}

// PathsRequest asks for the paths Head changes against Base: the diff from
// their merge base, rename detection off.
type PathsRequest struct {
	Repository string `json:"repository"`
	Base       string `json:"base"`
	Head       string `json:"head"`
}

// PathsResult is the changed paths, sorted and unique.
type PathsResult struct {
	Paths []string `json:"paths"`
}
