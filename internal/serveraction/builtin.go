// Copyright (c) 2025 Reliant Labs
package serveraction

// builtinSpecs is every server action Reliant ships, declared here, in one
// static list, so that validation in the API server, the CLI and the worker
// resolves a `uses:` identically without linking any implementation.
//
// The pipeline's actions are declared here by the tasks that implement them,
// each with its Handler bound on the worker (app.WorkerServerActions). Every
// one of them runs only while the server's pipeline.enabled switch is on.
func builtinSpecs() []Spec {
	return append(append(prSpecs(), queueSpecs()...), sweepSpecs()...)
}

// SweepPR is the daily PR sweep (pipeline build plan M13;
// internal/pipeline/practions.Sweep).
const SweepPR = "reliant/sweep.pr@1"

// sweepChecks mirrors practions.SweepChecks (TestSweepSpecNamesEveryCheck).
var sweepChecks = []any{"superseded", "stuck_in_queue", "stale_needs_person", "draft_abandoned", "stale_pr"}

func sweepSpecs() []Spec {
	return []Spec{{
		Name: SweepPR,
		Description: "Find the run's project's open PRs that fell through the cracks and act on each once: " +
			"wake the pr workflow, close it with cited evidence, or ask a person. Shadow mode records its GitHub writes only.",
		Params: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"checks": map[string]any{
					"type": "array", "items": map[string]any{"type": "string", "enum": sweepChecks},
					"description": "The checks to run; empty or absent runs every one.",
				},
			},
		},
		Mutates: true,
	}}
}

// QueueEnqueue is the queue.enqueue action (pipeline build plan M7;
// internal/mergequeue.EnqueueHandler).
const QueueEnqueue = "reliant/queue.enqueue@1"

func queueSpecs() []Spec {
	return []Spec{{
		Name:        QueueEnqueue,
		Description: "Put the PR's current head in the merge queue, if the server finds it admissible",
		Params: map[string]any{
			"type":                 "object",
			"required":             []any{"record_id"},
			"additionalProperties": false,
			"properties": map[string]any{
				"record_id": map[string]any{"type": "string", "minLength": 1},
				"head":      map[string]any{"type": "string"},
			},
		},
		Mutates: true,
	}}
}

// The pr.* actions (pipeline build plan M5; internal/pipeline/practions).
// Each takes the PR's record id, which a pr_pipeline trigger renders as
// `{{ trigger.record.id }}`.
const (
	PRNext        = "reliant/pr.next@1"
	PRVerdict     = "reliant/pr.verdict@1"
	PRChecked     = "reliant/pr.checked@1"
	PRPushed      = "reliant/pr.pushed@1"
	PRClose       = "reliant/pr.close@1"
	PRNeedsPerson = "reliant/pr.needs_person@1"
)

// withLocator makes an object schema name its PR either way: record_id, or
// repository and number.
func withLocator(schema map[string]any) map[string]any {
	delete(schema, "required")
	schema["anyOf"] = []any{
		map[string]any{"required": []any{"record_id"}},
		map[string]any{"required": []any{"repository", "number"}},
	}
	return schema
}

func prSpecs() []Spec {
	recordID := map[string]any{"type": "string", "minLength": 1,
		"description": "The PR's record id (pr_records.id): trigger.record.id."}
	from := func(what string) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "description": what}
	}
	object := func(required []string, props map[string]any) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": props}
	}
	return []Spec{
		{
			Name: PRNext,
			Description: "Decide what a pull request needs next (review, fix, enqueue, close, escalate or none) " +
				"from its pipeline state and its head on GitHub; a low-risk head passes here with no model.",
			Params: withLocator(object(nil, map[string]any{
				"record_id": recordID,
				"repository": map[string]any{"type": "string", "minLength": 1,
					"description": "The PR's repository as owner/name, with number, when the record id is not known (a GitHub firing): trigger.record.repository."},
				"number": map[string]any{"type": "integer", "minimum": 1, "description": "The PR's number, with repository: trigger.record.number."},
				"comment": map[string]any{
					"type": "object", "description": "The review or issue comment that woke the run, if one did: it becomes a blocker for the fixer.",
					"properties": map[string]any{
						"id":     map[string]any{"type": []any{"string", "integer"}},
						"body":   map[string]any{"type": "string"},
						"author": map[string]any{"type": "string"},
						"path":   map[string]any{"type": "string"},
						"line":   map[string]any{"type": []any{"integer", "null"}},
					},
				},
			})),
			Mutates: true,
		},
		{
			Name: PRVerdict,
			Description: "Record a reviewer's verdict on the PR's head. Only an engine-owned review node " +
				"(role: reviewer) of a built-in workflow, or of the repository's file at its base branch, may write one.",
			Params: object([]string{"record_id", "from"}, map[string]any{
				"record_id": recordID,
				"from":      from("The id of the review node whose verdict to record."),
			}),
			Mutates: true,
		},
		{
			Name:        PRChecked,
			Description: "Record the fixer's scoped check (lint, affected tests, build) and say whether to fix again, push or escalate.",
			Params: object([]string{"record_id", "from"}, map[string]any{
				"record_id": recordID,
				"from":      from("The id of the run node that ran the check."),
			}),
			Mutates: true,
		},
		{
			Name:        PRPushed,
			Description: "Record that the fixer's attempt was pushed; the PR is decided again on its new head.",
			Params: object([]string{"record_id"}, map[string]any{
				"record_id": recordID,
				"from":      from("The id of the node that pushed, when its output names the pushed head."),
			}),
			Mutates: true,
		},
		{
			Name:        PRClose,
			Description: "Close the PR with a comment citing the evidence (a superseded or duplicate PR). Shadow mode records it only.",
			Params: object([]string{"record_id", "evidence"}, map[string]any{
				"record_id": recordID,
				"evidence":  map[string]any{"description": "What shows the PR should close: text, or pr.next's evidence."},
			}),
			Mutates: true,
		},
		{
			Name:        PRNeedsPerson,
			Description: "Hand the PR to a person: an Inbox item and one PR comment, re-paged every 2 hours until someone acts.",
			Params: object([]string{"record_id", "reason"}, map[string]any{
				"record_id": recordID,
				"reason":    map[string]any{"type": "string", "minLength": 1, "description": "Why a person is needed, in plain words."},
			}),
			Mutates: true,
		},
	}
}
