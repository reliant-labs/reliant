package cli

// The command-scoped declaration memo (kcl_render_scope.go): one evaluation
// per env per command, envs evaluated concurrently, and a caller's
// cancellation never cached as the declaration's answer.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestDeclarationMemo_EvaluatesEachEnvOnce(t *testing.T) {
	ctx := withDeclarationMemo(context.Background())
	m := declarationMemoFrom(ctx)
	var evals atomic.Int64
	eval := func(context.Context) (*KCLEntities, error) {
		evals.Add(1)
		return &KCLEntities{Env: "prod"}, nil
	}
	var wg sync.WaitGroup
	got := make([]*KCLEntities, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], _ = m.render(ctx, t.TempDir()+"/../same", "prod", eval)
		}(i)
	}
	wg.Wait()
	// Different spellings of one directory are one key.
	dir := t.TempDir()
	first, _ := m.render(ctx, dir, "dev", eval)
	again, _ := m.render(ctx, filepath.Join(dir, "x", ".."), "dev", eval)
	if n := evals.Load(); n != 2 {
		t.Fatalf("8 concurrent renders of one env and 2 of another evaluated %d times, want 2 (one per env)", n)
	}
	for _, e := range got[1:] {
		if e != got[0] {
			t.Fatal("callers of one env must share the one evaluation's result")
		}
	}
	if first != again {
		t.Fatal("a cleaned path must hit the same entry")
	}
}

// One env's evaluation must not wait on another's: the old resolver memo held
// one lock across every render, which serialized a four-env status into four
// renders end to end.
func TestDeclarationMemo_EvaluatesDifferentEnvsConcurrently(t *testing.T) {
	ctx := withDeclarationMemo(context.Background())
	m := declarationMemoFrom(ctx)
	dir := t.TempDir()
	var arrived sync.WaitGroup
	arrived.Add(2)
	both := make(chan struct{})
	go func() { arrived.Wait(); close(both) }()
	eval := func(context.Context) (*KCLEntities, error) {
		arrived.Done()
		select {
		case <-both:
			return &KCLEntities{}, nil
		case <-time.After(10 * time.Second):
			return nil, errors.New("the other env's evaluation never started: renders are serialized")
		}
	}
	errs := make(chan error, 2)
	for _, env := range []string{"dev", "prod"} {
		go func(env string) {
			_, err := m.render(ctx, dir, env, eval)
			errs <- err
		}(env)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeclarationMemo_SharesADeclarationErrorButNotACancellation(t *testing.T) {
	ctx := withDeclarationMemo(context.Background())
	m := declarationMemoFrom(ctx)
	dir := t.TempDir()
	var evals atomic.Int64

	broken := errors.New("CompileError")
	for range 2 {
		if _, err := m.render(ctx, dir, "prod", func(context.Context) (*KCLEntities, error) {
			evals.Add(1)
			return nil, broken
		}); !errors.Is(err, broken) {
			t.Fatalf("got %v, want the declaration's own error", err)
		}
	}
	if n := evals.Load(); n != 1 {
		t.Fatalf("a declaration that fails to evaluate is the answer for the whole command; evaluated %d times, want 1", n)
	}

	evals.Store(0)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.render(cancelled, dir, "dev", func(ctx context.Context) (*KCLEntities, error) {
		evals.Add(1)
		return nil, ctx.Err()
	}); err == nil {
		t.Fatal("a cancelled caller must see its cancellation")
	}
	e, err := m.render(ctx, dir, "dev", func(context.Context) (*KCLEntities, error) {
		evals.Add(1)
		return &KCLEntities{Env: "dev"}, nil
	})
	if err != nil || e == nil || e.Env != "dev" {
		t.Fatalf("after another caller's cancellation the next caller must evaluate afresh; got %v, %v", e, err)
	}
}

func TestDeclarationMemo_BoundsConcurrentEvaluations(t *testing.T) {
	ctx := withDeclarationMemo(context.Background())
	m := declarationMemoFrom(ctx)
	dir := t.TempDir()
	var running, peak atomic.Int64
	eval := func(context.Context) (*KCLEntities, error) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		running.Add(-1)
		return &KCLEntities{}, nil
	}
	var wg sync.WaitGroup
	for i := range 3 * declarationRenderParallelism {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = m.render(ctx, dir, string(rune('a'+i)), eval)
		}(i)
	}
	wg.Wait()
	if p := peak.Load(); p > declarationRenderParallelism {
		t.Fatalf("%d evaluations ran at once, want at most %d", p, declarationRenderParallelism)
	}
}

