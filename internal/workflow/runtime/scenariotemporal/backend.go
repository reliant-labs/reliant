// Copyright (c) 2025 Reliant Labs

// Package scenariotemporal runs simulator scenarios against the REAL
// DynamicWorkflow inside a Temporal TestWorkflowEnvironment.
//
// It is a SECOND BACKEND for the exact same scenario YAML the fast simulator
// consumes — same simulator.Scenario, same simulator.Expectation, and the same
// simulator.CheckExpectations assertion evaluator. The only thing that differs
// is what executes between "here are the node outputs" and "here is what
// happened": the simulator walks the graph itself, while this backend hands the
// graph to DynamicWorkflow and mocks the ACTIVITY layer underneath it.
//
// That difference is the entire point. The simulator has no activity layer, so
// a passing scenario there proves only "given these node outputs, the graph
// routes here". Running the same scenario through DynamicWorkflow additionally
// exercises StepExecutor dispatch, the real loop executors, the real inline
// workflow/approval handling, node-condition skips through the production code
// path, and the real workflow-output evaluation.
//
// This is a TEST-LANE capability. It is deliberately NOT wired into the
// interactive run_scenario tool: that loop needs millisecond feedback while an
// LLM drafts a workflow, and the fast simulator remains the default there.
package scenariotemporal

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	// Imported for its init(), which registers every activity's input/output
	// type into the schema registry. This is the same registration production
	// relies on for output normalization and CEL typing; without it every
	// GetOutputDefaults lookup returns nil and mocks stay un-normalized.
	_ "github.com/reliant-labs/reliant/internal/workflow/runtime/activities"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"
)

// Result is the outcome of running one scenario on the Temporal backend.
//
// It reuses simulator.ScenarioResult verbatim so a caller can compare a
// Temporal run and a simulator run field by field — which is exactly what the
// parity test does.
type Result = simulator.ScenarioResult

// Runner executes scenarios for a single workflow definition against
// DynamicWorkflow.
type Runner struct {
	workflow *reliantv1.Workflow
}

// NewRunner builds a Temporal-backed scenario runner for a workflow.
func NewRunner(wf *reliantv1.Workflow) *Runner {
	return &Runner{workflow: wf}
}

// recorder accumulates what the real run actually did, in the shape the shared
// expectation checker reads.
//
// Every field here is written from an activity mock, i.e. from the workflow's
// own dispatch decisions — never from a graph walk this package performs. If
// this package ever starts deciding which node runs next, it has become the
// simulator with extra steps and the coverage it appears to provide is fake.
type recorder struct {
	mu sync.Mutex

	reached   []string
	completed []string
	skipped   []string
	seen      map[string]bool
	states    map[string]simulator.NodeExecutionState
	outputs   map[string]map[string]interface{}

	// loopIterations is the highest loop_iteration a WorkflowCheckpoint
	// reported for each loop node, +1 — i.e. how many iterations that loop
	// actually ran. A loop dispatches no activity of its own, so this
	// per-iteration checkpoint is the only place the real runtime tells us,
	// and without it `_iterations` on a loop node is unassertable.
	loopIterations map[string]int

	// unconsumed tracks scenario events that no activity ever asked for.
	consumed map[string]int
}

func newRecorder() *recorder {
	return &recorder{
		seen:           map[string]bool{},
		states:         map[string]simulator.NodeExecutionState{},
		outputs:        map[string]map[string]interface{}{},
		loopIterations: map[string]int{},
		consumed:       map[string]int{},
	}
}

func (r *recorder) markReached(id string) {
	// The stand-in node of a black-boxed sub-workflow is harness scaffolding,
	// not a graph node a scenario can name. Reporting it would put an id in
	// reached that no workflow contains.
	if id == "" || strings.HasSuffix(id, blackBoxNodeID) {
		return
	}
	if !r.seen[id] {
		r.seen[id] = true
		r.reached = append(r.reached, id)
	}
}

// snapshotReached copies what has been reached so far. Used on the
// non-termination path, where the workflow goroutine is still writing — reading
// r.reached directly there is a data race.
func (r *recorder) snapshotReached() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reached...)
}

