// Copyright (c) 2025 Reliant Labs
package serveraction

// builtinSpecs is every server action Reliant ships, declared here, in one
// static list, so that validation in the API server, the CLI and the worker
// resolves a `uses:` identically without linking any implementation.
//
// It is empty: the framework ships first, inert (pipeline build plan R10).
// The pipeline's actions (pr.*, queue.*, incident.*, release.*, sweep.*,
// recurring.output) are declared here by the tasks that implement them, each
// with its Handler bound on the worker (app.NewTemporalWorker). Every one of
// them runs only while the server's pipeline.enabled switch is on.
func builtinSpecs() []Spec {
	return nil
}
