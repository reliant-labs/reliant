// Package controller provides opinionated runtime helpers for
// controller-runtime reconcilers. It is the operator-flavored sibling
// of forge/pkg/contractkit: a runtime library that the thin shims
// emitted by `forge scaffold crd <name>` delegate to.
//
// The motivation is the same as contractkit. The previous operator
// scaffold emitted ~80 lines per CRD of mostly-mechanical reconciler
// boilerplate (fetch object, NotFound check, finalizer add/remove,
// status update, manager wiring). That logic is the same across every
// CRD a project owns, and once it lives in the project tree it is
// frozen — bumping the implementation needs a re-scaffold of every
// controller. By moving it into a runtime library, the per-CRD
// generated file shrinks to ~30 lines (a struct that embeds the
// generic Reconciler[T], plus the user's domain-logic methods) and the
// shared reconcile lifecycle can be evolved by bumping forge/pkg.
//
// The library carries:
//
//   - Reconciler[T] — the typed base reconciler. It owns fetch /
//     NotFound / finalizer-add / finalizer-cleanup / dispatch-to-user
//     and exposes ReconcileFunc / FinalizeFunc callbacks for the user's
//     domain logic.
//
//   - Result + Done / Requeue / Stop helper constructors over
//     ctrl.Result, so user code reads as `return controller.Requeue(5*time.Second), nil`.
//
//   - Common predicates: SkipDeletion, HasAnnotation, HasLabel,
//     AnnotationChanged. Each is exported so generated SetupWithManager
//     code can compose them without re-implementing.
//
//   - ClusterClientManager — multi-cluster client cache lifted from
//     control-plane-next/operators/workspace_controller. Generic over
//     scheme; safe for concurrent use.
//
//   - Backoff — capped-exponential helper used by reconcilers that
//     track per-object retry counts.
//
//   - IsTransient — classifies errors that are requeued with bounded
//     exponential backoff for Reconciler.TransientGrace before a persistent
//     outage is returned for normal controller-runtime reporting.
//
//   - UpdateStatus — a status write that retries the conflict a reconciler
//     gets when the informer cache trails its own previous write.
//
//   - controllertest — small envtest harness with skip-friendly
//     New() (returns nil + a Skip if envtest binaries are missing) so
//     unit tests are hermetic by default.
//
// Behavioural fingerprints preserved from the workspace-controller
// reference:
//
//   - NotFound on fetch maps to (ctrl.Result{}, nil), not an error.
//   - Finalizer add does an Update + re-fetch to avoid stale
//     resourceVersion.
//   - Finalizer cleanup removes the finalizer with a final Update.
//   - When SetupOptions.SkipDeletion is true, deletion events are
//     filtered out by predicate.
package controller
