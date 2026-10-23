package controller

import (
	"context"
	"fmt"

	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// UpdateStatus writes obj's status subresource, absorbing optimistic-
// concurrency conflicts.
//
// # Why a plain Status().Update is not enough
//
// A reconciler reads its object through the manager's informer cache, and the
// cache trails the API server. Write status at the end of one reconcile, get
// requeued a few hundred milliseconds later, and the object handed to the
// next reconcile can still carry the resourceVersion from BEFORE that write.
// Its Status().Update is then refused with "the object has been modified" —
// not because anyone else touched the object, but because the reconciler is
// racing its own previous write. A reconciler that requeues quickly (a
// failing reconcile under backoff, a status poll) hits this routinely.
//
// # What a retry does, and why it is safe
//
// On a conflict this re-reads the object's CURRENT resourceVersion and
// retries the same Update with it, after a short backoff that gives the cache
// time to catch up. It does not merge: the status sent is exactly the status
// the caller computed.
//
// That is the right semantics for a status subresource because the reconciler
// that calls this is its only author. The API server ignores everything
// outside .status on a status write, so taking a newer resourceVersion cannot
// carry a stale spec or metadata anywhere; and the status itself was computed
// from what this reconcile observed, with ObservedGeneration saying which
// generation that was. If the spec moved on meanwhile, the generation change
// enqueues another reconcile, which writes the status for the new spec.
//
// A conflict that survives the retries is returned. IsTransient classifies it
// as transient, so a reconciler returning it through Run is requeued rather
// than reported.
//
// An object deleted in the meantime returns the API server's NotFound, which
// callers writing the status of an object that may be going away usually
// wrap in client.IgnoreNotFound.
func UpdateStatus(ctx context.Context, c client.Client, obj client.Object) error {
	attempt := 0
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		attempt++
		if attempt > 1 {
			latest, ok := obj.DeepCopyObject().(client.Object)
			if !ok {
				return fmt.Errorf("controller: refreshing %T for a status retry: its DeepCopyObject "+
					"is not a client.Object", obj)
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(obj), latest); err != nil {
				return err
			}
			obj.SetResourceVersion(latest.GetResourceVersion())
		}
		return c.Status().Update(ctx, obj)
	})
}
