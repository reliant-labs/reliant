// Copyright (c) 2025 Reliant Labs

// Package runner executes workflow scenarios against the REAL DynamicWorkflow
// inside an in-memory Temporal TestWorkflowEnvironment — no server, no DB, no
// network. It is the ONLY scenario engine: the CLI, the RunScenario RPC, the
// run_scenario agent tool and the builtin-scenario tests all run here.
//
// Only the ACTIVITY layer is mocked. Everything between "here are the node
// outputs" and "here is what happened" is production code: StepExecutor
// dispatch, the real loop executors, inline workflow/approval handling,
// node-condition skips, CEL scope, save_message resolution and workflow-output
// evaluation. A scenario therefore proves the runtime works, not that some
// second model of it agrees.
//
// Production safety, since this runs inside API processes:
//   - every Run gets a fresh TestWorkflowEnvironment (no shared state between
//     runs; the only process-global state touched is the read-only activity
//     schema registry populated at init);
//   - every activity the workflow can dispatch is either a scenario mock, an
//     inert infrastructure stub, or — for anything else — a dynamic catch-all
//     that FAILS the run naming the activity, so an unmocked activity can
//     never become a real call;
//   - panics in the workflow or a mock are recovered and reported as a
//     scenario error;
//   - each run has a wall-clock budget (Options.Timeout / context deadline).
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
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
	"github.com/reliant-labs/reliant/internal/workflow/scenario"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// Result is the outcome of running one scenario.
type Result = scenario.ScenarioResult

// WorkflowLoader resolves a workflow ref ("builtin://agent",
// "project://deploy", a bare slug) to its definition. It is how a scenario
// reaches sub-workflows the workflow under test references.
type WorkflowLoader func(ref string) (*reliantv1.Workflow, error)

// DefaultTimeout is the wall-clock budget for one scenario when neither
// Options.Timeout nor the context sets one. Builtin scenarios settle in tens of
// milliseconds; anything still running after this is not slow, it is not
// terminating.
const DefaultTimeout = 20 * time.Second

// Options configures a Runner.
type Options struct {
	// Loader resolves referenced sub-workflows. Nil means builtins only.
	Loader WorkflowLoader
	// Timeout bounds one scenario's wall-clock time. Zero means
	// DefaultTimeout.
	Timeout time.Duration
}

// Runner executes scenarios for a single workflow definition against
// DynamicWorkflow. It is safe for concurrent use: all per-run state lives in
// Run.
type Runner struct {
	workflow *reliantv1.Workflow
	loader   WorkflowLoader
	timeout  time.Duration

	// omitActivity, when set, is a node activity left unregistered — a test
	// seam standing in for an activity the runtime gained without a runner
	// mock, so the catch-all can be exercised.
	omitActivity string
}

// NewRunner builds a scenario runner for a workflow that resolves only
// builtin refs.
func NewRunner(wf *reliantv1.Workflow) *Runner {
	return New(wf, Options{})
}

// New builds a scenario runner with explicit options.
func New(wf *reliantv1.Workflow, opts Options) *Runner {
	loader := opts.Loader
	if loader == nil {
		loader = LoadBuiltinWorkflow
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Runner{workflow: wf, loader: loader, timeout: timeout}
}

// LoadBuiltinWorkflow resolves a builtin workflow by ref ("builtin://agent")
// or bare name ("agent").
func LoadBuiltinWorkflow(ref string) (*reliantv1.Workflow, error) {
	name := bareWorkflowName(ref)
	data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("workflow %q not found in builtins: %w", ref, err)
	}
	wf, err := wfyaml.ParseWorkflow(data)
	if err != nil {
		return nil, fmt.Errorf("parse builtin workflow %q: %w", name, err)
	}
	return wf, nil
}

// loadRef resolves a ref through the runner's loader. A builtin:// ref always
// resolves from the embedded builtins, whatever loader was configured.
func (r *Runner) loadRef(ref string) (*reliantv1.Workflow, error) {
	if strings.HasPrefix(ref, "builtin://") {
		return LoadBuiltinWorkflow(ref)
	}
	return r.loader(ref)
}

// RunAll runs scenarios sequentially and returns their results in order.
func (r *Runner) RunAll(ctx context.Context, scenarios []*scenario.Scenario) []*Result {
	results := make([]*Result, len(scenarios))
	for i, sc := range scenarios {
		results[i] = r.RunContext(ctx, sc)
	}
	return results
}

// runWorkflow is the graph a scenario actually executes: the workflow under
// test with every ref the scenario did not open replaced, at the node, by its
// stand-in body. See inlineBlackBoxedRefs for why the substitution has to
// happen here rather than in the loader activity.
func (r *Runner) runWorkflow(sc *scenario.Scenario) *reliantv1.Workflow {
	openPaths := transparentRefPaths(r.workflow, sc.Events, r.loadRef)
	return inlineBlackBoxedLoops(
		inlineBlackBoxedRefs(r.workflow, openPaths, blackBoxOutputKeys(r.workflow, sc.Events, openPaths, r.loadRef), r.loadRef),
		sc.Events)
}

// recorder accumulates what the real run actually did, in the shape the shared
// expectation checker reads.
//
// Every field here is written from an activity mock, i.e. from the workflow's
// own dispatch decisions — never from a graph walk this package performs. If
// this package ever starts deciding which node runs next, it has become a
// second workflow engine and the coverage it appears to provide is fake.
type recorder struct {
	mu sync.Mutex

	reached   []string
	completed []string
	skipped   []string
	seen      map[string]bool
	states    map[string]scenario.NodeExecutionState
	outputs   map[string]map[string]interface{}

	// loopIterations is the highest loop_iteration a WorkflowCheckpoint
	// reported for each loop node, +1 — i.e. how many iterations that loop
	// actually ran. A loop dispatches no activity of its own, so this
	// per-iteration checkpoint is the only place the real runtime tells us,
	// and without it `_iterations` on a loop node is unassertable.
	loopIterations map[string]int

	// unconsumed tracks scenario events that no activity ever asked for.
	consumed map[string]int

	// saved is every message the run saved, in order, as the runtime resolved
	// it (see recordSaved).
	saved []scenario.SavedMessage

	// routerEvents holds the `outputs` a preset router's scenario event
	// supplied for its selected workflow, keyed by router path, so the
	// black-boxed child can return them (see workflowRoutingDecisionOutput).
	routerEvents map[string]map[string]interface{}

	// stepError is the first step-level failure the runtime reported
	// (FailStep): which node, and why.
	stepErrorNode, stepErrorMessage string
}

func newRecorder() *recorder {
	return &recorder{
		seen:           map[string]bool{},
		states:         map[string]scenario.NodeExecutionState{},
		outputs:        map[string]map[string]interface{}{},
		loopIterations: map[string]int{},
		consumed:       map[string]int{},
		routerEvents:   map[string]map[string]interface{}{},
	}
}

func (r *recorder) recordStepError(nodePath, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stepErrorNode != "" || r.stepErrorMessage != "" {
		return
	}
	r.stepErrorNode, r.stepErrorMessage = nodePath, msg
	if nodePath != "" {
		r.markAncestorsReached(nodePath)
		r.markReachedExact(nodePath)
		r.states[nodePath] = scenario.StateError
	}
}

func (r *recorder) markRouterEvent(routerID string, mock map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markReached(routerID)
	outputs, _ := mock["outputs"].(map[string]interface{})
	if outputs == nil {
		outputs = map[string]interface{}{}
	}
	r.routerEvents[routerID] = outputs
}

func (r *recorder) routerChildOutputs(routerID string) (map[string]interface{}, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out, ok := r.routerEvents[routerID]
	return out, ok
}

func (r *recorder) markReached(id string) {
	// The stand-in node of a black-boxed sub-workflow is harness scaffolding,
	// not a graph node a scenario can name. Reporting it would put an id in
	// reached that no workflow contains. Its ANCESTORS are still real, and
	// reaching the stand-in is precisely the proof that the ref node above it
	// was entered — so strip the scaffolding and keep the path.
	if id == "" {
		return
	}
	if strings.HasSuffix(id, blackBoxNodeID) {
		id = strings.TrimSuffix(strings.TrimSuffix(id, blackBoxNodeID), ".")
		if id == "" {
			return
		}
	}

	// A qualified path is self-describing: "implementations.impl.agent_loop"
	// could only have been produced by entering `implementations`, then `impl`,
	// then `agent_loop`. Recording those ancestors is how a STRUCTURAL node —
	// a loop or a `workflow` node — becomes observable at all, because it
	// dispatches no activity of its own and so no mock can ever see it.
	//
	// This reads the path the REAL executors composed (types.RuntimeContext's
	// NodePath, built at every nesting boundary); it does not walk the graph or
	// decide what runs next, which is the line this package must not cross.
	//
	// WorkflowCheckpoint covers only some of these, and cannot be made to cover
	// the rest: DynamicWorkflow checkpoints top-level non-loop nodes, and the
	// SEQUENTIAL loop executor checkpoints per iteration — but the PARALLEL one
	// has no iteration checkpoint at all (deliberately: a checkpoint means
	// "resume here", and concurrent iterations have no single resume position).
	// So parallel-compete's `implementations` announced itself nowhere, and its
	// absence from `reached` read as "the loop never ran" when in fact all three
	// iterations had completed.
	r.markAncestorsReached(id)
	r.markReachedExact(id)
}

