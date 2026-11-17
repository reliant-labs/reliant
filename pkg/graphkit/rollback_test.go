package graphkit

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/lifecyclekit"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

func acquire(b *Builder, m *Memo[int], name string, close func(context.Context) error) {
	Get(b, m, name, func() (int, error) { return 1, nil }, func(int) lifecyclekit.Node {
		return lifecyclekit.Node{Close: close,
			Run:   func(context.Context) error { panic("rollback must not run") },
			Drain: func(context.Context) error { panic("rollback must not drain") }}
	})
}

func TestBuildWithRollbackFailure(t *testing.T) {
	var b Builder
	var a, c, fail Memo[int]
	original, cleanup := errors.New("constructor failed"), errors.New("close failed")
	var order []string
	err := b.BuildWithRollback(context.Background(), "api", time.Second, func() {
		acquire(&b, &a, "db", func(ctx context.Context) error { order = append(order, "db"); return nil })
		acquire(&b, &c, "client", func(ctx context.Context) error { order = append(order, "client"); return cleanup })
		Get(&b, &fail, "bad", func() (int, error) { return 0, original }, nil)
	})
	if !errors.Is(err, original) || !errors.Is(err, cleanup) {
		t.Fatalf("lost error chain: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"client", "db"}) {
		t.Fatalf("close order: %v", order)
	}
	if b.BuildWithRollback(context.Background(), "retry", time.Second, func() {}) == nil {
		t.Fatal("rolled-back graph reused")
	}
	if b.Build("retry", func() {}) == nil {
		t.Fatal("legacy Build reused rolled-back graph")
	}
	if b.Supervise(context.Background(), lifecyclekit.Policy{}, nil) == nil {
		t.Fatal("closed resources supervised")
	}
	if len(order) != 2 {
		t.Fatal("rollback repeated close")
	}
	defer func() {
		if recover() == nil {
			t.Error("closed memo remained accessible")
		}
	}()
	Get(&b, &a, "db", func() (int, error) { return 2, nil }, nil)
}

func TestBuildWithRollbackSharedMemoAndForcePanic(t *testing.T) {
	var b Builder
	var a Memo[int]
	closes := 0
	original := errors.New("force panic")
	get := func() { acquire(&b, &a, "db", func(context.Context) error { closes++; return nil }) }
	err := b.BuildWithRollback(context.Background(), "api", time.Second, func() {
		get()
		get() // two consumers of the same resource are one acquisition
		panic(original)
	})
	if !errors.Is(err, original) || closes != 1 {
		t.Fatalf("panic rollback: %v, closes=%d", err, closes)
	}
}

func TestBuildWithRollbackCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var b Builder
	var a Memo[int]
	closed := false
	err := b.BuildWithRollback(ctx, "api", time.Second, func() {
		acquire(&b, &a, "db", func(cleanup context.Context) error {
			closed = true
			if cleanup.Err() != nil {
				t.Error("cleanup inherited canceled construction context")
			}
			if _, ok := cleanup.Deadline(); !ok {
				t.Error("cleanup unbounded")
			}
			return nil
		})
		cancel()
	})
	if !errors.Is(err, context.Canceled) || !closed {
		t.Fatalf("cancellation cleanup: %v, closed=%v", err, closed)
	}
}

func TestBuildWithRollbackBudgetAndPanic(t *testing.T) {
	var b Builder
	var a, c, fail Memo[int]
	closed := false
	err := b.BuildWithRollback(context.Background(), "api", time.Millisecond, func() {
		acquire(&b, &a, "db", func(ctx context.Context) error { closed = true; return ctx.Err() })
		acquire(&b, &c, "client", func(ctx context.Context) error { <-ctx.Done(); panic("broken closer") })
		Get(&b, &fail, "bad", func() (int, error) { return 0, errors.New("bad") }, nil)
	})
	if !closed || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup failed to continue after closer panic/budget: %v", err)
	}
}

