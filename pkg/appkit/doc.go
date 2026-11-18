// Package appkit holds the worker/operator supervised-component
// contracts that the generated internal/app composition layer re-uses.
//
// # Status (FORGE_SHAPE_REDESIGN §2)
//
// appkit was once the runtime DI engine behind a generated
// pkg/app/bootstrap.go table (a string-keyed Def/ServiceDef/Run that
// constructed, wired, and mounted every component). That engine is
// RETIRED. The live composition path is the generated cmd/server.go over
// the internal/app layer (OpenInfra → Build → PostBuild → mount via
// Inventory → serverkit.Run); construction is typed and owned, not
// string-keyed and reflected.
//
// # Moved to lifecyclekit (lib-boundary extraction)
//
// The worker lifecycle machinery — WrapWorker, WorkerInstance /
// ContextWorkerInstance, WorkerLifecycle — moved to
// [github.com/reliant-labs/forge/pkg/lifecyclekit], which also owns the
// operator/controller-manager bridge the generated RunOperators delegates to.
// The generated internal/app/lifecycle_gen.go now imports lifecyclekit
// directly. appkit keeps DEPRECATED aliases that delegate to lifecyclekit so
// any remaining consumer of the appkit names still compiles against a single
// implementation. New code should import lifecyclekit.
//
//   - [WorkerInstance] / [ContextWorkerInstance] — aliases of the
//     lifecyclekit lifecycle wrappers.
//   - [WrapWorker] — delegates to lifecyclekit.WrapWorker, the runtime
//     type-switch that gives RunContext-implementing workers the ctx-aware
//     wrapper and everything else the legacy Start wrapper.
//   - [WorkerLifecycle] — alias of lifecyclekit.WorkerLifecycle.
//
// operatorkit (the controller-manager runtime) remains a subpackage here.
package appkit