func (r *recorder) recordCompleted(id string, out map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markReached(id)
	r.states[id] = simulator.StateCompleted
	r.completed = append(r.completed, id)
	r.outputs[id] = out
}

// recordEntered marks a node as reached without claiming it completed. Used for
// structural nodes (loop, workflow), which announce entry via a checkpoint and
// have no activity of their own to complete.
//
// iteration is the loop_iteration the checkpoint carried; for a non-loop node
// the runtime always sends 0, which yields a count of 1 and is simply unused.
func (r *recorder) recordEntered(id string, iteration int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markReached(id)
	if n := iteration + 1; n > r.loopIterations[id] {
		r.loopIterations[id] = n
	}
}

func (r *recorder) recordSkipped(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markReached(id)
	r.states[id] = simulator.StateSkipped
	r.skipped = append(r.skipped, id)
}

// qualifiedNodeID is the dotted node id a scenario uses ("agent_loop.call_llm"),
// read from the runtime context the real executors built.
//
// NodePath is the authoritative answer: it is COMPOSED at every nesting
// boundary, so a node inside a sub-workflow inside a loop reports
// "impl_loop.attempt.review". Deriving the id from LoopNodeID instead cannot
// express that — LoopNodeID is loop-scoped identity and deliberately holds a
// single id, and the sub-workflow boundary keeps the OUTERMOST one, so from two
// levels down the path is unrecoverable.
//
// The LoopNodeID branch remains only for a context built before NodePath was
// threaded through (a replayed history, or a dispatch site that predates it):
// it reproduces the old one-level form rather than silently reporting a bare id.
func qualifiedNodeID(rtx types.RuntimeContext) string {
	if rtx.NodePath != "" {
		return rtx.NodePath
	}
	if rtx.LoopNodeID != "" && rtx.LoopNodeID != rtx.StepID {
		return rtx.LoopNodeID + "." + rtx.StepID
	}
	return rtx.StepID
}

// eventTable indexes scenario events the same way the simulator's mocker does:
// node-targeted events are consumed in order per node, and untargeted events
// are consumed sequentially.
type eventTable struct {
	mu         sync.Mutex
	byNode     map[string][]simulator.SimulatedEvent
	nodeIdx    map[string]int
	sequential []simulator.SimulatedEvent
	seqIdx     int

	// overrun counts, per node, how many times the runtime dispatched a node
	// AFTER its scenario events were used up. See next() for why this is
	// recorded rather than raised.
	overrun map[string]int
}

func newEventTable(events []simulator.SimulatedEvent) *eventTable {
	t := &eventTable{
		byNode:  map[string][]simulator.SimulatedEvent{},
		nodeIdx: map[string]int{},
		overrun: map[string]int{},
	}
	for _, e := range events {
		if e.Node != "" {
			t.byNode[e.Node] = append(t.byNode[e.Node], e)
		} else {
			t.sequential = append(t.sequential, e)
		}
	}
	return t
}

// next returns the mock output for a node, or an empty map when the scenario
// said nothing about it.
//
// When the scenario MOCKED a node and then ran out — the author supplied N
// events and the real runtime dispatched the N+1th — the empty map is still
// returned, but the overrun is RECORDED so the run reports it.
//
// Returning empty rather than failing is deliberate. An empty output is not a
// neutral answer, and that is exactly the hazard: the scenario's own CEL reads
// it, so an exhausted structured-agent execute_tools yields no response_data,
// `completed` computes false, and the loop keeps going for a reason the author
// never wrote. But raising an error here is worse than the disease — an
// activity failure drives the REAL runtime's retry-exhaustion and pause paths,
// so the harness would be injecting control flow the scenario never asked for,
// and this package's whole contract is that it observes the workflow rather
// than steering it. Recording the overrun keeps the diagnosis and leaves
// execution alone.
//
// A node with NO events at all is left silent: never mocking a node is the
// existing, deliberate "empty output" contract (an unmocked save_message must
// stay free). Only exhausting a node the author DID mock is under-specification.
func (t *eventTable) next(nodeID string) map[string]interface{} {
	t.mu.Lock()
	defer t.mu.Unlock()

	events, mocked := t.byNode[nodeID]
	if mocked {
		if i := t.nodeIdx[nodeID]; i < len(events) {
			t.nodeIdx[nodeID] = i + 1
			return simulator.EventOutput(events[i])
		}
	}
	if t.seqIdx < len(t.sequential) {
		e := t.sequential[t.seqIdx]
		t.seqIdx++
		return simulator.EventOutput(e)
	}
	if mocked {
		t.overrun[nodeID]++
	}
	return map[string]interface{}{}
}

