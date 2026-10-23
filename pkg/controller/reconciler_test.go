package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const testFinalizer = "forge.dev/test-cleanup"

// We use *corev1.ConfigMap as the test-T because it's a vanilla
// client.Object that the fake client supports out of the box.

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	return s
}

func TestRun_NotFound_ReturnsDoneNoError(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := &Reconciler[*corev1.ConfigMap]{Client: c}

	res, err := r.Run(
		context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Name: "missing", Namespace: "default"}},
		&corev1.ConfigMap{},
		func(ctx context.Context, obj *corev1.ConfigMap) (Result, error) {
			t.Fatal("reconcile should NOT be called for NotFound")
			return Done(), nil
		},
		nil,
	)
	if err != nil {
		t.Errorf("NotFound should not error, got %v", err)
	}
	if res.Requeue || res.RequeueAfter != 0 {
		t.Errorf("NotFound should return Done(), got %+v", res)
	}
}

func TestRun_HappyPath_DispatchesToReconcile(t *testing.T) {
	s := newScheme(t)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cm1", Namespace: "default"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cm).Build()
	r := &Reconciler[*corev1.ConfigMap]{Client: c}

	called := false
	res, err := r.Run(
		context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Name: "cm1", Namespace: "default"}},
		&corev1.ConfigMap{},
		func(ctx context.Context, obj *corev1.ConfigMap) (Result, error) {
			called = true
			if obj.Name != "cm1" {
				t.Errorf("expected fetched obj name=cm1, got %q", obj.Name)
			}
			return Requeue(2 * time.Second), nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("reconcile should have been called")
	}
	if res.RequeueAfter != 2*time.Second {
		t.Errorf("expected RequeueAfter=2s, got %v", res.RequeueAfter)
	}
}

func TestRun_FinalizerAdd_OnFirstReconcile(t *testing.T) {
	s := newScheme(t)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cm1", Namespace: "default"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cm).Build()
	r := &Reconciler[*corev1.ConfigMap]{Client: c, Finalizer: testFinalizer}

	_, err := r.Run(
		context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Name: "cm1", Namespace: "default"}},
		&corev1.ConfigMap{},
		func(ctx context.Context, obj *corev1.ConfigMap) (Result, error) {
			if !controllerutil.ContainsFinalizer(obj, testFinalizer) {
				t.Error("expected obj to carry finalizer when reconcile runs")
			}
			return Done(), nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the finalizer landed in the cluster.
	var got corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cm1", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get cm1: %v", err)
	}
	if !controllerutil.ContainsFinalizer(&got, testFinalizer) {
		t.Errorf("finalizer not added; finalizers=%v", got.Finalizers)
	}
}

func TestRun_FinalizerCleanup_OnDeletion(t *testing.T) {
	s := newScheme(t)
	now := metav1.Now()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cm1",
			Namespace:         "default",
			Finalizers:        []string{testFinalizer},
			DeletionTimestamp: &now,
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cm).Build()
	r := &Reconciler[*corev1.ConfigMap]{Client: c, Finalizer: testFinalizer}

	finalizeCalled := false
	_, err := r.Run(
		context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Name: "cm1", Namespace: "default"}},
		&corev1.ConfigMap{},
		func(ctx context.Context, obj *corev1.ConfigMap) (Result, error) {
			t.Error("reconcile should NOT be called during deletion")
			return Done(), nil
		},
		func(ctx context.Context, obj *corev1.ConfigMap) error {
			finalizeCalled = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !finalizeCalled {
		t.Error("finalize should have been called")
	}

	// The fake client deletes the object once the last finalizer is
	// removed (mimicking real k8s behavior). Confirm via NotFound.
	var got corev1.ConfigMap
	err = c.Get(context.Background(), types.NamespacedName{Name: "cm1", Namespace: "default"}, &got)
	if err == nil || !apierrors.IsNotFound(err) {
		t.Errorf("expected NotFound after finalizer removal, got err=%v finalizers=%v", err, got.Finalizers)
	}
}

