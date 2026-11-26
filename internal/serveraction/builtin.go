// Copyright (c) 2025 Reliant Labs
package serveraction

// builtinSpecs is every server action Reliant ships, declared here, in one
// static list, so that validation in the API server, the CLI and the worker
// resolves a `uses:` identically without linking any implementation.
//
// The framework shipped first, inert (pipeline build plan R10). The
// pipeline's actions (pr.*, queue.*, incident.*, release.*, sweep.*,
// recurring.output) are declared here by the tasks that implement them, each
// with its Handler bound on the worker (app.NewTemporalWorker); one with no
// handler bound yet is refused as unavailable. Every one of them runs only
// while the server's pipeline.enabled switch is on.
func builtinSpecs() []Spec {
	return []Spec{
		{
			// internal/mergequeue.EnqueueHandler (MVP task M7).
			Name:        "reliant/queue.enqueue@1",
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
		},
	}
}