func (t *eventTable) unconsumed() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	var out []string
	nodes := make([]string, 0, len(t.byNode))
	for n := range t.byNode {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	for _, n := range nodes {
		for i := t.nodeIdx[n]; i < len(t.byNode[n]); i++ {
			out = append(out, fmt.Sprintf(
				"event targeting %q was never consumed (node may not exist or wasn't reached)", n))
		}
	}
	for i := t.seqIdx; i < len(t.sequential); i++ {
		out = append(out, "sequential event was never consumed")
	}

	// The mirror image of an unconsumed event: the scenario ran out and the
	// workflow kept going. Reported in the same list because it is the same
	// class of defect — the scenario and the run disagree about how much
	// happens — and because it is the difference between "this scenario is
	// under-specified" and the symptom it otherwise presents as, which is a
	// loop that appears not to terminate.
	overrunNodes := make([]string, 0, len(t.overrun))
	for n := range t.overrun {
		overrunNodes = append(overrunNodes, n)
	}
	sort.Strings(overrunNodes)
	for _, n := range overrunNodes {
		out = append(out, fmt.Sprintf(
			"scenario exhausted its mocks for node %q: %d event(s) supplied but the runtime "+
				"dispatched it %d more time(s), each answered with EMPTY output (the workflow "+
				"does more than this scenario describes — supply the missing events, or assert "+
				"the outcome the extra executions produce)",
			n, len(t.byNode[n]), t.overrun[n]))
	}
	return out
}