// markAncestorsReached records every enclosing node of a dotted path, without
// recording the node itself. Splitting this out of markReached is what lets a
// SKIPPED node credit its ancestors — the parent really was entered — while
// staying out of `reached` itself.
func (r *recorder) markAncestorsReached(id string) {
	if strings.HasSuffix(id, blackBoxNodeID) {
		id = strings.TrimSuffix(strings.TrimSuffix(id, blackBoxNodeID), ".")
	}
	for i, c := range id {
		if c == '.' {
			r.markReachedExact(id[:i])
		}
	}
}

func (r *recorder) markReachedExact(id string) {
	if !r.seen[id] {
		r.seen[id] = true
		r.reached = append(r.reached, id)
	}
}

// snapshotReached copies what has been reached so far. Used on the
// non-termination path, where the workflow goroutine is still writing — reading
// r.reached directly there is a data race.
// recordSaved records one saved message under the node that saved it.
func (r *recorder) recordSaved(node, role, content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saved = append(r.saved, scenario.SavedMessage{Node: node, Role: role, Content: content})
}

func (r *recorder) snapshotReached() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reached...)
}

func (r *recorder) recordCompleted(id string, out map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markReached(id)
	r.states[id] = scenario.StateCompleted
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

// recordSkipped records a node whose condition evaluated false.
//
// A skipped node is NOT reached. That is what makes `not_reached:` mean
// anything: one-ring's `write_tests` is scheduled and condition-skipped on
// every run, so counting it as reached would turn `not_reached: [write_tests]`
// into an assertion that can never hold.
//
// The node's ANCESTORS are still reached: reaching a skipped node's parent is
// what scheduled it, and the parent's own entry is a real event. Only the
// skipped node itself is withheld.
func (r *recorder) recordSkipped(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markAncestorsReached(id)
	r.states[id] = scenario.StateSkipped
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

// workflowRoutingSuffix is the id RouterExecutor.makeRoutingDecision appends to
// a WORKFLOW-mode (preset) router's id for the synthetic CallLLM that carries
// its decision (router_executor.go).
const workflowRoutingSuffix = "__routing_decision"

// workflowRoutingDecisionRouter recognises a preset router's synthetic
// routing step and returns the router's qualified id.
func workflowRoutingDecisionRouter(qualifiedID string) (string, bool) {
	if !strings.HasSuffix(qualifiedID, workflowRoutingSuffix) {
		return "", false
	}
	routerID := strings.TrimSuffix(qualifiedID, workflowRoutingSuffix)
	if routerID == "" || strings.HasSuffix(routerID, ".") {
		return "", false
	}
	return routerID, true
}

// workflowRoutingDecisionOutput answers a preset router's synthetic CallLLM
// from the ROUTER's scenario event — the same treatment node routers get.
//
// The scenario writes the decision in the router's OUTPUT vocabulary, which is
// also what it asserts on:
//
//   - node: route
//     output: {selected_workflow: builtin://agent, selected_preset: general,
//     prompt: "...", reasoning: "...", outputs: {response_text: "..."}}
//
// The real RouterExecutor parses response_data as {workflow, preset, prompt,
// reasoning}, so the event is translated into that wire shape. The decision is
// then validated and the selected workflow executed by production code; it
// runs black-boxed (the scenario does not target its internals), and
// `outputs` is what its stand-in returns — which becomes the router's
// `outputs` field exactly as a real child's outputs would.
//
// The router itself is NOT recorded here: it is a structural node, and its real
// output is reported through the structural-completion query after the run.
func workflowRoutingDecisionOutput(
	events *eventTable,
	rec *recorder,
	routerID string,
	activityName string,
) (map[string]interface{}, error) {
	mock := events.next(routerID)
	rec.markRouterEvent(routerID, mock)

	out := normalizeOutput(map[string]interface{}{}, activityName)
	if len(mock) == 0 {
		return out, nil
	}
	// Decision fields: the router's output names (selected_*) map onto the
	// routerDecision wire keys; the wire keys themselves pass through.
	const wfKey, presetKey = "work" + "flow", "preset"
	decision := map[string]interface{}{}
	for from, to := range map[string]string{
		"selected_workflow": wfKey, "selected_preset": presetKey,
		wfKey: wfKey, presetKey: presetKey,
		"prompt": "prompt", "reasoning": "reasoning",
	} {
		if v, ok := mock[from]; ok {
			decision[to] = v
		}
	}
	responseData, err := structpb.NewStruct(decision)
	if err != nil {
		return nil, fmt.Errorf("scenario router mock for %q is not valid response_data: %w", routerID, err)
	}
	out["response_data"] = responseData.AsMap()
	return out, nil
}

// nodeRoutingSuffix is the id RouterExecutor.executeNodeRouting appends to a
// node-router's own id when it builds the synthetic CallLLM node that carries
// the routing decision (router_executor.go).
const nodeRoutingSuffix = "__node_routing_decision"

// nodeRoutingDecisionRouter recognises the synthetic routing step and returns
// the id of the ROUTER that dispatched it.
//
// A node router runs no activity under its own name. It dispatches a CallLLM
// step called "<router>__node_routing_decision" and reads selected_node out of
// that call's response_data — so the scenario's event, which names the router
// ("classify"), can only be delivered by mapping the synthetic id back.
// Without this the router's mock goes unconsumed, the CallLLM returns an empty
// output, and parseNodeRoutingDecision fails the run with "node routing
// decision has no response_data or response_text".
//
// The suffix is stripped off the LAST path segment so a router nested in a
// sub-workflow ("planning.classify__node_routing_decision") maps to the
// qualified router id a scenario would write ("planning.classify").
func nodeRoutingDecisionRouter(qualifiedID string) (string, bool) {
	if !strings.HasSuffix(qualifiedID, nodeRoutingSuffix) {
		return "", false
	}
	routerID := strings.TrimSuffix(qualifiedID, nodeRoutingSuffix)
	if routerID == "" || strings.HasSuffix(routerID, ".") {
		return "", false
	}
	return routerID, true
}

// nodeRoutingDecisionOutput answers the synthetic routing CallLLM from the
// ROUTER's scenario event, reshaped into the CallLLM output the real
// RouterExecutor parses.
//
// The scenario writes the router's decision flat:
//
//   - node: classify
//     output: {selected_node: refine_prompt, reasoning: "..."}
//
// The real runtime does not read that shape. parseNodeRoutingDecision unmarshals
// CallLLMOutput.response_data, so the flat mock is nested under response_data
// here. That is a translation of the SAME scenario event into the wire shape
// production really carries, not a second source of routing truth: the decision
// still comes from the scenario, and an unmocked router still gets an empty
// response_data and fails exactly as production does when the LLM returns
// nothing. There is deliberately no "default to the first candidate" fallback
// — silently defaulting is what explicit router mocks exist to stop.
//
// The router is recorded as reached/completed under its OWN id, with the flat
// output, so `reached: [classify]` and `node_outputs.classify.selected_node`
// mean what a scenario author expects. The synthetic step is not recorded at
// all: it is runtime scaffolding, not a graph node a scenario can name.
func nodeRoutingDecisionOutput(
	events *eventTable,
	rec *recorder,
	routerID string,
	activityName string,
) (map[string]interface{}, error) {
	decision := events.next(routerID)
	rec.recordCompleted(routerID, decision)

	out := normalizeOutput(map[string]interface{}{}, activityName)
	if len(decision) > 0 {
		responseData, err := structpb.NewStruct(decision)
		if err != nil {
			return nil, fmt.Errorf("scenario router mock for %q is not valid response_data: %w", routerID, err)
		}
		out["response_data"] = responseData.AsMap()
	}
	return out, nil
}

// eventTable indexes scenario events: node-targeted events are consumed in
// order per node, and untargeted events are consumed sequentially.
type eventTable struct {
	mu         sync.Mutex
	byNode     map[string][]scenario.SimulatedEvent
	nodeIdx    map[string]int
	sequential []scenario.SimulatedEvent
	seqIdx     int

	// overrun counts, per node, how many times the runtime dispatched a node
	// AFTER its scenario events were used up. See next() for why this is
	// recorded rather than raised.
	overrun map[string]int

	// taken marks events consumed by index (nextAt) rather than in order.
	taken map[string]map[int]bool
}

func newEventTable(events []scenario.SimulatedEvent) *eventTable {
	t := &eventTable{
		byNode:  map[string][]scenario.SimulatedEvent{},
		nodeIdx: map[string]int{},
		overrun: map[string]int{},
		taken:   map[string]map[int]bool{},
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
			return scenario.EventOutput(events[i])
		}
	}
	if t.seqIdx < len(t.sequential) {
		e := t.sequential[t.seqIdx]
		t.seqIdx++
		return scenario.EventOutput(e)
	}
	if mocked {
		t.overrun[nodeID]++
	}
	return map[string]interface{}{}
}

// nextAt returns the index-th event for nodeID. It answers the iterations of a
// PARALLEL loop: they run concurrently, so "the next event" would hand them
// out in whatever order their activities happened to start — a race. The
// iteration index is fixed by the loop, so event i goes to iteration i,
// deterministically, and matches the order the scenario lists them in.
func (t *eventTable) nextAt(nodeID string, index int) map[string]interface{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	events := t.byNode[nodeID]
	if index < 0 || index >= len(events) {
		if len(events) > 0 {
			t.overrun[nodeID]++
		}
		return map[string]interface{}{}
	}
	if t.taken[nodeID] == nil {
		t.taken[nodeID] = map[int]bool{}
	}
	t.taken[nodeID][index] = true
	return scenario.EventOutput(events[index])
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
		for i := 0; i < len(t.byNode[n]); i++ {
			if i < t.nodeIdx[n] || t.taken[n][i] {
				continue
			}
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

// Run executes one scenario with the runner's default budget. See RunContext.
func (r *Runner) Run(sc *scenario.Scenario) *Result {
	return r.RunContext(context.Background(), sc)
}

// RunContext executes one scenario against DynamicWorkflow in a fresh
// TestWorkflowEnvironment and evaluates the scenario's expectation against what
// really happened. It never panics and always returns within the runner's
// timeout (or ctx's deadline, whichever is sooner).
func (r *Runner) RunContext(ctx context.Context, sc *scenario.Scenario) (result *Result) {
	start := time.Now()
	defer func() {
		if p := recover(); p != nil {
			result = errorResult(sc, fmt.Errorf("scenario runner panicked: %v", p), start)
		}
	}()

	if validation := scenario.ValidateScenario(sc, r.workflow); !validation.Valid {
		res := errorResult(sc, fmt.Errorf("validation failed: %s", strings.Join(validation.Errors, "; ")), start)
		res.Mismatches = validation.Errors
		return res
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	var suite testsuite.WorkflowTestSuite
	// The runner executes inside API processes on every builder click; the
	// workflow's own INFO chatter (one line per iteration, with full node
	// outputs) is not something those processes should emit. The result
	// carries everything a caller needs.
	suite.SetLogger(quietLogger{})
	env := suite.NewTestWorkflowEnvironment()
	// Idle budget: a workflow blocked on nothing (a signal no mock sends, a
	// timer the env will not fire) makes the env's own event loop give up.
	env.SetTestTimeout(r.timeout)

	rec := newRecorder()
	events := newEventTable(sc.Events)
	guard := &runGuard{}

	if err := r.registerActivities(env, rec, events, guard, sc); err != nil {
		return errorResult(sc, err, start)
	}

	// project_path is a real production input, not harness scaffolding: it is
	// what ChatService.buildWorkflowInputs injects, and preset loading fails
	// terminally without it. A scenario may override it like any other input.
	inputs := map[string]interface{}{"project_path": scenarioProjectPath}
	for k, v := range sc.Inputs {
		inputs[k] = v
	}

	// A scenario that does not terminate must FAIL, not hang. The bound is
	// wall-clock rather than an iteration count because the loop lives inside
	// DynamicWorkflow, which this package deliberately does not reach into.
	var runPanic interface{}
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		defer func() { runPanic = recover() }()
		env.ExecuteWorkflow(runtime.DynamicWorkflow, runtime.WorkflowInput{
			ChatID:       "scenario-chat",
			WorkflowName: r.workflow.GetName(),
			Inputs:       inputs,
			Resume:       resumeInput(sc),
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
	case <-ctx.Done():
		// A workflow that keeps dispatching (a loop whose while never goes
		// false) is never idle, so the env's idle timeout cannot stop it.
		// Tripping the guard makes every further activity fail
		// non-retryably, which unwinds the workflow; the env then exits and
		// the goroutine ends rather than leaking. Wait briefly for that so a
		// long-lived API process does not accumulate spinning workflows.
		guard.trip()
		select {
		case <-runDone:
		case <-time.After(2 * time.Second):
		}
		msg := nonTerminatingMessage
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			msg = "scenario run cancelled: " + ctx.Err().Error()
		}
		return &Result{
			Status:   scenario.StatusError,
			Scenario: sc.Name,
			Execution: scenario.ExecutionDetails{
				Outcome:      "error",
				NodesReached: rec.snapshotReached(),
				Error:        &scenario.ErrorDetails{Message: msg},
				DurationMs:   time.Since(start).Milliseconds(),
			},
			Expected:   sc.Expect,
			Mismatches: []string{msg},
			RunAt:      time.Now(),
		}
	}
	if runPanic != nil {
		msg := fmt.Sprint(runPanic)
		if strings.HasPrefix(msg, "test timeout") {
			// The env's idle timeout: the workflow blocked on something no
			// scenario event can satisfy.
			msg = nonTerminatingMessage + " (" + firstLine(msg) + ")"
		} else {
			msg = "workflow panicked: " + firstLine(msg)
		}
		res := errorResult(sc, errors.New(msg), start)
		res.Execution.NodesReached = rec.snapshotReached()
		res.Expected = sc.Expect
		return res
	}

	// A STRUCTURAL node — `loop` or `workflow` — publishes its outputs into the
	// workflow's node-output store and runs no activity of its own, so no mock
	// above can observe them. The runtime reports each completed structural
	// node's qualified path and published outputs through a query, and this
	// reads it.
	//
	// Read AFTER the run, from the workflow's own state, so it stays an
	// observation: the harness learns what the runtime computed and cannot
	// influence it. A node that never completed — it errored, or the run was
	// abandoned mid-iteration — is simply absent, which is the truthful
	// answer, and for a loop the checkpoint-derived `_iterations` fallback
	// below still covers it.
	//
	// This supplies BOTH halves the harness previously lacked: real per-node
	// outputs (get-it-right's `attempt` loop publishes `eval_strategy` and
	// `review_grade`, which no mock could see) and COMPLETION (one-ring's
	// `impl_loop`, a `workflow:` node, was reached but never completed).
	var structuralCompletions []runtime.StructuralCompletion
	if q, qErr := env.QueryWorkflow(runtime.StructuralNodesCompletedQuery); qErr == nil {
		if decodeErr := q.Get(&structuralCompletions); decodeErr == nil {
			for _, done := range structuralCompletions {
				rec.recordCompleted(done.Path, done.Outputs)
			}
		}
	}

	// Fallback for a loop the query did NOT report, i.e. one that was entered
	// but never completed. Its outputs are genuinely unknown, but the
	// per-iteration checkpoints still say how far it got, so `_iterations`
	// stays assertable — under the same field name the real runtime uses
	// (model.LoopOutputIterationsField). A loop the query DID report already
	// carries a real `_iterations` from the runtime itself and is skipped here.
	for nodeID, iterations := range rec.loopIterations {
		if _, isActivityNode := rec.outputs[nodeID]; isActivityNode {
			continue
		}
		rec.outputs[nodeID] = map[string]interface{}{model.LoopOutputIterationsField: iterations}
	}

	// A JOIN node is the last structural node no mock can see. It runs no
	// activity (every dispatch loop filters join steps out — a join's work is
	// its SOURCES completing), and unlike a loop or a `workflow` node it has no
	// children whose composed NodePath would reveal it. So the runtime reports
	// satisfied joins through a query, and this reads it.
	//
	// Read AFTER the run, from the workflow's own state, so it stays an
	// observation: the harness learns which joins the runtime decided were
	// satisfied, and cannot influence that decision. A join that was never
	// satisfied is simply absent, which is the truthful answer — it was not
	// reached.
	var satisfiedJoins []string
	if q, qErr := env.QueryWorkflow(runtime.JoinsSatisfiedQuery); qErr == nil {
		if decodeErr := q.Get(&satisfiedJoins); decodeErr == nil {
			for _, joinPath := range satisfiedJoins {
				// A satisfied join has done all the work a join does, so it
				// is completed, not merely reached.
				rec.recordCompleted(joinPath, map[string]interface{}{})
			}
		}
	}

	execution := scenario.ExecutionDetails{
		NodesReached:   rec.reached,
		NodesCompleted: rec.completed,
		NodesSkipped:   rec.skipped,
		NodeStates:     rec.states,
		NodeOutputs:    rec.outputs,
		DurationMs:     time.Since(start).Milliseconds(),
		// Non-nil even when empty: this backend observes saves, so "none
		// saved" is a real answer (see scenario.checkMessageExpectations).
		SavedMessages: append([]scenario.SavedMessage{}, rec.saved...),
	}

	if runErr := env.GetWorkflowError(); runErr != nil {
		execution.Outcome = "error"
		execution.Error = &scenario.ErrorDetails{Message: runErr.Error()}
		// A step-level failure is the cause; the workflow error is only how
		// the retry ladder surfaced it ("deadline exceeded"). Report the node
		// and the real reason.
		if rec.stepErrorMessage != "" {
			execution.Error = &scenario.ErrorDetails{
				Node:    rec.stepErrorNode,
				Message: rec.stepErrorMessage + " (" + runErr.Error() + ")",
			}
		}
	} else {
		execution.Outcome = "completed"
		var wfResult *runtime.WorkflowResult
		if err := env.GetWorkflowResult(&wfResult); err == nil && wfResult != nil {
			execution.WorkflowOutputs = wfResult.Outputs
		}
	}

	result = &Result{
		Scenario:  sc.Name,
		Execution: execution,
		Expected:  sc.Expect,
		RunAt:     time.Now(),
	}

	mismatches := append(events.unconsumed(), guard.unmockedActivities()...)
	if sc.Expect != nil {
		mismatches = append(mismatches, scenario.CheckExpectations(sc.Expect, &execution)...)
	}
	result.Mismatches = mismatches
	result.Warnings = scenario.AnalyzeFalsePasses(sc, r.workflow, &execution, scenario.WorkflowRefLoader(r.loadRef))

	switch {
	case len(mismatches) > 0:
		result.Status = scenario.StatusFailed
	case sc.Expect == nil && execution.Outcome == "error":
		result.Status = scenario.StatusError
	default:
		result.Status = scenario.StatusPassed
	}
	return result
}

// quietLogger discards the Temporal SDK's workflow and activity logging.
type quietLogger struct{}

func (quietLogger) Debug(string, ...interface{}) {}
func (quietLogger) Info(string, ...interface{})  {}
func (quietLogger) Warn(string, ...interface{})  {}
func (quietLogger) Error(string, ...interface{}) {}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// runGuard is the per-run kill switch and unmocked-activity ledger shared by
// every activity mock of one run.
type runGuard struct {
	mu       sync.Mutex
	tripped  bool
	unmocked map[string]int
}

func (g *runGuard) trip() {
	g.mu.Lock()
	g.tripped = true
	g.mu.Unlock()
}

// check fails an activity once the run has been abandoned, so a workflow that
// never idles still unwinds instead of spinning on after the caller returned.
func (g *runGuard) check() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tripped {
		return temporal.NewNonRetryableApplicationError("scenario run abandoned (budget exceeded)", "ScenarioAbandoned", nil)
	}
	return nil
}

func (g *runGuard) recordUnmocked(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.unmocked == nil {
		g.unmocked = map[string]int{}
	}
	g.unmocked[name]++
}

func (g *runGuard) unmockedActivities() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	names := make([]string, 0, len(g.unmocked))
	for n := range g.unmocked {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, fmt.Sprintf(
			"workflow dispatched activity %q, which the scenario runner does not mock (%d call(s)); it was failed rather than executed", n, g.unmocked[n]))
	}
	return out
}

func errorResult(sc *scenario.Scenario, err error, start time.Time) *Result {
	return &Result{
		Status:   scenario.StatusError,
		Scenario: sc.Name,
		Execution: scenario.ExecutionDetails{
			Outcome:    "error",
			Error:      &scenario.ErrorDetails{Message: err.Error()},
			DurationMs: time.Since(start).Milliseconds(),
		},
		Mismatches: []string{err.Error()},
		RunAt:      time.Now(),
	}
}

// nodeActivities is every activity name a graph node can dispatch, derived
// from the node-type registry through the runtime's own name mapping — so it is
// complete by construction and cannot drift from the dispatcher. Registering
// the whole set (rather than walking one graph) means a sub-workflow body
// loaded at run time can never hit an unregistered node activity.
func nodeActivities() []string {
	seen := map[string]bool{}
	var names []string
	for nodeType := range model.DiscoverNodeMetas() {
		name := runtime.NodeActivityName(&reliantv1.Node{Type: nodeType})
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
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
	// Per-thread lifecycle bookkeeping written by the inline workflow
	// executor for every sub-workflow thread; never read back by the run.
	"ThreadStatus",
	// A completed spawn's result delivery: the parent reads the child
	// thread's final message and posts it to its own mailbox. The spawned
	// child ran (or was black-boxed) inside this same run, so there is no
	// thread row to read; an empty, successful delivery is the faithful
	// answer. Failing these instead told the parent its spawn had FAILED,
	// which changed the loop's control flow for a reason no scenario wrote.
	"FetchThreadResult",
	"EnqueueAgentMessage",
}

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

// scenarioProjectPath is the working directory every scenario run executes in.
//
// It must be NON-EMPTY. Preset loading treats an unset project path as a
// TerminalError ("project path not set, cannot load presets"), and it is
// checked before any preset is fetched — so a node carrying `presets:` fails
// during setup rather than executing. That is not a product defect: production
// always supplies this, injected as the `project_path` workflow input by
// ChatService.buildWorkflowInputs, which is exactly what this mirrors. The
// harness simply never set it, so every ref node with presets — parallel-compete's
// `review` and `synthesizer`, get-it-right's `attempt.review` — died in setup and
// took the whole run's outcome with it.
//
// The path never has to exist on disk: LoadPresetParams is stubbed to an empty
// map like every other infrastructure activity, so nothing reads it.
const scenarioProjectPath = "/scenario/project"

// transparentRefs decides, for one scenario, which referenced sub-workflows are
// executed for real instead of being replaced by a black box.
//
// The gate is load-bearing: executing an UNMOCKED `builtin://structured-agent`
// does not merely
// produce wrong output, it does not terminate. Its loop runs
// `while: (outputs.completed != true || ...) && (inputs.max_turns == 0 || ...)`,
// `completed` is computed from a response tool that an empty mock never reports,
// and max_turns defaults to 0 meaning UNLIMITED. Opening every ref by default is
// what previously produced a measured 766,072-iteration runaway.
//
// So the rule, as docs/workflows/testing.mdx states it, is: a
// ref is a black box unless the scenario targets its INTERNALS with a qualified
// id. An event on the ref node itself ("review") mocks that node and leaves its
// body opaque; an event strictly beneath it ("review.agent_loop.call_llm") is the
// author saying the body is what is under test.
//
// The decision is keyed by sub-workflow NAME rather than by node path because
// the loader activity is the only place a body can be substituted, and all the
// runtime tells it is `workflow_name` — no node path reaches it. A name is
// therefore opened when ANY node referencing it is targeted internally, which is
// the direction that preserves the gate's purpose: it never opens a name no
// scenario asked about.
func transparentRefs(wf *reliantv1.Workflow, events []scenario.SimulatedEvent, load WorkflowLoader) map[string]bool {
	targeted := make(map[string]bool, len(events))
	for _, e := range events {
		if e.Node != "" {
			targeted[e.Node] = true
		}
	}
	// A ref is transparent when some event names a node strictly inside it.
	hasInternalEvent := func(nodePath string) bool {
		prefix := nodePath + "."
		for node := range targeted {
			if strings.HasPrefix(node, prefix) {
				return true
			}
		}
		return false
	}

	transparent := map[string]bool{}
	// visited guards against a ref cycle; a workflow that (transitively)
	// references itself would otherwise recurse forever.
	visited := map[string]bool{}

	var walk func(nodes []*reliantv1.Node, prefix string)
	walk = func(nodes []*reliantv1.Node, prefix string) {
		for _, n := range nodes {
			nodePath := joinScenarioNodePath(prefix, n.GetId())

			// An inline body is part of THIS graph: its nodes carry the parent's
			// path prefix and it is always executed, so descend unconditionally.
			if inline := model.NodeInlineWorkflow(n); inline != nil {
				walk(inline.GetNodes(), nodePath)
				continue
			}

			ref := model.NodeRef(n)
			if ref == "" || !hasInternalEvent(nodePath) {
				continue
			}
			name := bareWorkflowName(ref)
			transparent[name] = true

			// Descend into the ref's own graph so a body two levels down can be
			// opened too — one-ring reaches `impl_loop.attempt.implement` only
			// through get-it-right's body. Nodes inside the ref are addressed
			// from the REFERRING node's path, which is how the real runtime
			// composes NodePath at a sub-workflow boundary.
			if visited[name] {
				continue
			}
			visited[name] = true
			if sub, err := load(ref); err == nil {
				walk(sub.GetNodes(), nodePath)
			}
		}
	}
	walk(wf.GetNodes(), "")
	return transparent
}

// joinScenarioNodePath mirrors the runtime's own node-path composition
// (runtime.joinNodePath) so the ids computed here are the ids a scenario writes.
func joinScenarioNodePath(prefix, nodeID string) string {
	if prefix == "" {
		return nodeID
	}
	if nodeID == "" {
		return prefix
	}
	return prefix + "." + nodeID
}

// blackBoxNodeID is the id of the single node an opaque sub-workflow runs. It
// is deliberately not a name any scenario would write, and it is filtered out of
// the reached set so a scenario can neither see nor assert on it.
const blackBoxNodeID = "__scenario_black_box"

// blackBoxWorkflow returns a stand-in graph for a referenced sub-workflow whose
// body a scenario has not asked to execute: one inert save_message node. It
// keeps the parent's inputs so input binding still type-checks, and it cannot
// loop, block, or fail.
//
// outputKeys are the field names the scenario's mocks for black-boxed ref nodes
// actually carry, and declaring them is what makes a black box MOCKABLE rather
// than merely inert.
//
// Without them the sub-workflow declares no outputs, EvaluateWorkflowOutputs
// falls back to returning the raw node-output map, and the parent reads
// `nodes.review` as `{"__scenario_black_box": {...}}` — harness scaffolding
// where the scenario's `{response: {...}}` should be. Every downstream CEL then
// reads through a key that does not exist, which is why get-it-right's
// `eval_strategy` and `review_grade` came back missing while `attempt.review`
// sat in the reached list looking fine.
//
// Hoisting each key from the stand-in node makes the parent see exactly the map
// the scenario wrote. The `has()` guard
// is required because ONE stand-in graph serves every black-boxed ref in the
// run: a key another node's mock supplied is simply absent here, and must
// evaluate to null rather than fail the whole output evaluation.
func blackBoxWorkflow(wf *reliantv1.Workflow, outputKeys []string) *reliantv1.Workflow {
	var outputs map[string]string
	if len(outputKeys) > 0 {
		outputs = make(map[string]string, len(outputKeys))
		for _, k := range outputKeys {
			outputs[k] = fmt.Sprintf(
				"{{has(nodes.%[1]s.%[2]s) ? nodes.%[1]s.%[2]s : null}}", blackBoxNodeID, k)
		}
	}
	return &reliantv1.Workflow{
		Name:       wf.GetName(),
		ApiVersion: wf.GetApiVersion(),
		Inputs:     wf.GetInputs(),
		// Keep the preset tag: presets are validated against the workflow a
		// caller loads (a preset router checks its candidates' presets), so a
		// stand-in without it would reject every preset the real one accepts.
		Presets: wf.GetPresets(),
		Outputs: outputs,
		Entry:   []string{blackBoxNodeID},
		Nodes: []*reliantv1.Node{{
			Id:   blackBoxNodeID,
			Type: model.NodeTypeSaveMessage,
		}},
	}
}

// blackBoxedRefPaths returns the node paths of every `workflow` ref the run will
// replace with a black box — i.e. the ref nodes a scenario is entitled to mock
// as a UNIT, addressing the ref node itself.
//
// It walks exactly as transparentRefs does, and for the same reason: a ref two
// levels down is only reachable through the graph of the ref above it, and a
// node inside a ref is addressed from the REFERRING node's path.
func blackBoxedRefPaths(wf *reliantv1.Workflow, openPaths map[string]bool, load WorkflowLoader) map[string]bool {
	paths := map[string]bool{}
	visited := map[string]bool{}

	var walk func(nodes []*reliantv1.Node, prefix string)
	walk = func(nodes []*reliantv1.Node, prefix string) {
		for _, n := range nodes {
			nodePath := joinScenarioNodePath(prefix, n.GetId())
			if inline := model.NodeInlineWorkflow(n); inline != nil {
				walk(inline.GetNodes(), nodePath)
				continue
			}
			ref := model.NodeRef(n)
			if ref == "" {
				continue
			}
			if !openPaths[nodePath] {
				paths[nodePath] = true
				continue
			}
			name := bareWorkflowName(ref)
			if visited[name] {
				continue
			}
			visited[name] = true
			if sub, err := load(ref); err == nil {
				walk(sub.GetNodes(), nodePath)
			}
		}
	}
	walk(wf.GetNodes(), "")
	return paths
}

// blackBoxOutputKeys collects the field names the scenario's mocks for
// black-boxed ref nodes carry, so blackBoxWorkflow can hoist them.
//
// Only events targeting a black-boxed ref NODE contribute. An event aimed at an
// ordinary node inside a transparent body is answered by that node's own
// activity mock and has nothing to do with a sub-workflow's output contract.
func blackBoxOutputKeys(
	wf *reliantv1.Workflow,
	events []scenario.SimulatedEvent,
	openPaths map[string]bool,
	load WorkflowLoader,
) []string {
	refPaths := blackBoxedRefPaths(wf, openPaths, load)
	seen := map[string]bool{}
	var keys []string
	for _, e := range events {
		if e.Node == "" || !refPaths[e.Node] {
			continue
		}
		for k := range scenario.EventOutput(e) {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// parallelLoopPaths returns the node paths of every parallel loop in the graph
// (walking inline bodies).
func parallelLoopPaths(wf *reliantv1.Workflow) map[string]bool {
	paths := map[string]bool{}
	var walk func(nodes []*reliantv1.Node, prefix string)
	walk = func(nodes []*reliantv1.Node, prefix string) {
		for _, n := range nodes {
			nodePath := joinScenarioNodePath(prefix, n.GetId())
			if la := model.GetLoopArgs(n); la != nil && model.CelBoolValue(la.GetParallel()) {
				paths[nodePath] = true
			}
			if inline := model.NodeInlineWorkflow(n); inline != nil {
				walk(inline.GetNodes(), nodePath)
			}
		}
	}
	walk(wf.GetNodes(), "")
	return paths
}

// routerChildOutputKeys collects the `outputs` keys preset-router events
// supply for their selected (black-boxed) workflow, so the stand-in the loader
// returns for that workflow declares them.
func routerChildOutputKeys(wf *reliantv1.Workflow, events []scenario.SimulatedEvent) []string {
	routers := map[string]bool{}
	var walk func(nodes []*reliantv1.Node, prefix string)
	walk = func(nodes []*reliantv1.Node, prefix string) {
		for _, n := range nodes {
			nodePath := joinScenarioNodePath(prefix, n.GetId())
			if args := model.GetRouterArgs(n); args != nil && len(args.GetWorkflows()) > 0 {
				routers[nodePath] = true
			}
			if inline := model.NodeInlineWorkflow(n); inline != nil {
				walk(inline.GetNodes(), nodePath)
			}
		}
	}
	walk(wf.GetNodes(), "")

	var keys []string
	for _, e := range events {
		if !routers[e.Node] {
			continue
		}
		outputs, _ := e.Output["outputs"].(map[string]interface{})
		for k := range outputs {
			keys = append(keys, k)
		}
	}
	return keys
}

// mergeKeys returns the sorted union of key lists.
func mergeKeys(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, k := range l {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// blackBoxRefPath maps a stand-in node's path back to the ref node above it —
// "attempt.review.__scenario_black_box" -> "attempt.review". That parent path is
// the id the scenario writes, and resolving it here is what lets an event on the
// ref node be consumed at all.
func blackBoxRefPath(nodePath string) string {
	if !strings.HasSuffix(nodePath, blackBoxNodeID) {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSuffix(nodePath, blackBoxNodeID), ".")
}

// inlineBlackBoxedRefs rewrites the workflow so every ref the scenario did NOT
// target internally carries its stand-in body INLINE, at the node itself.
//
// This is what makes the transparency gate per-NODE. The loader activity is
// told only `workflow_name`, so a decision made there is necessarily keyed by
// sub-workflow name — and that is too coarse the moment one name is referenced
// twice with different intent. get-it-right is exactly that shape: `implement`
// and `refactor` are both `builtin://agent`, and a scenario that mocks
// `implement`'s internals while mocking `refactor` as a unit has to get a
// transparent body at one node and an opaque one at the other. Keyed by name,
// opening `agent` for `implement` also opened it for `refactor`, whose own mock
// then went unconsumed while its body ran off the scenario's events.
//
// Substituting in the graph moves the decision to the only place that knows the
// node path. An inline body is loaded directly by the executor and never
// consults the loader at all, so the two nodes can now differ. Refs that ARE
// transparent are left as refs and still resolve by name through the loader,
// where a name is the right key: the scenario asked for that body.
func inlineBlackBoxedRefs(
	wf *reliantv1.Workflow,
	openPaths map[string]bool,
	outputKeys []string,
	load WorkflowLoader,
) *reliantv1.Workflow {
	var rewrite func(nodes []*reliantv1.Node, prefix string) []*reliantv1.Node
	rewrite = func(nodes []*reliantv1.Node, prefix string) []*reliantv1.Node {
		out := make([]*reliantv1.Node, 0, len(nodes))
		for _, n := range nodes {
			nodePath := joinScenarioNodePath(prefix, n.GetId())

			if model.NodeInlineWorkflow(n) != nil {
				clone, ok := proto.Clone(n).(*reliantv1.Node)
				if !ok {
					out = append(out, n)
					continue
				}
				if sub := model.NodeInlineWorkflow(clone); sub != nil {
					sub.Nodes = rewrite(sub.GetNodes(), nodePath)
				}
				out = append(out, clone)
				continue
			}

			ref := model.NodeRef(n)
			if ref == "" || openPaths[nodePath] {
				// No ref, or the scenario targeted THIS node's internals — the
				// body it asked for is loaded by name through the loader.
				out = append(out, n)
				continue
			}

			sub, err := load(ref)
			if err != nil {
				// Unresolvable here; the loader activity still black-boxes
				// it by name.
				out = append(out, n)
				continue
			}
			clone, ok := proto.Clone(n).(*reliantv1.Node)
			if !ok {
				out = append(out, n)
				continue
			}
			args := clone.GetWorkflow()
			if args == nil {
				out = append(out, n)
				continue
			}
			args.Ref = nil
			args.Inline = blackBoxWorkflow(sub, outputKeys)
			out = append(out, clone)
		}
		return out
	}

	clone, ok := proto.Clone(wf).(*reliantv1.Workflow)
	if !ok {
		return wf
	}
	clone.Nodes = rewrite(clone.GetNodes(), "")
	return clone
}

// inlineBlackBoxedLoops replaces every loop node a scenario marked
// `black_box: true` with a `workflow` node of the same id whose inline body is
// the stand-in, so the loop is mocked AS A UNIT: its body never runs, and the
// event's output (`_results`, `_iterations`, declared outputs, …) becomes the
// node's output exactly as downstream CEL reads it.
//
// It mirrors inlineBlackBoxedRefs: the substitution happens in the graph, at the
// node, because that is the only place that knows the node path. The node keeps
// its id, condition, save_message and timeout, so everything around it — edges,
// skips, the node's own save — still runs through production code.
func inlineBlackBoxedLoops(wf *reliantv1.Workflow, events []scenario.SimulatedEvent) *reliantv1.Workflow {
	keysByPath := map[string][]string{}
	for _, e := range events {
		if !e.BlackBox || e.Node == "" {
			continue
		}
		seen := map[string]bool{}
		for _, k := range keysByPath[e.Node] {
			seen[k] = true
		}
		for k := range scenario.EventOutput(e) {
			if !seen[k] {
				seen[k] = true
				keysByPath[e.Node] = append(keysByPath[e.Node], k)
			}
		}
	}
	if len(keysByPath) == 0 {
		return wf
	}

	var rewrite func(nodes []*reliantv1.Node, prefix string) []*reliantv1.Node
	rewrite = func(nodes []*reliantv1.Node, prefix string) []*reliantv1.Node {
		out := make([]*reliantv1.Node, 0, len(nodes))
		for _, n := range nodes {
			nodePath := joinScenarioNodePath(prefix, n.GetId())
			keys, blackBoxed := keysByPath[nodePath]
			if blackBoxed && n.GetType() == model.NodeTypeLoop {
				sort.Strings(keys)
				clone, ok := proto.Clone(n).(*reliantv1.Node)
				if !ok {
					out = append(out, n)
					continue
				}
				clone.Type = model.NodeTypeWorkflow
				clone.Args = &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
					Inline: blackBoxWorkflow(&reliantv1.Workflow{Name: n.GetId()}, keys),
				}}
				out = append(out, clone)
				continue
			}
			if model.NodeInlineWorkflow(n) != nil {
				clone, ok := proto.Clone(n).(*reliantv1.Node)
				if !ok {
					out = append(out, n)
					continue
				}
				if sub := model.NodeInlineWorkflow(clone); sub != nil {
					sub.Nodes = rewrite(sub.GetNodes(), nodePath)
				}
				out = append(out, clone)
				continue
			}
			out = append(out, n)
		}
		return out
	}

	clone, ok := proto.Clone(wf).(*reliantv1.Workflow)
	if !ok {
		return wf
	}
	clone.Nodes = rewrite(clone.GetNodes(), "")
	return clone
}

// transparentRefPaths is transparentRefs's per-NODE twin: the set of ref node
// PATHS whose internals the scenario targeted.
//
// transparentRefs answers the loader's question ("may this NAME be loaded for
// real"), which is all the loader can act on. This answers the graph's question
// ("does THIS node get its real body"), which is the one that decides
// black-boxing now that the substitution happens at the node. They disagree
// exactly when one sub-workflow is referenced twice with different intent —
// get-it-right's `implement` and `refactor` are both `builtin://agent`.
func transparentRefPaths(wf *reliantv1.Workflow, events []scenario.SimulatedEvent, load WorkflowLoader) map[string]bool {
	targeted := make(map[string]bool, len(events))
	for _, e := range events {
		if e.Node != "" {
			targeted[e.Node] = true
		}
	}
	hasInternalEvent := func(nodePath string) bool {
		prefix := nodePath + "."
		for node := range targeted {
			if strings.HasPrefix(node, prefix) {
				return true
			}
		}
		return false
	}

	open := map[string]bool{}
	visited := map[string]bool{}

	var walk func(nodes []*reliantv1.Node, prefix string)
	walk = func(nodes []*reliantv1.Node, prefix string) {
		for _, n := range nodes {
			nodePath := joinScenarioNodePath(prefix, n.GetId())
			if inline := model.NodeInlineWorkflow(n); inline != nil {
				walk(inline.GetNodes(), nodePath)
				continue
			}
			ref := model.NodeRef(n)
			if ref == "" || !hasInternalEvent(nodePath) {
				continue
			}
			open[nodePath] = true

			name := bareWorkflowName(ref)
			if visited[name] {
				continue
			}
			visited[name] = true
			if sub, err := load(ref); err == nil {
				walk(sub.GetNodes(), nodePath)
			}
		}
	}
	walk(wf.GetNodes(), "")
	return open
}

func (r *Runner) registerActivities(
	env *testsuite.TestWorkflowEnvironment,
	rec *recorder,
	events *eventTable,
	guard *runGuard,
	sc *scenario.Scenario,
) error {
	wfJSON, err := protojson.Marshal(r.runWorkflow(sc))
	if err != nil {
		return fmt.Errorf("marshal workflow: %w", err)
	}

	// Which refs stay opaque is what decides which scenario events are ref-node
	// mocks, and therefore which keys a stand-in has to expose.
	//
	// Two gates, deliberately, because two consumers ask different questions.
	// openPaths is per-NODE and drives the graph rewrite, which is the one that
	// decides black-boxing. `transparent` is per-NAME and is all the loader
	// activity can act on, since `workflow_name` is the only thing it receives.
	openPaths := transparentRefPaths(r.workflow, sc.Events, r.loadRef)
	transparent := transparentRefs(r.workflow, sc.Events, r.loadRef)
	outputKeys := mergeKeys(
		blackBoxOutputKeys(r.workflow, sc.Events, openPaths, r.loadRef),
		routerChildOutputKeys(r.workflow, sc.Events))

	blackBoxJSON, err := protojson.Marshal(blackBoxWorkflow(r.workflow, outputKeys))
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
	// is the rule stated in docs/workflows/testing.mdx: an event on the ref
	// NODE mocks that node and
	// leaves its body opaque, and only an event naming a node strictly INSIDE
	// the ref (a qualified id like `parent.inner`) makes the body transparent.
	//
	// A ref the scenario DID target internally is loaded for real, so the body
	// under test actually runs; see transparentRefs for why that gate is keyed
	// by name and why opening every ref by default does not terminate.
	env.RegisterActivityWithOptions(
		func(_ context.Context, in map[string]string) (runtime.LoadedWorkflow, error) {
			if err := guard.check(); err != nil {
				return runtime.LoadedWorkflow{}, err
			}
			// A ref is written "builtin://agent" while the loaded workflow is
			// named "agent", so compare on the bare name — otherwise a workflow
			// requesting ITSELF is misread as a sub-workflow, and vice versa.
			ref := in["workflow_name"]
			name := bareWorkflowName(ref)
			if ref == "" || name == r.workflow.GetName() {
				return runtime.LoadedWorkflow{WorkflowJSON: wfJSON}, nil
			}
			if transparent[name] {
				sub, err := r.loadRef(ref)
				if err != nil {
					return runtime.LoadedWorkflow{}, err
				}
				subJSON, err := protojson.Marshal(sub)
				if err != nil {
					return runtime.LoadedWorkflow{}, fmt.Errorf("marshal sub-workflow %q: %w", name, err)
				}
				return runtime.LoadedWorkflow{WorkflowJSON: subJSON}, nil
			}
			// Opaque: a stand-in built from the ref's OWN definition, so it
			// keeps that workflow's inputs and preset tag (a preset router
			// validates its choice against them). Unresolvable refs fall back
			// to a stand-in shaped like the root.
			if sub, err := r.loadRef(ref); err == nil {
				subJSON, err := protojson.Marshal(blackBoxWorkflow(sub, outputKeys))
				if err != nil {
					return runtime.LoadedWorkflow{}, fmt.Errorf("marshal black-box workflow %q: %w", name, err)
				}
				return runtime.LoadedWorkflow{WorkflowJSON: subJSON}, nil
			}
			return runtime.LoadedWorkflow{WorkflowJSON: blackBoxJSON}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)

	// A skipped node dispatches no activity at all — SkippedStep is the real
	// runtime's only observable signal that a node was scheduled and its
	// condition was false. Reading it here is what makes `skipped:` and
	// `not_reached:` assertable.
	env.RegisterActivityWithOptions(
		func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
			if err := guard.check(); err != nil {
				return nil, err
			}
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
	// mean the same thing here as in the scenario.
	env.RegisterActivityWithOptions(
		func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
			if err := guard.check(); err != nil {
				return nil, err
			}
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
			if err := guard.check(); err != nil {
				return nil, err
			}
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
			if err := guard.check(); err != nil {
				return nil, err
			}
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
				if err := guard.check(); err != nil {
					return nil, err
				}
				return map[string]interface{}{}, nil
			},
			activity.RegisterOptions{Name: name},
		)
	}

	// FailStep is how the real runtime surfaces a step-level defect (a node
	// config that fails CEL evaluation, an unknown node type). It MUST fail,
	// exactly as the real handler does — stubbing it to success would turn the
	// bugs this runner exists to catch into green runs. The failing node is
	// recorded so the result can say WHERE the run died (error_node).
	env.RegisterActivityWithOptions(
		func(_ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
			if err := guard.check(); err != nil {
				return nil, err
			}
			msg, _ := in["error"].(string)
			nodePath, _ := in["node_path"].(string)
			rec.recordStepError(nodePath, msg)
			return nil, fmt.Errorf("workflow validation failed: %s", msg)
		},
		activity.RegisterOptions{Name: "FailStep"},
	)

	// Every node-backed activity resolves its mock from the scenario, keyed by
	// the qualified node id the REAL runtime context reports.
	//
	// The mock is normalized through schema.GetOutputDefaults — the SAME
	// function StepExecutor.normalizeOutput applies to a real activity result.
	// Without it a scenario that omits a field the workflow's CEL reads (e.g.
	// call_llm's compaction_threshold) fails on a missing key that a real
	// activity result would always carry.
	// Parallel loops that still run their body. A loop the scenario
	// black-boxed runs once as a unit, so its one event is not per-iteration.
	parallelLoops := parallelLoopPaths(r.workflow)
	for path := range scenario.BlackBoxedNodes(sc.Events) {
		delete(parallelLoops, path)
	}
	var saveHandler func(context.Context, types.ActivityInput) (map[string]interface{}, error)
	for _, name := range nodeActivities() {
		if name == r.omitActivity {
			continue
		}
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
					if err := guard.check(); err != nil {
						return nil, err
					}
					stepID, _ := in["step_id"].(string)
					loopNodeID, _ := in["loop_node_id"].(string)
					id := stepID
					if loopNodeID != "" && loopNodeID != stepID {
						id = loopNodeID + "." + stepID
					}
					// node_path is the QUALIFIED position and the same
					// authoritative answer qualifiedNodeID prefers for every
					// other activity — the runtime sends it for run steps too
					// (step_executor.go startRun). The loop_node_id form above
					// can only express ONE level, so a run node inside a
					// sub-workflow body resolved to "attempt.lint" while the
					// scenario names "impl_loop.attempt.lint": the mock went
					// unconsumed and the node was recorded under an id nothing
					// could match. SkippedStep already prefers node_path; this
					// makes an executed run node agree with a skipped one.
					if nodePath, _ := in["node_path"].(string); nodePath != "" {
						id = nodePath
					}
					out := normalizeOutput(events.next(id), activityName)
					var req *types.SaveMessageRequest
					if raw, ok := in[types.RunStepSaveMessageKey]; ok && raw != nil {
						req = &types.SaveMessageRequest{}
						if b, err := json.Marshal(raw); err != nil || json.Unmarshal(b, req) != nil {
							req = nil
						}
					}
					if err := resolveDelegatedSave(rec, id, activityName, req, out, stepID); err != nil {
						return nil, err
					}
					rec.recordCompleted(id, out)
					return out, nil
				},
				activity.RegisterOptions{Name: activityName},
			)
			continue
		}
		nodeHandler := func(_ context.Context, in types.ActivityInput) (map[string]interface{}, error) {
			if err := guard.check(); err != nil {
				return nil, err
			}
			id := qualifiedNodeID(in.Runtime)
			if routerID, ok := nodeRoutingDecisionRouter(id); ok {
				return nodeRoutingDecisionOutput(events, rec, routerID, activityName)
			}
			if routerID, ok := workflowRoutingDecisionRouter(id); ok {
				return workflowRoutingDecisionOutput(events, rec, routerID, activityName)
			}
			// An inline save_message runs as a SaveMessage activity under a
			// synthetic "<node>-save" step id (save_message.go:611). It is a
			// side effect of the owning node, not a graph node a scenario
			// can name, so it must not appear in reached/completed.
			if strings.HasSuffix(in.Runtime.StepID, "-save") {
				// A workflow-side save (a structural node's save_message):
				// the runtime already resolved it, so the dispatched node
				// carries the final role and content.
				recordResolvedSave(rec, strings.TrimSuffix(id, "-save"), in.Node)
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
			// A black-boxed ref runs one stand-in node, so the mock has to
			// be looked up under the REF NODE above it: the scenario wrote
			// `attempt.review`, the runtime dispatches
			// `attempt.review.__scenario_black_box`. Resolving the id here
			// is what lets an event on a ref node be consumed at all —
			// keyed by the dispatched path it never matched, and the run
			// reported it "never consumed" while the node sat in `reached`.
			//
			// The output is deliberately NOT normalized: it is a
			// sub-workflow's declared output map, not a SaveMessage result,
			// and filling it with SaveMessage's fields would put keys in
			// `nodes.<ref>` that the real sub-workflow never returns.
			if refPath := blackBoxRefPath(id); refPath != "" {
				// The child a preset router selected: its outputs came
				// with the router's own event, which is already consumed.
				if out, ok := rec.routerChildOutputs(refPath); ok {
					return out, nil
				}
				var out map[string]interface{}
				if parallelLoops[refPath] {
					// Iterations of a parallel ref loop: event i answers
					// iteration i (see nextAt).
					out = events.nextAt(refPath, in.Runtime.LoopIteration)
				} else {
					out = events.next(refPath)
				}
				rec.recordCompleted(refPath, out)
				return out, nil
			}
			out := normalizeOutput(events.next(id), activityName)
			// in.Node is the node with its args already CEL-evaluated by
			// the real runtime, which is what carries the explicit
			// compaction_threshold when the workflow sets one.
			runtime.ApplyMockedCompactionThreshold(out, in.Node)
			if activityName == "SaveMessage" {
				// A save_message NODE: its args are the resolved message.
				recordResolvedSave(rec, id, in.Node)
			}
			// A delegated save_message runs after the activity in
			// production (ActivityWrapper); run the same resolution on the
			// mocked result so its condition and templates are exercised.
			if err := resolveDelegatedSave(rec, id, activityName, in.Runtime.SaveMessage, out, in.Runtime.StepID); err != nil {
				return nil, err
			}
			rec.recordCompleted(id, out)
			return out, nil
		}
		env.RegisterActivityWithOptions(nodeHandler, activity.RegisterOptions{Name: activityName})
		if activityName == "SaveMessage" {
			saveHandler = nodeHandler
		}
	}
	// Anything else the workflow dispatches is an activity this runner has no
	// mock for. It must never reach a real implementation — the runner runs
	// inside API processes with no daemon, DB writes or network to spare — so
	// it fails non-retryably (one attempt, not a retry ladder) and is reported
	// as a scenario mismatch naming the activity.
	env.RegisterDynamicActivity(
		func(ctx context.Context, _ converter.EncodedValues) (interface{}, error) {
			name := activity.GetInfo(ctx).ActivityType.Name
			guard.recordUnmocked(name)
			return nil, temporal.NewNonRetryableApplicationError(
				fmt.Sprintf("activity %q is not mocked by the scenario runner", name), "ScenarioUnmockedActivity", nil)
		},
		activity.DynamicRegisterOptions{},
	)

	// A spawn is ASYNCHRONOUS in production: the child runs in the background
	// and finishes after the parent's current turn. In this environment a
	// black-boxed spawn child is one instant activity racing the parent's own
	// activities on real goroutines, so whether it finished before or after
	// the parent's loop-exit check was a scheduling coin flip — and that
	// decides whether the parent parks and takes another turn. Delaying the
	// stand-in on the WORKFLOW clock makes the order deterministic and
	// faithful: the mock clock only advances once nothing else is running, so
	// the child completes after the parent has finished the turn that spawned
	// it. (Registration must precede every OnActivity call; the catch-all
	// expectation keeps every other SaveMessage on the normal path.)
	if saveHandler != nil {
		env.OnActivity("SaveMessage", mock.Anything, mock.MatchedBy(isSpawnStandIn)).
			After(spawnCompletionDelay).Return(saveHandler)
		env.OnActivity("SaveMessage", mock.Anything, mock.Anything).Return(saveHandler)
	}
	return nil
}

// spawnCompletionDelay is how far, on the workflow's mock clock, a
// black-boxed spawn child's completion is pushed. Any positive value works:
// it costs no wall-clock time, it only orders the child after everything
// already runnable.
const spawnCompletionDelay = time.Second

// isSpawnStandIn matches the stand-in node of a black-boxed spawn child
// ("spawn-call_x.__scenario_black_box", possibly nested under a parent path).
func isSpawnStandIn(in types.ActivityInput) bool {
	refPath := blackBoxRefPath(qualifiedNodeID(in.Runtime))
	if refPath == "" {
		return false
	}
	last := refPath
	if i := strings.LastIndex(refPath, "."); i >= 0 {
		last = refPath[i+1:]
	}
	return strings.HasPrefix(last, "spawn-")
}

// normalizeOutput fills a scenario's partial mock out to the activity's full
// output field set, mirroring StepExecutor.normalizeOutput.
func normalizeOutput(raw map[string]interface{}, activityName string) map[string]interface{} {
	if raw == nil {
		raw = map[string]interface{}{}
	}
	raw = encodeToolCallInputs(raw)
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

// resolveDelegatedSave runs a node's delegated save_message against its mocked
// result, through the runtime's own resolution path
// (runtime.ResolveDelegatedSaveMessage — the function ActivityWrapper calls
// after a real activity). A resolution error fails the activity, exactly as it
// fails the step in production: that is the point of running it, since a
// save_message template that cannot resolve is a run-ending defect the
// activity mock would otherwise hide.
func resolveDelegatedSave(rec *recorder, node, activityName string, req *types.SaveMessageRequest, out map[string]interface{}, stepID string) error {
	if req == nil || req.Config == nil {
		return nil
	}
	// Resolve against a copy: normalization inside the resolver must not
	// change what the workflow receives from the mock.
	copied := make(map[string]interface{}, len(out))
	for k, v := range out {
		copied[k] = v
	}
	saved, err := runtime.ResolveDelegatedSaveMessage(req, activityName, copied, runtime.DelegatedSaveIdentity{
		ChatID:     "scenario-chat",
		Thread:     "scenario-thread",
		WorkflowID: "scenario-wf",
		StepID:     stepID,
	})
	if err != nil {
		return temporal.NewNonRetryableApplicationError(
			fmt.Sprintf("save_message for %s: %v", node, err), "SaveMessageResolution", err)
	}
	if saved != nil {
		rec.recordSaved(node, saved.Role, saved.Content)
	}
	return nil
}

// recordResolvedSave records a message the runtime resolved before dispatch
// (a save_message node, or a structural node's workflow-side save).
func recordResolvedSave(rec *recorder, node string, dispatched *reliantv1.Node) {
	args := dispatched.GetSaveMessageNode()
	if args == nil || node == "" {
		return
	}
	rec.recordSaved(node, args.GetResolvedRole(), args.GetResolvedContent())
}

// resumeInput maps a scenario's start_at onto the runtime's real
// position-resume mode (WorkflowInput.Resume → resolveResumeTarget): the run
// enters directly at that node, with NO earlier node outputs reconstructed —
// thread history is the only carried state. That is exactly what start_at
// models, so a scenario's `state:` is deliberately not injected here: a
// resumed run in production does not have it either, and a downstream read
// of an earlier node that is not has()-guarded must fail here as it would
// there.
func resumeInput(sc *scenario.Scenario) *runtime.ResumeInput {
	if sc.StartAt == "" {
		return nil
	}
	return &runtime.ResumeInput{NodeID: sc.StartAt}
}

// encodeToolCallInputs puts a mock's tool calls into their wire shape.
//
// A tool call's `input` is a JSON STRING on the wire (proto ToolCallMsg.input
// is `string`), and everything downstream — save_message's tool_calls field,
// execute_tools — reads it that way. Scenario YAML naturally writes it as a
// map (`input: {command: ls}`); the typed `llm_response` form
// already JSON-encodes it, but a raw `output:` mock passed the map straight
// through, so the run saw a shape production never produces and failed in
// save_message resolution ("tool_calls[0].input: expected string"). Encoding
// here makes the raw and typed forms mean the same thing, and keeps stored
// scenarios that target nodes inside a ref — where validation cannot infer the
// node's output shape — working. The mock map is not mutated.
func encodeToolCallInputs(raw map[string]interface{}) map[string]interface{} {
	calls, ok := raw["tool_calls"].([]interface{})
	if !ok || len(calls) == 0 {
		return raw
	}
	var encoded []interface{}
	for i, c := range calls {
		call, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		input, present := call["input"]
		if !present || input == nil {
			continue
		}
		if _, isString := input.(string); isString {
			continue
		}
		b, err := json.Marshal(input)
		if err != nil {
			continue
		}
		if encoded == nil {
			encoded = append([]interface{}{}, calls...)
		}
		copied := make(map[string]interface{}, len(call))
		for k, v := range call {
			copied[k] = v
		}
		copied["input"] = string(b)
		encoded[i] = copied
	}
	if encoded == nil {
		return raw
	}
	out := make(map[string]interface{}, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	out["tool_calls"] = encoded
	return out
}

// RunScenario runs one scenario for wf with the given ref loader. Its
// signature matches tools.ScenarioRunner, which is how the run_scenario and
// write_scenario agent tools reach the runner without importing it (the
// runner imports the runtime's activities, which import the tools package).
func RunScenario(ctx context.Context, wf *reliantv1.Workflow, sc *scenario.Scenario, loader func(ref string) (*reliantv1.Workflow, error)) *scenario.ScenarioResult {
	return New(wf, Options{Loader: loader}).RunContext(ctx, sc)
}