func TestRun_FinalizeError_LeavesFinalizer(t *testing.T) {
	s := newScheme(t)
	now := metav1.Now()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cm1",
			Namespace:         "default",
			Finalizers:        []string{testFinalizer},
			DeletionTimestamp: &now,
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cm).Build()
	r := &Reconciler[*corev1.ConfigMap]{Client: c, Finalizer: testFinalizer}

	wantErr := errors.New("finalize boom")
	_, err := r.Run(
		context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Name: "cm1", Namespace: "default"}},
		&corev1.ConfigMap{},
		func(ctx context.Context, obj *corev1.ConfigMap) (Result, error) {
			return Done(), nil
		},
		func(ctx context.Context, obj *corev1.ConfigMap) error {
			return wantErr
		},
	)
	if !errors.Is(err, wantErr) {
		t.Errorf("expected %v, got %v", wantErr, err)
	}

	// Finalizer should still be present on the object.
	var got corev1.ConfigMap
	if err := c.Get(context.Background(), types.NamespacedName{Name: "cm1", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get cm1: %v", err)
	}
	if !controllerutil.ContainsFinalizer(&got, testFinalizer) {
		t.Error("finalizer should remain after finalize error")
	}
}

func TestRun_NoFinalizer_DeletionPathSkipsFinalize(t *testing.T) {
	s := newScheme(t)
	now := metav1.Now()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cm1",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{"someone-elses-finalizer"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cm).Build()
	r := &Reconciler[*corev1.ConfigMap]{Client: c} // no Finalizer set

	res, err := r.Run(
		context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Name: "cm1", Namespace: "default"}},
		&corev1.ConfigMap{},
		func(ctx context.Context, obj *corev1.ConfigMap) (Result, error) {
			t.Error("reconcile should not run on deletion path")
			return Done(), nil
		},
		func(ctx context.Context, obj *corev1.ConfigMap) error {
			t.Error("finalize should not run when r.Finalizer is empty")
			return nil
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Requeue || res.RequeueAfter != 0 {
		t.Errorf("expected Done() on deletion-without-finalizer path, got %+v", res)
	}
}

func TestRun_ReconcileError_Surfaced(t *testing.T) {
	s := newScheme(t)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cm1", Namespace: "default"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cm).Build()
	r := &Reconciler[*corev1.ConfigMap]{Client: c}

	wantErr := errors.New("reconcile boom")
	_, err := r.Run(
		context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Name: "cm1", Namespace: "default"}},
		&corev1.ConfigMap{},
		func(ctx context.Context, obj *corev1.ConfigMap) (Result, error) {
			return Done(), wantErr
		},
		nil,
	)
	if !errors.Is(err, wantErr) {
		t.Errorf("expected %v, got %v", wantErr, err)
	}
}

// fakeRecorder captures events for assertions.
type fakeRecorder struct {
	events []string
}

func (r *fakeRecorder) Event(_ runtime.Object, _, reason, _ string) {
	r.events = append(r.events, reason)
}
func (r *fakeRecorder) Eventf(_ runtime.Object, _, _, _ string, _ ...interface{}) {}
func (r *fakeRecorder) AnnotatedEventf(_ runtime.Object, _ map[string]string, _, _, _ string, _ ...interface{}) {
}

func TestRun_RecorderEmitsReconcileEvents(t *testing.T) {
	s := newScheme(t)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cm1", Namespace: "default"},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(cm).Build()
	rec := &fakeRecorder{}
	r := &Reconciler[*corev1.ConfigMap]{Client: c, Recorder: rec}

	_, err := r.Run(
		context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Name: "cm1", Namespace: "default"}},
		&corev1.ConfigMap{},
		func(ctx context.Context, obj *corev1.ConfigMap) (Result, error) {
			return Done(), nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.events) < 2 {
		t.Errorf("expected at least 2 events (Reconciling + Reconciled), got %v", rec.events)
	}
}

// Ensure the type parameter wiring compiles for a non-pointer-typed-nil
// case via a generic helper (compile-time test only).
var _ = func() {
	var _ ReconcileFunc[*corev1.ConfigMap] = func(context.Context, *corev1.ConfigMap) (Result, error) {
		return Done(), nil
	}
	var _ FinalizeFunc[*corev1.ConfigMap] = func(context.Context, *corev1.ConfigMap) error {
		return nil
	}
	var _ client.Object = &corev1.ConfigMap{}
}
