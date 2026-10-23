package controller

import (
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
)

// Result is a thin alias of ctrl.Result. The library exports it so
// user code never has to import sigs.k8s.io/controller-runtime/pkg/reconcile
// directly — the helpers (Done, Requeue, Stop) are the canonical
// constructors and they all return Result.
type Result = ctrl.Result

// Done returns the success / no-requeue Result. Equivalent to
// ctrl.Result{}.
func Done() Result {
	return ctrl.Result{}
}

// Requeue returns a Result that asks controller-runtime to re-enqueue
// the object after `after`. A zero or negative `after` becomes
// Requeue: true (the controller-runtime convention for "as soon as
// possible").
func Requeue(after time.Duration) Result {
	if after <= 0 {
		return ctrl.Result{Requeue: true}
	}
	return ctrl.Result{RequeueAfter: after}
}

// Stop returns a Result that signals the controller should NOT
// re-enqueue the object — semantically identical to Done(), but
// reads more clearly at the call site when the reconciler is
// deliberately giving up (e.g., because the object is in a
// terminal failed state and further reconciles would be no-ops).
func Stop() Result {
	return ctrl.Result{}
}