// Run executes one scenario against DynamicWorkflow in a fresh
// TestWorkflowEnvironment and evaluates the scenario's expectation against what
// really happened.
func (r *Runner) Run(scenario *simulator.Scenario) *Result {
	start := time.Now()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	rec := newRecorder()
	events := newEventTable(scenario.Events)

	if err := r.registerActivities(env, rec, events); err != nil {
		return errorResult(scenario, err, start)
	}

	inputs := map[string]interface{}{}
	for k, v := range scenario.Inputs {
		inputs[k] = v
	}

	// A scenario that does not terminate must FAIL, not hang. The fast
	// simulator has always had this bound (MaxIterations, default 100); this
	// backend had none, so one non-terminating scenario took the whole lane
	// from ~2s to the Go test timeout and took every other scenario's result
	// with it — a hang reports nothing, while a failure names the scenario.
	//
	// The bound is wall-clock rather than an iteration count because the loop
	// lives inside DynamicWorkflow, which this package deliberately does not
	// reach into: the runner mocks the activity layer and never decides what
	// runs next. Every scenario here settles in milliseconds, so seconds is
	// orders of magnitude of headroom, not a tuned threshold.
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		env.ExecuteWorkflow(runtime.DynamicWorkflow, runtime.WorkflowInput{
			ChatID:       "scenario-chat",
			WorkflowName: r.workflow.GetName(),
			Inputs:       inputs,
			ExecContext: &runtime.ExecutionContext{
				WorkflowID:   "scenario-wf",
				ChatID:       "scenario-chat",
				Thread:       "scenario-thread",
				ThreadMode:   model.ThreadModeNew,
				WorkflowName: r.workflow.GetName(),
			},
		})
	}()

	select {
	case <-runDone:
	case <-time.After(scenarioRunBudget):
		// The workflow goroutine is left running: the test environment owns it
		// and offers no cancellation, and it is abandoned when the process
		// exits. Returning here is what keeps one runaway scenario from
		// destroying the whole lane's results.
		return &Result{
			Status:   simulator.StatusError,
			Scenario: scenario.Name,
			Execution: simulator.ExecutionDetails{
				Outcome:      "error",
				NodesReached: rec.snapshotReached(),
				Error:        &simulator.ErrorDetails{Message: nonTerminatingMessage},
				DurationMs:   time.Since(start).Milliseconds(),
			},
			Expected:   scenario.Expect,
			Mismatches: []string{nonTerminatingMessage},
			RunAt:      time.Now(),
		}
	}

	// A loop node publishes its outputs into the workflow's node-output store
	// (workflow.go's ProtoLoopOutputToMap -> nodeOutputStore.Set) and runs no
	// activity of its own, so no mock above can observe them. Surfacing the
	// iteration count the runtime's own per-iteration checkpoint reported is
	// what lets `node_outputs: { <loop>: { _iterations: N } }` be asserted here
	// as it is in the simulator — under the same field name the real runtime
	// uses (model.LoopOutputIterationsField).
	for nodeID, iterations := range rec.loopIterations {
		if _, isActivityNode := rec.outputs[nodeID]; isActivityNode {
			continue
		}
		rec.outputs[nodeID] = map[string]interface{}{model.LoopOutputIterationsField: iterations}
	}

	execution := simulator.ExecutionDetails{
		NodesReached:   rec.reached,
		NodesCompleted: rec.completed,
		NodesSkipped:   rec.skipped,
		NodeStates:     rec.states,
		NodeOutputs:    rec.outputs,
		DurationMs:     time.Since(start).Milliseconds(),
	}

	if runErr := env.GetWorkflowError(); runErr != nil {
		execution.Outcome = "error"
		execution.Error = &simulator.ErrorDetails{Message: runErr.Error()}
	} else {
		execution.Outcome = "completed"
		var wfResult *runtime.WorkflowResult
		if err := env.GetWorkflowResult(&wfResult); err == nil && wfResult != nil {
			execution.WorkflowOutputs = wfResult.Outputs
		}
	}

	result := &Result{
		Scenario:  scenario.Name,
		Execution: execution,
		Expected:  scenario.Expect,
		RunAt:     time.Now(),
	}

	mismatches := events.unconsumed()
	if scenario.Expect != nil {
		// The SHARED evaluator — same function the fast simulator calls.
		mismatches = append(mismatches, simulator.CheckExpectations(scenario.Expect, &execution)...)
	}
	result.Mismatches = mismatches

	switch {
	case len(mismatches) > 0:
		result.Status = simulator.StatusFailed
	case scenario.Expect == nil && execution.Outcome == "error":
		result.Status = simulator.StatusError
	default:
		result.Status = simulator.StatusPassed
	}
	return result
}

func errorResult(scenario *simulator.Scenario, err error, start time.Time) *Result {
	return &Result{
		Status:   simulator.StatusError,
		Scenario: scenario.Name,
		Execution: simulator.ExecutionDetails{
			Outcome:    "error",
			Error:      &simulator.ErrorDetails{Message: err.Error()},
			DurationMs: time.Since(start).Milliseconds(),
		},
		Mismatches: []string{err.Error()},
		RunAt:      time.Now(),
	}
}

// nodeActivityNames returns every activity name this workflow's nodes can
// dispatch, walking inline sub-graphs so loop bodies are covered too.
//
// SaveMessage is always included: a node's inline `save_message:` block runs a
// SaveMessage activity even when no save_message NODE exists anywhere in the
// graph, and an unregistered activity fails the step rather than being skipped.
func nodeActivityNames(wf *reliantv1.Workflow) map[string]bool {
	names := map[string]bool{"SaveMessage": true}
	var walk func(nodes []*reliantv1.Node)
	walk = func(nodes []*reliantv1.Node) {
		for _, n := range nodes {
			if model.IsActivityNode(n.GetType()) {
				names[activityNameFor(n.GetType())] = true
			}
			// A `run` node is STRUCTURAL, so the check above skips it, yet it
			// still dispatches a real activity — and one whose name the
			// snake_case->PascalCase rule cannot produce ("run" would give
			// "Run", not "ExecuteRunStep"). Left unregistered, every run node
			// fails with ActivityNotRegisteredError, Temporal retries it to
			// exhaustion, and the loop pauses on retry exhaustion.
			if name := nodeTypeActivityOverrides[n.GetType()]; name != "" {
				names[name] = true
			}
			if inline := model.NodeInlineWorkflow(n); inline != nil {
				walk(inline.GetNodes())
			}
		}
	}
	walk(wf.GetNodes())
	return names
}

