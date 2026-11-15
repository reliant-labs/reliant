package lifecyclekit_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/reliant-labs/forge/pkg/lifecyclekit"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

type fakeLeader struct {
	names []string
	err   error
	runs  int
}

func (f *fakeLeader) Run(ctx context.Context, name string, fn func(context.Context) error) error {
	f.names = append(f.names, name)
	for range f.runs { // simulate losing and re-winning the lease
		if err := fn(ctx); err != nil {
			return err
		}
	}
	return f.err
}

type plain struct{ starts, stops atomic.Int32 }

func (p *plain) Start(context.Context) error { p.starts.Add(1); return nil }
func (p *plain) Stop(context.Context) error  { p.stops.Add(1); return nil }

type ctxWorker struct {
	plain
	runContexts atomic.Int32
}

func (c *ctxWorker) RunContext(context.Context) error { c.runContexts.Add(1); return nil }

func TestSingletonRunsWorkerOnlyThroughTheLeaderUnderALeaseNamedForIt(t *testing.T) {
	p := &plain{}
	f := &fakeLeader{runs: 2}
	w := lifecyclekit.Singleton(f, lifecyclekit.WrapWorker("sweeper", p))

	cw, ok := w.(serverkit.ContextWorker)
	if !ok {
		t.Fatal("a singleton must be a ContextWorker so the supervisor cancels it per worker")
	}
	if err := cw.RunContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.names; len(got) != 1 || got[0] != "worker/sweeper" {
		t.Fatalf("lease names = %v", got)
	}
	if p.starts.Load() != 2 {
		t.Fatalf("worker started %d times, want 2 (once per lease win)", p.starts.Load())
	}
	if w.Name() != "sweeper" {
		t.Fatalf("Name() = %q", w.Name())
	}
	if err := w.Stop(context.Background()); err != nil || p.stops.Load() != 1 {
		t.Fatalf("Stop err=%v stops=%d", err, p.stops.Load())
	}
}

func TestSingletonPrefersRunContext(t *testing.T) {
	c := &ctxWorker{}
	f := &fakeLeader{runs: 1}
	w := lifecyclekit.Singleton(f, lifecyclekit.WrapWorker("c", c))
	if err := w.(serverkit.ContextWorker).RunContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.runContexts.Load() != 1 || c.starts.Load() != 0 {
		t.Fatalf("RunContext=%d Start=%d", c.runContexts.Load(), c.starts.Load())
	}
}

func TestSingletonPropagatesLeaderError(t *testing.T) {
	boom := errors.New("boom")
	w := lifecyclekit.Singleton(&fakeLeader{err: boom}, lifecyclekit.WrapWorker("x", &plain{}))
	if err := w.(serverkit.ContextWorker).RunContext(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestSingletonWithNilLeaderIsTheWorkerItself(t *testing.T) {
	inner := lifecyclekit.WrapWorker("x", &plain{})
	if got := lifecyclekit.Singleton(nil, inner); got != inner {
		t.Fatal("nil leader must return the worker unchanged")
	}
}

func TestLeaseNameOfDistinguishesLeasedFromParallelWorkers(t *testing.T) {
	inner := lifecyclekit.WrapWorker("sweeper", &plain{})
	if got := lifecyclekit.LeaseNameOf(inner); got != "" {
		t.Fatalf("a plain worker has lease %q", got)
	}
	leased := lifecyclekit.Singleton(&fakeLeader{}, inner)
	if got := lifecyclekit.LeaseNameOf(leased); got != "worker/sweeper" {
		t.Fatalf("LeaseNameOf = %q, want worker/sweeper", got)
	}
}