// RenderKCL outside a scope is unchanged: every call evaluates.
func TestRenderKCL_OutsideAScopeEvaluatesEveryCall(t *testing.T) {
	var raws atomic.Int64
	stubRenderKCLRaw(t, &raws)
	dir := t.TempDir()
	for range 2 {
		if _, err := RenderKCL(context.Background(), dir, "prod"); err != nil {
			t.Fatal(err)
		}
	}
	if n := raws.Load(); n != 2 {
		t.Fatalf("outside a declaration memo RenderKCL evaluated %d times for 2 calls, want 2", n)
	}
}

// The all-environments view evaluates each env's declaration ONCE. It used
// to make three evaluations per env (lifecycle, destination, ledger) and six
// for a bound hosted env — 37s on control-plane's four envs.
func TestEnvTopology_EvaluatesEachDeclaredEnvOnce(t *testing.T) {
	var raws atomic.Int64
	stubRenderKCLRaw(t, &raws)
	dir := t.TempDir()
	ensureLedgerReady(t, dir)
	for _, env := range []string{"dev", "prod", "staging"} {
		declareEnvDir(t, dir, env)
	}
	// No retired in-checkout ledger: answered without exec'ing git.
	retiredLedgerPresence.Store(dir, false)
	t.Cleanup(func() { retiredLedgerPresence.Delete(dir) })

	report, code, out := runTopologyJSON(t, nil, envTopologyOptions{
		ProjectDir: dir,
		Resolver:   kclTargetResolver{memo: &envStatusRenderMemo{}},
	})
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	if len(report.Environments) != 3 {
		t.Fatalf("got %d environments, want 3:\n%s", len(report.Environments), out)
	}
	if n := raws.Load(); n != 3 {
		t.Fatalf("`forge env status` evaluated %d declarations for 3 envs, want 3 (one each): the lifecycle, destination "+
			"and ledger reads must share the command's render (withDeclarationMemo)", n)
	}
}

func TestGitCatFileBatch_ReadsEverySpecInOneCall(t *testing.T) {
	testkit.Slow(t, "execs git")
	dir := t.TempDir()
	files := map[string]string{
		"a.json":       `{"release":{"version":"v1"}}`,
		"nl.json":      "line one\nline two\n",
		"empty.json":   "",
		"sub/b c.json": "spaced",
	}
	for rel, content := range files {
		path := filepath.Join(dir, ".forge", "releases", rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitInit(t, dir)

	specs := []string{"HEAD:.forge/releases/missing.json"}
	for rel := range files {
		specs = append(specs, "HEAD:.forge/releases/"+rel)
	}
	got, err := gitCatFileBatch(context.Background(), dir, specs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["HEAD:.forge/releases/missing.json"]; ok {
		t.Error("a missing object must be absent from the result, so the caller falls back for it alone")
	}
	for rel, want := range files {
		b, ok := got["HEAD:.forge/releases/"+rel]
		if !ok || string(b) != want {
			t.Errorf("%s: got %q (present %v), want %q", rel, b, ok, want)
		}
	}
}

// stubRenderKCLRaw replaces the KCL evaluation with an empty declaration and
// counts the evaluations.
func stubRenderKCLRaw(t *testing.T, n *atomic.Int64) {
	t.Helper()
	prev := renderKCLRawHook
	renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) {
		n.Add(1)
		return []byte(`{"output": {}}`), nil
	}
	t.Cleanup(func() { renderKCLRawHook = prev })
}

// A caller waiting for another's evaluation of the same env gives up on its
// own cancellation at once: it never waits for the evaluation to finish. The
// evaluation still completes and its answer is shared.
func TestDeclarationMemo_WaiterHonoursItsOwnCancellation(t *testing.T) {
	ctx := withDeclarationMemo(context.Background())
	m := declarationMemoFrom(ctx)
	dir := t.TempDir()
	started, release := make(chan struct{}), make(chan struct{})
	first := make(chan *KCLEntities)
	go func() {
		e, _ := m.render(ctx, dir, "prod", func(context.Context) (*KCLEntities, error) {
			close(started)
			<-release
			return &KCLEntities{Env: "prod"}, nil
		})
		first <- e
	}()
	<-started

	waiter, cancel := context.WithCancel(ctx)
	cancel()
	returned := make(chan error, 1)
	go func() {
		_, err := m.render(waiter, dir, "prod", func(context.Context) (*KCLEntities, error) {
			t.Error("the waiter evaluated beside the running evaluation")
			return nil, nil
		})
		returned <- err
	}()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled waiter blocked behind the running evaluation")
	}

	close(release)
	got := <-first
	again, err := m.render(ctx, dir, "prod", func(context.Context) (*KCLEntities, error) {
		t.Error("a finished evaluation was evaluated again")
		return nil, nil
	})
	if err != nil || again != got {
		t.Fatalf("after the evaluation: %v, %v; want its shared answer", again, err)
	}
}