func TestBuildWithRollbackTransfersOwnership(t *testing.T) {
	var b Builder
	var a Memo[int]
	closes := 0
	if err := b.BuildWithRollback(context.Background(), "api", time.Second, func() {
		Get(&b, &a, "db", func() (int, error) { return 1, nil }, func(int) lifecyclekit.Node {
			return lifecyclekit.Node{Close: func(context.Context) error { closes++; return nil }}
		})
	}); err != nil {
		t.Fatal(err)
	}
	if closes != 0 {
		t.Fatal("successful Build closed acquired resource")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	policy := lifecyclekit.Policy{Drainer: &serverkit.Drainer{PreStopDelay: -1, ShutdownTimeout: time.Second}}
	if err := b.Supervise(ctx, policy, nil); err != nil {
		t.Fatal(err)
	}
	if closes != 1 {
		t.Fatalf("normal shutdown closes=%d", closes)
	}
	if err := b.Rollback(time.Second); err != nil {
		t.Fatal(err)
	}
	if b.Supervise(ctx, policy, nil) == nil {
		t.Fatal("supervision ownership transferred twice")
	}
	if b.BuildWithRollback(ctx, "again", time.Second, func() {}) == nil {
		t.Fatal("rebuild succeeded")
	}
	if closes != 1 {
		t.Fatal("resource double closed")
	}
}

func TestRejectedSupervisionKeepsRollbackOwnership(t *testing.T) {
	var b Builder
	var a Memo[int]
	closes := 0
	if err := b.BuildWithRollback(context.Background(), "api", time.Second, func() {
		acquire(&b, &a, "db", func(context.Context) error { closes++; return nil })
	}); err != nil {
		t.Fatal(err)
	}
	// Invalid external node must not consume the transfer or leak acquisitions.
	if err := b.Supervise(context.Background(), lifecyclekit.Policy{}, nil, lifecyclekit.Node{}); err == nil {
		t.Fatal("invalid supervision accepted")
	}
	if err := b.Rollback(time.Second); err != nil {
		t.Fatal(err)
	}
	if closes != 1 {
		t.Fatalf("closes=%d", closes)
	}
}

// A plain Build graph whose node set Supervise rejects has not transferred
// ownership either: the rejection is returned and Rollback still releases.
func TestRejectedSupervisionOfPlainBuildKeepsRollbackOwnership(t *testing.T) {
	var b Builder
	var a Memo[int]
	closes := 0
	if err := b.Build("api", func() {
		acquire(&b, &a, "db", func(context.Context) error { closes++; return nil })
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.Supervise(context.Background(), lifecyclekit.Policy{}, nil, lifecyclekit.Node{}); err == nil {
		t.Fatal("invalid supervision accepted")
	}
	if b.supervised {
		t.Fatal("rejected supervision took ownership")
	}
	if closes != 0 {
		t.Fatalf("rejected supervision closed resources: closes=%d", closes)
	}
	if err := b.Rollback(time.Second); err != nil {
		t.Fatal(err)
	}
	if closes != 1 {
		t.Fatalf("closes=%d", closes)
	}
}

func TestRollbackAfterBuildBeforeSupervise(t *testing.T) {
	var b Builder
	var a Memo[int]
	closes := 0
	if err := b.BuildWithRollback(context.Background(), "api", time.Second, func() {
		acquire(&b, &a, "db", func(context.Context) error { closes++; return nil })
	}); err != nil {
		t.Fatal(err)
	}
	// A later external listener construction failed; no node was started.
	if err := b.Rollback(time.Second); err != nil {
		t.Fatal(err)
	}
	if err := b.Rollback(time.Second); err != nil {
		t.Fatal(err)
	}
	if closes != 1 {
		t.Fatalf("closes=%d", closes)
	}
}

func TestBuildWithRollbackRejectsInvalidBudgetAndPreCanceled(t *testing.T) {
	var b Builder
	called := false
	if b.BuildWithRollback(context.Background(), "api", 0, func() { called = true }) == nil || called {
		t.Fatal("invalid budget accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.BuildWithRollback(ctx, "api", time.Second, func() { called = true }); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("pre-canceled build: %v", err)
	}
}
