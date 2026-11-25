package cli

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// countKCLEvaluations replaces the render seam with a fake that answers every
// env with an empty render and counts how many evaluations ran, at most how
// many at once. With overlap > 1, each evaluation blocks until overlap
// evaluations have entered (a barrier, not a sleep), so a batch that runs
// them concurrently reaches that peak deterministically; one that runs them
// serially is caught by the deadline and leaves the peak at 1.
func countKCLEvaluations(t *testing.T, overlap int64) (evals, peak *atomic.Int64) {
	t.Helper()
	evals, peak = &atomic.Int64{}, &atomic.Int64{}
	var inFlight, entered atomic.Int64
	released := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(released) }) }
	prev := renderKCLRawHook
	renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) {
		evals.Add(1)
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if overlap > 1 {
			if entered.Add(1) >= overlap {
				release()
			}
			select {
			case <-released:
			case <-time.After(30 * time.Second):
				release() // never overlapped: let every evaluation finish so the test can fail
			}
		}
		return []byte(`{"output": {}}`), nil
	}
	t.Cleanup(func() { release(); renderKCLRawHook = prev })
	return evals, peak
}

func writeKCLMemoProject(t *testing.T, envs ...string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: memo\nmodule_path: example.com/memo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, env := range envs {
		writeKCLMemoFile(t, dir, "deploy/kcl/"+env+"/main.k", "output = {}\n")
	}
	return dir
}

func writeKCLMemoFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestKCLRenderMemo_OneEvaluationPerInputState pins what the generate-run memo
// buys: the five generate steps that read an env's KCL share one evaluation
// while nothing it reads has changed, and a step that rewrites a deploy file
// or forge.yaml gets a fresh one.
func TestKCLRenderMemo_OneEvaluationPerInputState(t *testing.T) {
	testkit.Slow(t, "execs git")
	evals, _ := countKCLEvaluations(t, 0)
	dir := writeKCLMemoProject(t, "dev")
	defer beginKCLRenderMemo()()

	render := func() {
		t.Helper()
		if _, _, err := renderKCLPure(context.Background(), dir, "dev"); err != nil {
			t.Fatalf("renderKCLPure: %v", err)
		}
	}
	render()
	render()
	render()
	if got := evals.Load(); got != 1 {
		t.Fatalf("three renders of unchanged inputs evaluated %d times, want 1", got)
	}

	writeKCLMemoFile(t, dir, "deploy/kcl/config_gen.k", "x = 1\n")
	render()
	if got := evals.Load(); got != 2 {
		t.Fatalf("a rewritten deploy file must re-evaluate: %d evaluations, want 2", got)
	}

	writeKCLMemoFile(t, dir, "deploy/kcl/config_gen.k", "x = 1\n") // same bytes
	render()
	if got := evals.Load(); got != 2 {
		t.Fatalf("a deploy file rewritten with the same bytes must not re-evaluate: %d evaluations, want 2", got)
	}

	writeKCLMemoFile(t, dir, "forge.yaml", "name: memo\nmodule_path: example.com/memo\nfrontends: []\n")
	render()
	if got := evals.Load(); got != 3 {
		t.Fatalf("a rewritten forge.yaml must re-evaluate: %d evaluations, want 3", got)
	}
}

// TestKCLRenderMemo_OffOutsideAGenerateRun keeps the memo out of every other
// command: `forge env up` renders an env twice on purpose, with the port
// allocator in states the key does not see.
func TestKCLRenderMemo_OffOutsideAGenerateRun(t *testing.T) {
	testkit.Slow(t, "execs git")
	evals, _ := countKCLEvaluations(t, 0)
	dir := writeKCLMemoProject(t, "dev")

	for range 2 {
		if _, _, err := renderKCLPure(context.Background(), dir, "dev"); err != nil {
			t.Fatalf("renderKCLPure: %v", err)
		}
	}
	if got := evals.Load(); got != 2 {
		t.Fatalf("with no generate run live, two renders evaluated %d times, want 2", got)
	}

	end := beginKCLRenderMemo()
	if _, _, err := renderKCLPure(context.Background(), dir, "dev"); err != nil {
		t.Fatal(err)
	}
	end()
	if _, _, err := renderKCLPure(context.Background(), dir, "dev"); err != nil {
		t.Fatal(err)
	}
	if got := evals.Load(); got != 4 {
		t.Fatalf("a render after the run ended must evaluate again: %d evaluations, want 4", got)
	}
}

// TestRenderEnvsPure_EvaluatesTheMissesConcurrentlyInOrder pins the batch: the
// envs the memo cannot answer are evaluated at the same time, the results come
// back in the order asked, and a second walk is answered from the memo.
func TestRenderEnvsPure_EvaluatesTheMissesConcurrentlyInOrder(t *testing.T) {
	testkit.Slow(t, "execs git")
	evals, peak := countKCLEvaluations(t, 2)
	envs := []string{"dev", "prod", "staging"}
	dir := writeKCLMemoProject(t, envs...)
	defer beginKCLRenderMemo()()

	got := renderEnvsPure(context.Background(), dir, envs)
	if len(got) != len(envs) {
		t.Fatalf("got %d results for %d envs", len(got), len(envs))
	}
	for i, r := range got {
		if r.env != envs[i] || r.err != nil || r.entities == nil {
			t.Errorf("result %d = {env %q, err %v, entities %v}, want env %q rendered", i, r.env, r.err, r.entities != nil, envs[i])
		}
	}
	if p := peak.Load(); p < 2 {
		t.Errorf("at most %d evaluation(s) ran at once; the misses must be evaluated concurrently", p)
	}

	renderEnvsPure(context.Background(), dir, envs)
	if n := evals.Load(); n != int64(len(envs)) {
		t.Errorf("two walks over unchanged envs evaluated %d times, want %d", n, len(envs))
	}
}

// TestKCLRenderMemo_ConcurrentRunsShareNothingStale guards the counter: one run
// ending while another is live must not drop the live run's memo, and the
// last one out must.
func TestKCLRenderMemo_ConcurrentRunsShareNothingStale(t *testing.T) {
	endA := beginKCLRenderMemo()
	endB := beginKCLRenderMemo()
	endA()
	endA() // idempotent
	if _, live := kclRenderMemoKey(t.TempDir(), "dev"); !live {
		t.Fatal("the memo went off while a run was still live")
	}
	endB()
	if _, live := kclRenderMemoKey(t.TempDir(), "dev"); live {
		t.Fatal("the memo stayed on after the last run ended")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); beginKCLRenderMemo()() }()
	}
	wg.Wait()
	if _, live := kclRenderMemoKey(t.TempDir(), "dev"); live {
		t.Fatal("concurrent begin/end left the memo on")
	}
}
