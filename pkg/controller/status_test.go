package controller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// A Pod rather than the ConfigMap the other tests use, because the property
// under test is a STATUS-subresource write and a ConfigMap has no status.
func statusClient(t *testing.T, funcs *interceptor.Funcs) (client.Client, *corev1.Pod) {
	t.Helper()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"}}
	b := fake.NewClientBuilder().WithScheme(newScheme(t)).
		WithObjects(pod).WithStatusSubresource(&corev1.Pod{})
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	c := b.Build()
	var live corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pod), &live); err != nil {
		t.Fatalf("get: %v", err)
	}
	return c, &live
}

// TestUpdateStatus_AStaleReadIsNotAConflict is ELECTRON-B9: a reconcile
// handed an object one status write behind (the informer cache trailing the
// reconciler's own previous write) had its Status().Update refused with "the
// object has been modified", and that refusal was logged at ERROR. The status
// this reconciler computed is the status that should land.
func TestUpdateStatus_AStaleReadIsNotAConflict(t *testing.T) {
	ctx := context.Background()
	c, stale := statusClient(t, nil)

	// The reconciler's previous write, which the cache has not caught up
	// with: the server's resourceVersion moves past the one `stale` holds.
	previous := stale.DeepCopy()
	previous.Status.Message = "previous reconcile"
	if err := c.Status().Update(ctx, previous); err != nil {
		t.Fatalf("previous status write: %v", err)
	}

	// The control: a plain Status().Update of the stale copy conflicts,
	// so the fake enforces resourceVersion and the case below is real.
	control := stale.DeepCopy()
	control.Status.Message = "control"
	if err := c.Status().Update(ctx, control); !apierrors.IsConflict(err) {
		t.Fatalf("a plain Status().Update of a stale object returned %v, want a conflict; "+
			"without one this test proves nothing", err)
	}

	stale.Status.Message = "this reconcile"
	if err := UpdateStatus(ctx, c, stale); err != nil {
		t.Fatalf("UpdateStatus on a stale object: %v", err)
	}
	var got corev1.Pod
	if err := c.Get(ctx, client.ObjectKeyFromObject(stale), &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Message != "this reconcile" {
		t.Errorf("status.message = %q, want the status this reconcile computed", got.Status.Message)
	}
}

// TestUpdateStatus_APersistentConflictIsReturnedAsTransient: a conflict the
// retries cannot clear is returned, and is the kind Run requeues rather than
// reports.
func TestUpdateStatus_APersistentConflictIsReturnedAsTransient(t *testing.T) {
	attempts := 0
	c, pod := statusClient(t, &interceptor.Funcs{
		SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
			attempts++
			return apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, "p",
				errors.New("the object has been modified"))
		},
	})

	err := UpdateStatus(context.Background(), c, pod)
	if !apierrors.IsConflict(err) {
		t.Fatalf("UpdateStatus = %v, want the conflict", err)
	}
	if attempts < 2 {
		t.Errorf("Status().Update was attempted %d times; a conflict must be retried", attempts)
	}
	if !IsTransient(err) {
		t.Error("the surviving conflict is not transient, so Run would report it")
	}
}

// TestUpdateStatus_OtherErrorsAreNotRetried: only a conflict is a reason to
// re-read and try again. A refusal for any other reason is returned at once.
func TestUpdateStatus_OtherErrorsAreNotRetried(t *testing.T) {
	attempts := 0
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "p", errors.New("the service account may not update pods/status"))
	c, pod := statusClient(t, &interceptor.Funcs{
		SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
			attempts++
			return forbidden
		},
	})

	if err := UpdateStatus(context.Background(), c, pod); !apierrors.IsForbidden(err) {
		t.Fatalf("UpdateStatus = %v, want the forbidden error", err)
	}
	if attempts != 1 {
		t.Errorf("a forbidden write was attempted %d times, want 1", attempts)
	}
}

// TestUpdateStatus_ADeletedObjectIsNotFound: an object gone between the
// conflict and the re-read reports NotFound, which callers ignore.
func TestUpdateStatus_ADeletedObjectIsNotFound(t *testing.T) {
	ctx := context.Background()
	c, stale := statusClient(t, nil)
	if err := c.Delete(ctx, stale.DeepCopy()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := client.IgnoreNotFound(UpdateStatus(ctx, c, stale)); err != nil {
		t.Fatalf("UpdateStatus on a deleted object = %v, want NotFound", err)
	}
}
