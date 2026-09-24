// Copyright (c) 2025 Reliant Labs
package runner

import (
	"context"
	"fmt"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the properties that let the runner execute inside API
// processes (the RunScenario RPC and the run_scenario tool), not just in CI.

func mustWorkflow(t *testing.T, y string) *Runner {
	t.Helper()
	wf, err := wfyaml.ParseWorkflow([]byte(y))
	require.NoError(t, err)
	return NewRunner(wf)
}

func mustScenario(t *testing.T, y string) *scenario.Scenario {
	t.Helper()
	all, err := scenario.ParseScenarioYAML([]byte(y))
	require.NoError(t, err)
	require.Len(t, all, 1)
	return all[0]
}

// Many scenarios run concurrently in one process, each on its own
// environment, and each gets its own correct answer — no cross-talk between
// runs through shared state.
func TestRunner_ConcurrentRunsAreIsolated(t *testing.T) {
	r := mustWorkflow(t, `
name: echo
entry: [ask]
nodes:
  - id: ask
    type: call_llm
    args: {model: {tags: [fast]}}
outputs:
  answer: "{{nodes.ask.response_text}}"
`)
	const n = 24
	var wg sync.WaitGroup
	results := make([]*Result, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = r.Run(mustScenario(t, fmt.Sprintf(`
name: run_%[1]d
events:
  - node: ask
    type: llm_response
    text: "answer-%[1]d"
expect:
  outcome: completed
  outputs:
    answer: "answer-%[1]d"
`, i)))
		}(i)
	}
	wg.Wait()
	for i, res := range results {
		assert.Equal(t, scenario.StatusPassed, res.Status, "run %d: %v", i, res.Mismatches)
	}
}

// A workflow that never stops dispatching (a loop whose while stays true)
// is never idle, so the SDK's idle timeout cannot stop it. The runner's own
// budget must, and must return promptly with a timeout error.
func TestRunner_NonTerminatingScenarioTimesOut(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: spin
entry: [forever]
nodes:
  - id: forever
    type: loop
    while: "true"
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
          args: {model: {tags: [fast]}}
`))
	require.NoError(t, err)
	r := New(wf, Options{Timeout: 500 * time.Millisecond})

	start := time.Now()
	res := r.Run(mustScenario(t, `
name: spins
events: []
`))
	elapsed := time.Since(start)

	assert.Equal(t, scenario.StatusError, res.Status)
	require.NotNil(t, res.Execution.Error)
	assert.Contains(t, res.Execution.Error.Message, "did not terminate")
	assert.Less(t, elapsed, 5*time.Second, "the budget must bound the run")
}

// A caller's context deadline bounds the run too, independently of the
// runner's own budget.
func TestRunner_ContextDeadlineBoundsRun(t *testing.T) {
	r := mustWorkflow(t, `
name: spin
entry: [forever]
nodes:
  - id: forever
    type: loop
    while: "true"
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
          args: {model: {tags: [fast]}}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	res := r.RunContext(ctx, mustScenario(t, "name: spins\nevents: []\n"))
	assert.Equal(t, scenario.StatusError, res.Status)
	assert.Contains(t, res.Execution.Error.Message, "did not terminate")
}

// An activity the runner has no mock for must never execute for real. It
// fails the run, named, instead of reaching a daemon, the database or the
// network.
func TestRunner_UnmockedActivityIsAScenarioErrorNotARealCall(t *testing.T) {
	r := mustWorkflow(t, `
name: wt
entry: [make_wt]
nodes:
  - id: make_wt
    type: create_worktree
`)
	// Take the node activity away so the dispatch reaches the catch-all:
	// this is exactly what an activity added to the runtime without a runner
	// mock would do.
	res := r.runWithout(t, "CreateWorktree", mustScenario(t, "name: wt\nevents: []\n"))

	require.NotEqual(t, scenario.StatusPassed, res.Status)
	joined := strings.Join(res.Mismatches, "\n")
	assert.Contains(t, joined, `workflow dispatched activity "CreateWorktree", which the scenario runner does not mock`)
}

// A panic anywhere in the run is contained and reported, never propagated
// into the calling process.
func TestRunner_PanicIsContained(t *testing.T) {
	r := &Runner{workflow: nil, loader: LoadBuiltinWorkflow, timeout: time.Second}
	var res *Result
	require.NotPanics(t, func() {
		res = r.Run(&scenario.Scenario{Name: "nil_workflow"})
	})
	assert.Equal(t, scenario.StatusError, res.Status)
}

func (r *Runner) runWithout(t *testing.T, activityName string, sc *scenario.Scenario) *Result {
	t.Helper()
	clone := *r
	clone.omitActivity = activityName
	return clone.Run(sc)
}

// An abandoned run must unwind, not keep spinning in the background: the API
// process that hosted it lives on.
func TestRunner_TimedOutRunDoesNotLeakGoroutines(t *testing.T) {
	wf, err := wfyaml.ParseWorkflow([]byte(`
name: spin
entry: [forever]
nodes:
  - id: forever
    type: loop
    while: "true"
    inline:
      entry: [work]
      nodes:
        - id: work
          type: call_llm
          args: {model: {tags: [fast]}}
`))
	require.NoError(t, err)
	r := New(wf, Options{Timeout: 200 * time.Millisecond})
	sc := mustScenario(t, "name: spins\nevents: []\n")

	_ = r.Run(sc) // warm up lazily-started background goroutines
	time.Sleep(200 * time.Millisecond)
	before := goruntime.NumGoroutine()
	for i := 0; i < 5; i++ {
		_ = r.Run(sc)
	}
	require.Eventually(t, func() bool {
		return goruntime.NumGoroutine() <= before+2
	}, 5*time.Second, 50*time.Millisecond,
		"goroutines grew from %d after 5 abandoned runs", before)
}