// nodeTypeActivityOverrides mirrors runtime's nodeTypeToActivityNameOverrides:
// node types whose activity name the snake_case -> PascalCase rule cannot
// derive. `run` is structural but dispatches ExecuteRunStep.
var nodeTypeActivityOverrides = map[string]string{model.NodeTypeRun: "ExecuteRunStep"}

// activityNameFor mirrors runtime's snake_case node type -> PascalCase activity
// name mapping. Kept here because the runtime copy is unexported; the acronym
// table is the only interesting part and it is pinned by a test.
var activityAcronyms = map[string]string{"llm": "LLM", "api": "API", "url": "URL", "id": "ID", "mcp": "MCP"}

func activityNameFor(nodeType string) string {
	parts := strings.Split(nodeType, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		if a, ok := activityAcronyms[strings.ToLower(p)]; ok {
			parts[i] = a
		} else {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

// infrastructureActivities are the activities DynamicWorkflow calls for its own
// bookkeeping rather than to execute a graph node. They are stubbed to
// no-op success: none of them influences routing, and a scenario has nothing to
// say about them.
//
// They take `interface{}` rather than map[string]interface{} because several
// are dispatched with a concrete struct (EmitStreamFinalized sends
// types.EmitStreamFinalizedInput), and Temporal's reflective dispatch fails
// outright on a stub whose parameter type does not match.
var infrastructureActivities = []string{
	"WorkflowStatus",
	"WorkflowError",
	"Cleanup",
	"LoadPresetParams",
	"EmitStreamFinalized",
	"EmitToolCallStatus",
	"PreflightDaemonCheck",
	"ValidateThreadOwnership",
	// QuestionCreate is NOT here: it resolves an ask_question node from the
	// scenario and is registered explicitly below, like ApprovalCreate.
	"QuestionResolve",
	"ApprovalResolve",
	// A `workflow` node that spawns a CHILD workflow first creates the child's
	// thread and workflow row through this activity. It is bookkeeping, not a
	// graph node, but leaving it unregistered fails the parent's step.
	"CreateWorkflowWithThread",
}

// scenarioRunBudget is the wall-clock ceiling for a single scenario. Every
// scenario in this lane settles in milliseconds; anything still running after
// this is not slow, it is not terminating.
const scenarioRunBudget = 20 * time.Second

// nonTerminatingMessage is reported when a scenario exhausts scenarioRunBudget.
// It names the usual cause: a loop whose `while` reads mocked outputs stays true
// once the scenario's events run out, because an unmatched node falls back to an
// empty mock rather than to "the loop should stop".
const nonTerminatingMessage = "scenario did not terminate within the run budget — " +
	"a loop's while condition never went false (check that the scenario supplies " +
	"a final event whose output exits the loop)"

// bareWorkflowName strips a workflow ref's scheme ("builtin://agent" -> "agent")
// so a ref can be compared against a loaded workflow's own name.
func bareWorkflowName(ref string) string {
	if i := strings.Index(ref, "://"); i >= 0 {
		return ref[i+len("://"):]
	}
	return ref
}

// blackBoxNodeID is the id of the single node an opaque sub-workflow runs. It
// is deliberately not a name any scenario would write, and it is filtered out of
// the reached set so a scenario can neither see nor assert on it.
const blackBoxNodeID = "__scenario_black_box"

// blackBoxWorkflow returns a stand-in graph for a referenced sub-workflow whose
// body a scenario has not asked to execute: one inert save_message node. It
// keeps the parent's inputs so input binding still type-checks, and it cannot
// loop, block, or fail.
func blackBoxWorkflow(wf *reliantv1.Workflow) *reliantv1.Workflow {
	return &reliantv1.Workflow{
		Name:       wf.GetName(),
		ApiVersion: wf.GetApiVersion(),
		Inputs:     wf.GetInputs(),
		Entry:      []string{blackBoxNodeID},
		Nodes: []*reliantv1.Node{{
			Id:   blackBoxNodeID,
			Type: model.NodeTypeSaveMessage,
		}},
	}
}

func (r *Runner) registerActivities(env *testsuite.TestWorkflowEnvironment, rec *recorder, events *eventTable) error {
	wfJSON, err := protojson.Marshal(r.workflow)
	if err != nil {
		return fmt.Errorf("marshal workflow: %w", err)
	}

	blackBoxJSON, err := protojson.Marshal(blackBoxWorkflow(r.workflow))
	if err != nil {
		return fmt.Errorf("marshal black-box workflow: %w", err)
	}

	// The loader MUST honour workflow_name. Returning the parent graph for every
	// request makes a `ref:` load ITSELF: builtin://structured-agent resolved to
	// structured-agent, whose agent_loop then ran forever against a completion
	// signal no mock can produce (max_turns: 0 is unlimited), and the whole lane
	// died on the Go test timeout instead of finishing in ~2s.
	//
	// An unmocked ref is therefore a BLACK BOX — one inert node, no body. This
	// mirrors the fast simulator's hasInternalEvents gate and the rule stated in
	// docs/workflows/testing.mdx: an event on the ref NODE mocks that node and
	// leaves its body opaque, and only an event naming a node strictly INSIDE
	// the ref (a qualified id like `parent.inner`) makes the body transparent.
	//
	// Nothing here loads a real sub-workflow yet, so every ref is opaque. That
	// is the safe default: a scenario that wants a body executed says so with a
	// qualified event, and until this grows real ref loading, such a scenario
	// fails visibly on an unconsumed event rather than hanging the lane.
	env.RegisterActivityWithOptions(
		func(_ context.Context, in map[string]string) (runtime.LoadedWorkflow, error) {
			// A ref is written "builtin://agent" while the loaded workflow is
			// named "agent", so compare on the bare name — otherwise a workflow
			// requesting ITSELF is misread as a sub-workflow, and vice versa.
			if ref := in["workflow_name"]; ref != "" && bareWorkflowName(ref) != r.workflow.GetName() {
				return runtime.LoadedWorkflow{WorkflowJSON: blackBoxJSON}, nil
			}
			return runtime.LoadedWorkflow{WorkflowJSON: wfJSON}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)

	// A skipped node dispatches no activity at all — SkippedStep is the real
	// runtime's only observable signal that a node was scheduled and its
	// condition was false. Reading it here is what lets `skipped:` and
	// `not_reached:` mean the same thing on both backends.
	env.RegisterActivityWithOptions(
		func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
			stepID, _ := in["step_id"].(string)
			// node_path is the qualified position; a skipped node inside a
			// sub-workflow body is otherwise recorded under its bare id and
			// cannot be matched against `not_reached: [outer.inner]`.
			if nodePath, _ := in["node_path"].(string); nodePath != "" {
				stepID = nodePath
			}
			rec.recordSkipped(stepID)
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "SkippedStep"},
	)

	// A STRUCTURAL node (loop, join, workflow) dispatches no activity of its
	// own, so the activity mocks above can never observe one. WorkflowCheckpoint
	// is the real runtime's node-entry signal and is the only place a top-level
	// loop node announces itself — which is what makes `reached: [agent_loop]`
	// mean the same thing here as in the simulator.
	env.RegisterActivityWithOptions(
		func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
			if nodeID, _ := in["node_id"].(string); nodeID != "" {
				iteration, _ := in["loop_iteration"].(float64)
				rec.recordEntered(nodeID, int(iteration))
			}
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "WorkflowCheckpoint"},
	)

	// An approval node does not run an activity named for its node type. It
	// creates a row via ApprovalCreate and then BLOCKS on a signal that only a
	// human sends, so the node is observable here only at ApprovalCreate, and a
	// scenario can only resolve it by answering through this mock's
	// already_resolved path — the same short-circuit a replay takes.
	//
	// Returning already_resolved is what makes an approval scenario runnable
	// offline. The alternative, stubbing ApprovalCreate to an empty map, leaves
	// the workflow waiting for a signal nobody sends until the 1h timer fires,
	// which is what a scenario's `status: approved` event silently became
	// before this existed.
	env.RegisterActivityWithOptions(
		func(_ context.Context, in handlers.ApprovalCreateInput) (map[string]interface{}, error) {
			id := in.NodePath
			if id == "" {
				id = in.StepID
				if in.LoopNodeID != "" && in.LoopNodeID != in.StepID {
					id = in.LoopNodeID + "." + in.StepID
				}
			}
			out := events.next(id)
			rec.recordCompleted(id, out)

			status, _ := out["status"].(string)
			if status == "" {
				// No scenario event for this node: treat it as approved rather
				// than hanging. A scenario that cares says so explicitly.
				status = "approved"
			}
			actionTaken, _ := out["action_taken"].(string)
			approvalID, _ := out["approval_id"].(string)
			if approvalID == "" {
				approvalID = "scenario-approval-" + id
			}
			return map[string]interface{}{
				"approval_id":      approvalID,
				"already_resolved": true,
				"status":           status,
				"action_taken":     actionTaken,
			}, nil
		},
		activity.RegisterOptions{Name: "ApprovalCreate"},
	)

	// An ask_question node is the same shape as approval: it runs no activity
	// named for its node type. It creates a row via QuestionCreate and then
	// BLOCKS on signal.question.<id> that only a human sends, so the node is
	// observable here only at QuestionCreate, and a scenario can only resolve
	// it through this mock's already_resolved path — the same short-circuit a
	// replay takes.
	//
	// Answering here is what makes an ask_question scenario runnable offline.
	// Stubbing QuestionCreate to an empty map instead leaves the workflow
	// waiting for a signal nobody sends; the test environment then auto-fires
	// the 24h timer, the node resolves as a TIMEOUT, and has_feedback comes
	// back false no matter what the scenario said. That silently turned
	// `has_feedback: true` into "the user did not answer" and exited the agent
	// loop after one iteration.
	//
	// The response_data shape mirrors what the question service really signals
	// (verified against the recorded history in
	// replaytest/fixtures/pause_resume.json), so it is parsed by the SAME
	// parseQuestionResponse the production path uses rather than bypassing it.
	env.RegisterActivityWithOptions(
		func(_ context.Context, in handlers.QuestionCreateInput) (map[string]interface{}, error) {
			id := in.NodePath
			if id == "" {
				id = in.StepID
				if in.LoopNodeID != "" && in.LoopNodeID != in.StepID {
					id = in.LoopNodeID + "." + in.StepID
				}
			}
			out := events.next(id)
			rec.recordCompleted(id, out)

			hasFeedback, _ := out["has_feedback"].(bool)
			answer := map[string]interface{}{"question": "", "selected": []string{"Continue"}, "freetext": ""}
			if hasFeedback {
				response, _ := out["response"].(string)
				if response == "" {
					response = "scenario feedback"
				}
				answer = map[string]interface{}{
					"question": "", "selected": []string{"Provide feedback"}, "freetext": response,
				}
			}
			responseData, err := json.Marshal(map[string]interface{}{
				"answers": []map[string]interface{}{answer},
			})
			if err != nil {
				return nil, fmt.Errorf("marshal scenario question response: %w", err)
			}
			return map[string]interface{}{
				"question_id":      "scenario-question-" + id,
				"already_resolved": true,
				"response_data":    string(responseData),
			}, nil
		},
		activity.RegisterOptions{Name: "QuestionCreate"},
	)

	for _, name := range infrastructureActivities {
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{}, nil
			},
			activity.RegisterOptions{Name: name},
		)
	}

	// FailStep is how the real runtime surfaces a step-level defect (unknown
	// node type, a workflow/approval node that reached StepExecutor). It MUST
	// fail: stubbing it to success would convert exactly the bugs this backend
	// exists to catch into green runs.
	env.RegisterActivityWithOptions(
		func(_ context.Context, in handlers.FailStepInput) (map[string]interface{}, error) {
			return nil, fmt.Errorf("%s", in.Error)
		},
		activity.RegisterOptions{Name: "FailStep"},
	)

	// Every node-backed activity resolves its mock from the scenario, keyed by
	// the qualified node id the REAL runtime context reports.
	//
	// The mock is normalized through schema.GetOutputDefaults — the SAME
	// function StepExecutor.normalizeOutput applies to a real activity result.
	// Without it a scenario that omits a field the workflow's CEL reads (e.g.
	// call_llm's compaction_threshold) fails on a missing key here while
	// passing in the simulator, which normalizes identically.
	for name := range nodeActivityNames(r.workflow) {
		activityName := name
		// ExecuteRunStep is dispatched with a FLAT map carrying step_id /
		// loop_node_id (step_executor.go startRun), NOT the types.ActivityInput
		// envelope every other node activity uses. Decoding it as ActivityInput
		// yields a zero-valued Runtime, so a `run` node resolved to the empty
		// id: its scenario event went unconsumed and "" was recorded as a
		// reached node. Register the shape the runtime actually sends.
		if activityName == "ExecuteRunStep" {
			env.RegisterActivityWithOptions(
				func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
					stepID, _ := in["step_id"].(string)
					loopNodeID, _ := in["loop_node_id"].(string)
					id := stepID
					if loopNodeID != "" && loopNodeID != stepID {
						id = loopNodeID + "." + stepID
					}
					out := normalizeOutput(events.next(id), activityName)
					rec.recordCompleted(id, out)
					return out, nil
				},
				activity.RegisterOptions{Name: activityName},
			)
			continue
		}
		env.RegisterActivityWithOptions(
			func(_ context.Context, in types.ActivityInput) (map[string]interface{}, error) {
				id := qualifiedNodeID(in.Runtime)
				// An inline save_message runs as a SaveMessage activity under a
				// synthetic "<node>-save" step id (save_message.go:611). It is a
				// side effect of the owning node, not a graph node a scenario
				// can name, so it must not appear in reached/completed.
				if strings.HasSuffix(in.Runtime.StepID, "-save") {
					return normalizeOutput(map[string]interface{}{}, activityName), nil
				}
				// A thread-inject SaveMessage (child_workflow_init.go) carries
				// a RuntimeContext with no StepID at all: it seeds a child
				// thread and belongs to no graph node. Recording it put an
				// EMPTY STRING in reached — an id nothing can match, which then
				// appeared as a phantom entry in every mismatch message.
				if in.Runtime.StepID == "" {
					return normalizeOutput(map[string]interface{}{}, activityName), nil
				}
				out := normalizeOutput(events.next(id), activityName)
				// in.Node is the node with its args already CEL-evaluated by
				// the real runtime, which is what carries the explicit
				// compaction_threshold when the workflow sets one.
				runtime.ApplyMockedCompactionThreshold(out, in.Node)
				rec.recordCompleted(id, out)
				return out, nil
			},
			activity.RegisterOptions{Name: activityName},
		)
	}
	return nil
}

// normalizeOutput fills a scenario's partial mock out to the activity's full
// output field set, mirroring StepExecutor.normalizeOutput.
func normalizeOutput(raw map[string]interface{}, activityName string) map[string]interface{} {
	if raw == nil {
		raw = map[string]interface{}{}
	}
	defaults := schema.GetOutputDefaults(activityName)
	if defaults == nil {
		return raw
	}
	out := make(map[string]interface{}, len(defaults)+len(raw))
	for k, v := range defaults {
		out[k] = v
	}
	for k, v := range raw {
		out[k] = v
	}
	return out
}
