// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/askuser"
	"github.com/reliant-labs/reliant/internal/execfollow"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// superviseClients bundles the Connect clients the supervision verbs use,
// all authenticated by the resolved context credential (PAT or JWT). Every
// verb here talks to the server exclusively over these RPCs — no DB access.
type superviseClients struct {
	chat     reliantv1connect.ChatServiceClient
	question reliantv1connect.QuestionServiceClient
	approval reliantv1connect.ApprovalServiceClient
	conn     *connection
}

// newSuperviseClients resolves the context connection and constructs the
// Connect clients, mirroring how `workflow run` / `follow` build theirs.
func newSuperviseClients(cmd *cobra.Command) (*superviseClients, error) {
	conn, err := resolveConnection(cmd)
	if err != nil {
		return nil, err
	}
	httpClient := conn.httpClient()
	return &superviseClients{
		chat:     reliantv1connect.NewChatServiceClient(httpClient, conn.ServerURL),
		question: reliantv1connect.NewQuestionServiceClient(httpClient, conn.ServerURL),
		approval: reliantv1connect.NewApprovalServiceClient(httpClient, conn.ServerURL),
		conn:     conn,
	}, nil
}

// rpcError renders a Connect error as a customer-readable message. The most
// common failures (not found / permission / unauthenticated / try again) get
// purpose-written text naming the server and credential actually in play;
// everything else falls back to the raw message.
func (c *superviseClients) rpcError(err error, what string) error {
	if err == nil {
		return nil
	}
	switch connect.CodeOf(err) {
	case connect.CodeNotFound:
		return fmt.Errorf("%s: not found on %s — check the execution id, or %s may not have access",
			what, c.conn.describeServer(), c.conn.describeCredential())
	case connect.CodePermissionDenied:
		return fmt.Errorf("%s: permission denied — %s cannot access this execution on %s",
			what, c.conn.describeCredential(), c.conn.describeServer())
	case connect.CodeUnauthenticated:
		return fmt.Errorf("%s: %s was rejected by %s — run 'reliant auth token create' or 'reliant auth login'",
			what, c.conn.describeCredential(), c.conn.describeServer())
	case connect.CodeUnavailable:
		// The transport already names the server and where that URL came from;
		// keep the cause rather than replacing it with a vaguer sentence.
		return fmt.Errorf("%s: %w", what, err)
	case connect.CodeFailedPrecondition:
		return fmt.Errorf("%s: %s", what, connect.CodeOf(err))
	default:
		return fmt.Errorf("%s: %w", what, err)
	}
}

// ============================================================================
// workflow watch
// ============================================================================

func newWorkflowWatchCmd() *cobra.Command {
	var flags followFlags

	cmd := &cobra.Command{
		Use:   "watch <execution-id>",
		Short: "Watch a workflow execution and print meaningful boundaries as they happen",
		Long: `Streams a workflow execution and prints only the boundaries a supervisor
cares about, in human-readable form:

  ▶ node started        ✓ node completed        ✗ node failed (exit N)
  ▶ workflow started    ✓ workflow completed    ✗ workflow failed
  ❓ question raised (id + prompt + option labels)
  ▶ question answered (id + time spent in the gate)
  ⏸ approval required (id + title)
  ▶ approval approved/denied (id + time spent in the gate)

A run step that exits non-zero prints ✗ with its exit code, not ✓: the activity
completed, the COMMAND failed, and those are not the same event.

A run that ends at a terminal node declaring outcome: failure prints
"✗ workflow ended WITHOUT PASSING" rather than ✓, and exits 1. Reaching the end
of the graph is not the same as the work having passed.

watch BLOCKS on the live update feed until the next boundary or a terminal
state — it does not print a snapshot and exit (use 'workflow status' for a
one-shot snapshot). It consumes the same durable update feed as
'workflow follow'; the two are deliberate siblings:

  watch   human/agent supervision — readable boundary lines (this command)
  follow  machine pipelines        — one NDJSON event per line on stdout

Gates are reconciled every poll, so a question/approval that is already open
when you attach (or that you --tail past) is still printed — you cannot sit at
a gate with no signal. Pass --exit-on-gate to stop at the next gate (exit 3).

Both drive the identical event engine and honor the same --hook, --timeout,
--interval and --tail flags. 'question' and 'approval' are additional hookable
events here and on follow, so an agent can auto-answer:

  reliant workflow watch <id> --hook 'on=question cmd=./answer.sh'

The hook receives the event JSON (including the question payload) on stdin
and RELIANT_EVENT_* env vars, exactly as follow's hooks do.

Exit codes:
  0  the root workflow completed AND passed
  1  the root workflow failed, was cancelled, expired, or ended at a terminal
     node declaring outcome: failure
  2  --timeout elapsed before a terminal state
  3  --exit-on-gate: a question/approval gate opened`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowWatch(cmd, args[0], &flags)
		},
	}

	flags.register(cmd)
	return cmd
}

// runWorkflowWatch drives a watch session (human-readable renderer) and exits
// with the engine's exit code when non-zero, mirroring runWorkflowFollow.
func runWorkflowWatch(cmd *cobra.Command, executionID string, flags *followFlags) error {
	conn, err := resolveConnection(cmd)
	if err != nil {
		return err
	}

	hooks, err := resolveHooks(flags.hooks, conn.Hooks)
	if err != nil {
		return err
	}

	httpClient := conn.httpClient()

	fmt.Fprintf(cmd.ErrOrStderr(), "Watching execution %s (Ctrl-C to stop)\n", executionID)

	engine := &execfollow.Engine{
		Source:      newChatUpdateSource(httpClient, conn.ServerURL, executionID),
		ExecutionID: executionID,
		Out:         cmd.OutOrStdout(),
		Log:         cmd.ErrOrStderr(),
		Renderer:    execfollow.RenderText,
		Hooks:       hooks,
		Interval:    flags.interval,
		Timeout:     flags.timeout,
		Tail:        flags.tail,
		ExitOnGate:  flags.exitOnGate,
	}

	code, err := engine.Run(cmd.Context())
	if err != nil {
		return err
	}
	if code != execfollow.ExitSuccess {
		os.Exit(code)
	}
	return nil
}

// ============================================================================
// workflow wait-for-gate
// ============================================================================

// waitForGateResult is the machine-readable (--json) result of a wait-for-gate
// call: the outcome that ended the wait, plus the open gate(s) when it stopped
// at one.
type waitForGateResult struct {
	ExecutionID string `json:"execution_id"`
	// Outcome: gate | completed | did_not_pass | failed | cancelled | expired |
	// timeout. did_not_pass is the run that finished its graph at a terminal
	// node declaring failure — a clean lifecycle with a failing verdict.
	Outcome string             `json:"outcome"`
	Gates   []execfollow.Event `json:"gates,omitempty"`
}

func newWorkflowWaitForGateCmd() *cobra.Command {
	var (
		jsonOut  bool
		timeout  time.Duration
		interval time.Duration
		tail     bool
	)
	cmd := &cobra.Command{
		Use:   "wait-for-gate <execution-id>",
		Short: "Block until the workflow needs you — the next open question/approval",
		Long: `Blocks until the workflow reaches the next OPEN gate — a question or approval
awaiting input — then prints that gate (id, node, prompts + option labels) and
exits 3. This is the "run until it needs me" supervision primitive: unlike
'watch'/'follow' it does not stream every boundary, it stays quiet until there
is something for you to act on.

If a gate is ALREADY open when you call it, it returns immediately (the same
per-poll reconciler that 'watch'/'follow' use surfaces an already-open gate).
A historical question/approval that has since been ANSWERED does not count —
only a currently-pending gate ends the wait.

Typical loop:

  while reliant workflow wait-for-gate <id> --json > gate.json; do
    reliant workflow answer <id> --select "$(pick_answer gate.json)"
  done

Exit codes:
  3  a question/approval gate is open (its details are printed)
  0  the workflow completed AND passed before any gate opened
  1  the workflow failed, was cancelled, expired, or ended without passing
     (outcome: failure) before any gate opened — --json reports which
  2  --timeout elapsed before a gate opened

Drives the same event engine as 'watch'/'follow' and honors --timeout,
--interval and --tail.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowWaitForGate(cmd, args[0], jsonOut, timeout, interval, tail)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the gate (or outcome) as JSON")
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "Give up after this long (exit code 2); 0 waits indefinitely")
	cmd.Flags().DurationVar(&interval, "interval", execfollow.DefaultInterval, "Poll interval")
	cmd.Flags().BoolVar(&tail, "tail", false, "Skip historical events (an already-open gate is still reported)")
	return cmd
}

// runWorkflowWaitForGate drives the follow engine with ExitOnGate set, keeping
// the stream quiet (Out discarded) until a gate opens, then prints the gate and
// exits with the engine's code.
func runWorkflowWaitForGate(cmd *cobra.Command, executionID string, jsonOut bool, timeout, interval time.Duration, tail bool) error {
	conn, err := resolveConnection(cmd)
	if err != nil {
		return err
	}
	httpClient := conn.httpClient()

	fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for the next gate on execution %s (Ctrl-C to stop)\n", executionID)

	engine := &execfollow.Engine{
		Source:      newChatUpdateSource(httpClient, conn.ServerURL, executionID),
		ExecutionID: executionID,
		Out:         io.Discard, // stay quiet until a gate opens; the gate is printed below
		Log:         cmd.ErrOrStderr(),
		Interval:    interval,
		Timeout:     timeout,
		Tail:        tail,
		ExitOnGate:  true,
	}

	code, err := engine.Run(cmd.Context())
	if err != nil {
		return err
	}
	if werr := writeWaitForGateResult(cmd.OutOrStdout(), executionID, code, engine.OpenGates(), engine.TerminalStatus(), engine.TerminalOutcome(), jsonOut); werr != nil {
		return werr
	}
	if code != execfollow.ExitSuccess {
		os.Exit(code)
	}
	return nil
}

// writeWaitForGateResult renders the outcome of a wait-for-gate call. Kept pure
// (no RPCs, no os.Exit) so the output surface is unit-testable independently of
// the engine's process-exit behavior.
func writeWaitForGateResult(w io.Writer, executionID string, code int, gates []execfollow.Event, terminal, outcome string, jsonOut bool) error {
	result := waitForGateResult{
		ExecutionID: executionID,
		Outcome:     waitForGateOutcome(code, terminal, outcome),
	}
	if code == execfollow.ExitGate {
		result.Gates = gates
	}

	if jsonOut {
		return printJSONIndent(w, result)
	}

	switch code {
	case execfollow.ExitGate:
		if len(gates) == 0 {
			// Defensive: ExitGate always carries at least one open gate, but
			// never claim a gate we can't show.
			fmt.Fprintf(w, "A gate is open on %s, awaiting input.\n", executionID)
			return nil
		}
		fmt.Fprintln(w, "Workflow is waiting for input:")
		for _, g := range gates {
			fmt.Fprintln(w, execfollow.RenderText(g))
		}
	case execfollow.ExitTimeout:
		fmt.Fprintf(w, "Timed out waiting for a gate on %s.\n", executionID)
	case execfollow.ExitSuccess:
		fmt.Fprintf(w, "Workflow %s completed — no gate to wait for.\n", executionID)
	default: // ExitFailed
		fmt.Fprintf(w, "Workflow %s %s before reaching a gate.\n", executionID, terminalWord(terminal, outcome))
	}
	return nil
}

// waitForGateOutcome maps the engine exit code (and the root's terminal
// status/verdict, when it ended) onto the machine-readable outcome string.
//
// "did_not_pass" is its own value rather than being folded into "failed": a run
// that reached its declared failure terminal ran cleanly to the end of its
// graph, which is a different thing to go look at than a run whose machinery
// broke.
func waitForGateOutcome(code int, terminal, outcome string) string {
	switch code {
	case execfollow.ExitGate:
		return "gate"
	case execfollow.ExitSuccess:
		return "completed"
	case execfollow.ExitTimeout:
		return "timeout"
	default: // ExitFailed
		if terminal == "completed" && outcome == execfollow.OutcomeFailure {
			return "did_not_pass"
		}
		if terminal != "" {
			return terminal // failed | cancelled | expired
		}
		return "failed"
	}
}

func terminalWord(terminal, outcome string) string {
	if terminal == "completed" && outcome == execfollow.OutcomeFailure {
		return "ended without passing"
	}
	if terminal == "" {
		return "ended"
	}
	return terminal
}

// ============================================================================
// workflow status
// ============================================================================

type statusNode struct {
	NodeID     string       `json:"node_id"`
	Record     string       `json:"record,omitempty"`
	Thread     string       `json:"thread,omitempty"`
	Status     string       `json:"status"`
	Gate       string       `json:"gate,omitempty"` // step that raised this thread's open question
	Started    string       `json:"started,omitempty"`
	Completed  string       `json:"completed,omitempty"`
	DurationMs int64        `json:"duration_ms,omitempty"`
	Iteration  *int32       `json:"iteration,omitempty"`
	Children   []statusNode `json:"children,omitempty"`
}

type statusStep struct {
	StepID   string `json:"step_id"`
	Activity string `json:"activity"`
	Runs     int    `json:"runs"`
	// Failed is how many of those runs did not pass. Runs and last_result alone
	// report a lane that failed four loop iterations and passed the fifth as
	// plainly "ok": the aggregate keeps only the most recent execution, and the
	// four red ones vanish. A retry loop's whole story is in this number.
	Failed   int    `json:"failed,omitempty"`
	Result   string `json:"last_result"`
	ExitCode *int32 `json:"exit_code,omitempty"`
	LastMs   int64  `json:"last_duration_ms,omitempty"`
}

// statusStepGroup is the steps recorded by ONE node thread.
//
// step_executions has no phase column, but it does not need one: rows are
// scoped by workflow_id, and the execution tree says which node spawned each
// child workflow. Flattening every row into a single table keyed on step_id
// threw that away, and two phases both running builtin://get-it-right — each
// with steps named lint/test/build/review — merged into one set of rows
// belonging to neither. A reader then attributes whatever the last row said to
// the whole run. Measured: a `review` row written by the phase that has
// review_enabled: false was read as the OTHER phase's reviewer being skipped.
type statusStepGroup struct {
	// Node is the path of node ids from the root graph down to the thread that
	// recorded these steps — "impl_2" for a phase, "impl_2/agent_loop" for a
	// workflow node inside it. Empty for the root graph's own steps.
	Node string `json:"node"`
	// Record is the workflow this thread ran.
	Record string       `json:"record,omitempty"`
	Steps  []statusStep `json:"steps"`
}

type statusReport struct {
	ExecutionID string `json:"execution_id"`
	Workflow    string `json:"workflow"`
	// Status is the LIFECYCLE: did the Temporal execution finish, and how.
	Status string `json:"status"`
	// Outcome is the run's own VERDICT — "success" / "failure" — declared by the
	// terminal node the graph reached. Empty when the workflow declared none.
	// A run that routed to its `failed` node is status=completed,
	// outcome=failure, and reporting only the first is a false green.
	Outcome       string            `json:"outcome,omitempty"`
	Started       string            `json:"started,omitempty"`
	Completed     string            `json:"completed,omitempty"`
	OpenQuestions int               `json:"open_questions"`
	OpenApprovals int               `json:"open_approvals"`
	Nodes         []statusNode      `json:"nodes"`
	Steps         []statusStepGroup `json:"steps,omitempty"`
}

// statusExitCode is the process exit code `workflow status` ends with.
//
//	0  the run succeeded (or is still going — nothing has gone wrong yet)
//	1  the command itself failed (returned as an error, never from here)
//	2  the command ran fine and found a problem: the run ended, and it did not
//	   succeed — either its lifecycle is failed/cancelled/expired, or it ran to a
//	   terminal node that declares outcome: failure
//
// This matches the convention already used elsewhere (`forge env status`), and
// deliberately does NOT reuse 1 — "I could not inspect the run" and "the run
// did not pass" are different problems for a supervisor and must not collapse.
// A still-running run exits 0, so the common `status` poll is unaffected.
func statusExitCode(r statusReport) int {
	switch r.Status {
	case "failed", "cancelled", "expired":
		return 2
	case "completed":
		if r.Outcome == execfollow.OutcomeFailure {
			return 2
		}
	}
	return 0
}

func newWorkflowStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status <execution-id>",
		Short: "Show a one-shot snapshot of a workflow execution",
		Long: `Prints a point-in-time snapshot of a workflow execution over Connect RPCs
(no streaming, no DB): overall status AND outcome, the per-node execution tree
with status/timing, recorded step executions, and the count of open questions
and approvals awaiting input.

Status and Outcome are two different facts and both are printed:

  Status   the LIFECYCLE — did the Temporal execution finish, and how
  Outcome  the run's own VERDICT — did the work pass

They are not the same. A run that fails verification routes to its workflow's
failure terminal node and finishes cleanly: Status COMPLETED, Outcome FAILURE.
Reading Status alone reports that run as a success. Outcome is blank when the
workflow declares no pass/fail terminal — blank means "it never said", not
failure.

A node thread parked on a gate keeps the stored status RUNNING, so the tree
marks the thread holding the open question as GATED and names the step that
raised it. Only that thread is marked — its siblings keep reading RUNNING,
because in a fanned-out run they really are still executing.

Exit codes:
  0  the run succeeded, or is still running
  1  the command failed (could not reach the server, no such execution, ...)
  2  the command ran fine and the run did NOT succeed — Outcome FAILURE, or a
     failed/cancelled/expired lifecycle

Use 'workflow ps' for every live run at once, with time-in-state and suspected
stalls; use 'workflow watch' to block on live boundaries instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowStatus(cmd, args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runWorkflowStatus(cmd *cobra.Command, executionID string, jsonOut bool) error {
	clients, err := newSuperviseClients(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()

	execResp, err := clients.chat.GetWorkflowExecutions(ctx, connect.NewRequest(&reliantv1.GetWorkflowExecutionsRequest{
		ChatId: executionID,
	}))
	if err != nil {
		return clients.rpcError(err, "fetching workflow executions")
	}
	root := execResp.Msg.GetRootWorkflow()
	if root == nil {
		if all := execResp.Msg.GetAllRootWorkflows(); len(all) > 0 {
			root = all[0]
		}
	}
	if root == nil {
		return fmt.Errorf("no workflow execution found for %s", executionID)
	}

	report := statusReport{
		ExecutionID: executionID,
		Workflow:    root.GetWorkflowName(),
		Status:      chatWorkflowStatusString(root),
		Outcome:     root.GetOutcome(),
		Started:     root.GetCreatedAt(),
		Completed:   root.GetCompletedAt(),
	}

	// Open questions: GetPendingQuestion returns the single pending question
	// (which may bundle several sub-questions); count its sub-questions. The
	// question's thread id is what lets the node tree below mark the ONE thread
	// that is actually gated instead of leaving every sibling reading RUNNING.
	gateByThread := map[string]string{}
	if q, err := clients.question.GetPendingQuestion(ctx, connect.NewRequest(&reliantv1.GetPendingQuestionRequest{
		ChatId: executionID,
	})); err == nil {
		if qi := q.Msg.GetQuestion(); qi != nil {
			report.OpenQuestions = 1
			if md, ok := askuser.ParseMetadata(qi.GetMetadata()); ok && len(md.Questions) > 1 {
				report.OpenQuestions = len(md.Questions)
			}
			if qi.GetThreadId() != "" {
				gateByThread[qi.GetThreadId()] = orDash(qi.GetStepId())
			}
		}
	}

	for _, child := range root.GetChildren() {
		report.Nodes = append(report.Nodes, buildStatusNode(child, gateByThread))
	}
	report.Steps = summarizeSteps(root)

	// Open approvals: pending approvals for the chat.
	if a, err := clients.approval.ListApprovalsByChat(ctx, connect.NewRequest(&reliantv1.ListApprovalsByChatRequest{
		ChatId: executionID,
	})); err == nil {
		for _, ap := range a.Msg.GetApprovals() {
			if ap.GetStatus() == reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING {
				report.OpenApprovals++
			}
		}
	}

	if jsonOut {
		if err := printJSONIndent(cmd.OutOrStdout(), report); err != nil {
			return err
		}
	} else {
		printStatusReport(cmd.OutOrStdout(), report)
	}
	if code := statusExitCode(report); code != 0 {
		os.Exit(code)
	}
	return nil
}

// buildStatusNode converts a WorkflowExecution subtree into a statusNode.
//
// gateByThread maps a thread id to the step that raised its open question, so a
// gated thread is marked as gated and its siblings are not. The stored status
// alone cannot express this: a thread parked on a signal stays RUNNING, so
// without the thread-scoped gate every node in a fanned-out run reads RUNNING
// whether it is working or waiting on a human.
func buildStatusNode(wf *reliantv1.WorkflowExecution, gateByThread map[string]string) statusNode {
	node := statusNode{
		NodeID:    wf.GetSpawnedByNodeId(),
		Record:    wf.GetWorkflowName(),
		Thread:    wf.GetThread(),
		Status:    chatWorkflowStatusString(wf),
		Gate:      gateByThread[wf.GetThread()],
		Started:   wf.GetCreatedAt(),
		Completed: wf.GetCompletedAt(),
	}
	if node.NodeID == "" {
		node.NodeID = wf.GetWorkflowName()
	}
	if wf.Iteration != nil {
		it := wf.GetIteration()
		node.Iteration = &it
	}
	node.DurationMs = durationMs(wf.GetCreatedAt(), wf.GetCompletedAt())
	for _, child := range wf.GetChildren() {
		node.Children = append(node.Children, buildStatusNode(child, gateByThread))
	}
	return node
}

// summarizeSteps groups step executions by the node thread that recorded them,
// and within a thread by step id, reporting run count and the most recent
// result.
//
// Two things the flat version could not say, and this one must:
//
//   - WHICH phase a row belongs to. Steps are already scoped by workflow_id
//     and the tree already says which node spawned each child workflow, so the
//     attribution is in the response — it was being discarded, not missing.
//   - That a `workflow`-type node ran at all. Such a node executes as a child
//     workflow and writes NO step_executions row of its own, so the phase whose
//     reviewer actually ran showed nothing while the phase that skipped its
//     reviewer showed a row. One row is synthesized per child workflow, in the
//     parent's group, from the child's own execution record.
func summarizeSteps(root *reliantv1.WorkflowExecution) []statusStepGroup {
	type agg struct {
		activity string
		runs     int
		failed   int
		result   string
		exit     *int32
		lastMs   int64
		lastAt   string
	}
	type group struct {
		record string
		byStep map[string]*agg
		order  []string
	}

	groups := map[string]*group{}
	var groupOrder []string

	groupFor := func(node, record string) *group {
		g := groups[node]
		if g == nil {
			g = &group{record: record, byStep: map[string]*agg{}}
			groups[node] = g
			groupOrder = append(groupOrder, node)
		}
		return g
	}

	// record folds one execution of one step into its group. createdAt orders
	// repeats so the reported result is the most recent one.
	record := func(g *group, stepID, activity, createdAt string, ms int64, result string, exit *int32) {
		a := g.byStep[stepID]
		if a == nil {
			a = &agg{}
			g.byStep[stepID] = a
			g.order = append(g.order, stepID)
		}
		a.runs++
		// Counted across every execution, not just the surviving one: a loop
		// that ran this step five times and failed four of them must not report
		// as a single "ok".
		if result == "FAIL" {
			a.failed++
		}
		if createdAt >= a.lastAt {
			a.lastAt = createdAt
			a.activity = activity
			a.lastMs = ms
			a.result = result
			a.exit = exit
		}
	}

	var walk func(wf *reliantv1.WorkflowExecution, node string)
	walk = func(wf *reliantv1.WorkflowExecution, node string) {
		g := groupFor(node, wf.GetWorkflowName())

		for _, s := range wf.GetSteps() {
			var exit *int32
			if s.ExitCode != nil {
				ec := s.GetExitCode()
				exit = &ec
			}
			record(g, s.GetStepId(), s.GetActivityName(), s.GetCreatedAt(), s.GetDurationMs(), stepResult(s), exit)
		}

		for _, c := range wf.GetChildren() {
			childNode := c.GetSpawnedByNodeId()
			if childNode == "" {
				childNode = c.GetWorkflowName()
			}
			record(g, childNode, c.GetWorkflowName(), c.GetCreatedAt(),
				durationMs(c.GetCreatedAt(), c.GetCompletedAt()), childWorkflowResult(c), nil)
			walk(c, childNodePath(node, childNode))
		}
	}
	walk(root, "")

	out := make([]statusStepGroup, 0, len(groupOrder))
	for _, node := range groupOrder {
		g := groups[node]
		if len(g.order) == 0 {
			continue
		}
		steps := make([]statusStep, 0, len(g.order))
		for _, id := range g.order {
			a := g.byStep[id]
			steps = append(steps, statusStep{
				StepID:   id,
				Activity: a.activity,
				Runs:     a.runs,
				Failed:   a.failed,
				Result:   a.result,
				ExitCode: a.exit,
				LastMs:   a.lastMs,
			})
		}
		out = append(out, statusStepGroup{Node: node, Record: g.record, Steps: steps})
	}
	return out
}

// childNodePath joins a parent node path with a child node id. The root graph
// has the empty path, so its immediate children are named by node id alone.
func childNodePath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "/" + child
}

// stepResult is what one recorded step execution claims.
//
// SKIP is its own word. A skipped step now records a NULL verdict rather than
// success=true, but "-" would report it the same as a row that simply has no
// verdict — and the distinction that cost the time is precisely "did this
// check run". The activity name is the emitter's stamp for it; output_json,
// which carries the `skipped` field itself, is deliberately not sent over this
// RPC because it can be megabytes per run.
func stepResult(s *reliantv1.StepExecution) string {
	if s.GetActivityName() == model.ActivitySkippedStep {
		return "SKIP"
	}
	if s.Success == nil {
		return "-"
	}
	if s.GetSuccess() {
		return "ok"
	}
	return "FAIL"
}

// childWorkflowResult is what a synthesized row for a workflow-type node
// claims. A child workflow has a lifecycle AND a verdict, exactly like the run
// as a whole, and the two are not the same fact: a child that finished without
// declaring an outcome reports its lifecycle word, never "ok".
func childWorkflowResult(c *reliantv1.WorkflowExecution) string {
	switch c.GetOutcome() {
	case execfollow.OutcomeFailure:
		return "FAIL"
	case execfollow.OutcomeSuccess:
		return "ok"
	}
	status := chatWorkflowStatusString(c)
	switch status {
	case "failed", "cancelled", "expired":
		return "FAIL"
	}
	return status
}

func printStatusReport(w io.Writer, r statusReport) {
	fmt.Fprintf(w, "Execution %s\n", r.ExecutionID)
	fmt.Fprintf(w, "  Workflow:  %s\n", r.Workflow)
	fmt.Fprintf(w, "  Status:    %s\n", strings.ToUpper(r.Status))
	// The verdict is printed on its own line, immediately under the lifecycle,
	// because they answer different questions and a COMPLETED run that did not
	// pass is the exact case a supervisor must not mistake for a finished build.
	if line := outcomeLine(r); line != "" {
		fmt.Fprintf(w, "  Outcome:   %s\n", line)
	}
	if r.Started != "" {
		fmt.Fprintf(w, "  Started:   %s\n", r.Started)
	}
	if r.Completed != "" {
		fmt.Fprintf(w, "  Completed: %s\n", r.Completed)
	}
	fmt.Fprintf(w, "  Open questions: %d   Open approvals: %d\n", r.OpenQuestions, r.OpenApprovals)

	if len(r.Nodes) > 0 {
		fmt.Fprintln(w, "\nNODE THREADS (child workflows)")
		for _, n := range r.Nodes {
			printStatusNode(w, n, 1)
		}
	}

	if len(r.Steps) > 0 {
		// Grouped, never flat: two phases running the same workflow have steps
		// with the same names, and a merged table attributes one phase's rows
		// to the other.
		fmt.Fprintln(w, "\nSTEP EXECUTIONS  (by node thread)")
		for _, g := range r.Steps {
			node := g.Node
			if node == "" {
				node = "(root)"
			}
			header := "  " + node
			if g.Record != "" && g.Record != node {
				header += "  ← " + g.Record
			}
			fmt.Fprintln(w, header)
			fmt.Fprintf(w, "    %-24s %-22s %5s %6s  %-9s %s\n", "STEP", "ACTIVITY", "RUNS", "FAILED", "LAST", "LAST(ms)")
			for _, s := range g.Steps {
				ms := ""
				if s.LastMs > 0 {
					ms = strconv.FormatInt(s.LastMs, 10)
				}
				// FAILED is blank rather than 0 so a lane with red iterations
				// behind a green one is the only thing in the column.
				failed := ""
				if s.Failed > 0 {
					failed = strconv.Itoa(s.Failed)
				}
				fmt.Fprintf(w, "    %-24s %-22s %5d %6s  %-9s %s\n",
					truncate(s.StepID, 24), truncate(s.Activity, 22), s.Runs, failed, s.Result, ms)
			}
		}
	}
}

// outcomeLine renders the run's verdict, or "" when there is nothing to say
// (the workflow declared no outcome and its lifecycle already speaks for
// itself). Absence of a declared outcome is NEVER rendered as failure.
func outcomeLine(r statusReport) string {
	switch r.Outcome {
	case execfollow.OutcomeFailure:
		return "✗ FAILURE — the run reached a terminal node that declares failure; it ran to the end and did not pass"
	case execfollow.OutcomeSuccess:
		return "✓ SUCCESS"
	}
	if r.Status == "completed" {
		return "(not declared — this workflow does not state a pass/fail verdict)"
	}
	return ""
}

func printStatusNode(w io.Writer, n statusNode, depth int) {
	indent := strings.Repeat("  ", depth)
	// A gated thread stays RUNNING in the stored status, so say GATED instead —
	// otherwise a fanned-out run shows every node as RUNNING and the one that
	// actually needs a human is indistinguishable from the ones that are busy.
	state := strings.ToUpper(n.Status)
	if n.Gate != "" {
		state = "GATED"
	}
	line := fmt.Sprintf("%s%s  [%s]", indent, n.NodeID, state)
	if n.Gate != "" {
		line += fmt.Sprintf("  ⏸ %s", n.Gate)
	}
	if n.Iteration != nil {
		line += fmt.Sprintf("  iter=%d", *n.Iteration)
	}
	if n.DurationMs > 0 {
		line += fmt.Sprintf("  %s", humanDuration(n.DurationMs))
	}
	fmt.Fprintln(w, line)
	for _, c := range n.Children {
		printStatusNode(w, c, depth+1)
	}
}

// ============================================================================
// workflow questions
// ============================================================================

type questionsReport struct {
	ExecutionID  string             `json:"execution_id"`
	QuestionID   string             `json:"question_id,omitempty"`
	StepID       string             `json:"step_id,omitempty"`
	Stuck        bool               `json:"stuck,omitempty"`
	SubQuestions []subQuestionValue `json:"sub_questions"`
}

type subQuestionValue struct {
	Question      string        `json:"question"`
	AllowMultiple bool          `json:"allow_multiple,omitempty"`
	Options       []optionValue `json:"options,omitempty"`
}

type optionValue struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func newWorkflowQuestionsCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "questions <execution-id>",
		Short: "List the open question(s) awaiting input on a workflow execution",
		Long: `Fetches the pending question for a workflow execution via
QuestionService.GetPendingQuestion and prints each sub-question's prompt and
option labels. A single ask_user question may bundle multiple sub-questions;
all are shown.

Prints "No open questions." and exits 0 when nothing is pending.
Answer with 'reliant workflow answer <execution-id>'.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowQuestions(cmd, args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runWorkflowQuestions(cmd *cobra.Command, executionID string, jsonOut bool) error {
	clients, err := newSuperviseClients(cmd)
	if err != nil {
		return err
	}

	resp, err := clients.question.GetPendingQuestion(cmd.Context(), connect.NewRequest(&reliantv1.GetPendingQuestionRequest{
		ChatId: executionID,
	}))
	if err != nil {
		return clients.rpcError(err, "fetching pending question")
	}

	qi := resp.Msg.GetQuestion()
	if qi == nil {
		if jsonOut {
			return printJSONIndent(cmd.OutOrStdout(), questionsReport{ExecutionID: executionID, SubQuestions: []subQuestionValue{}})
		}
		fmt.Fprintln(cmd.OutOrStdout(), "No open questions.")
		return nil
	}

	report := questionsReport{
		ExecutionID:  executionID,
		QuestionID:   qi.GetQuestionId(),
		StepID:       qi.GetStepId(),
		Stuck:        execfollow.IsStuckStep(qi.GetStepId()),
		SubQuestions: []subQuestionValue{},
	}
	if md, ok := askuser.ParseMetadata(qi.GetMetadata()); ok {
		for _, q := range md.Questions {
			sv := subQuestionValue{Question: q.Question, AllowMultiple: q.AllowMultiple}
			for _, o := range q.Options {
				sv.Options = append(sv.Options, optionValue{Label: o.Label, Description: o.Description})
			}
			report.SubQuestions = append(report.SubQuestions, sv)
		}
	}

	if jsonOut {
		return printJSONIndent(cmd.OutOrStdout(), report)
	}

	out := cmd.OutOrStdout()
	if report.Stuck {
		fmt.Fprintln(out, "⚠ STUCK ESCALATION — the workflow parked for human help (not a routine review).")
	}
	fmt.Fprintf(out, "Open question %s", report.QuestionID)
	if report.StepID != "" {
		fmt.Fprintf(out, " (node %s)", report.StepID)
	}
	fmt.Fprintln(out)
	if len(report.SubQuestions) == 0 {
		fmt.Fprintln(out, "  (question has no structured ask_user prompts — inspect raw metadata via --json)")
		return nil
	}
	for i, sq := range report.SubQuestions {
		multi := ""
		if sq.AllowMultiple {
			multi = " (multiple allowed)"
		}
		fmt.Fprintf(out, "\n  %d. %s%s\n", i+1, sq.Question, multi)
		for _, o := range sq.Options {
			if o.Description != "" {
				fmt.Fprintf(out, "       - %s — %s\n", o.Label, o.Description)
			} else {
				fmt.Fprintf(out, "       - %s\n", o.Label)
			}
		}
	}
	return nil
}

// ============================================================================
// workflow answer
// ============================================================================

func newWorkflowAnswerCmd() *cobra.Command {
	var (
		questionID  string
		selects     []string
		text        string
		interactive bool
	)
	cmd := &cobra.Command{
		Use:   "answer <execution-id>",
		Short: "Answer a pending question on a workflow execution",
		Long: `Answers the pending question for a workflow execution via
QuestionService.ResolveQuestion — no hand-built JSON required. The response is
assembled into the exact ask_user shape the workflow expects
({"answers":[{question, selected, freetext}]}), including multi-sub-question
asks.

Selecting options:
  --select "<label>"   Pick an option by its exact label. Repeatable — one per
                       sub-question, in declaration order. When a label is
                       unambiguous it is matched to whichever sub-question
                       offers it, so order-independence works for distinct
                       option sets.
  --text "<freetext>"  Attach free-text feedback (applies to the first
                       sub-question, or the only one).
  --interactive        Prompt for each sub-question's options on the terminal
                       and pick interactively. This is the default when no
                       --select/--text is given.
  --question <qid>     Target a specific question id (defaults to the pending
                       one). Must match the currently pending question.

Examples:
  reliant workflow answer <id> --select "Continue"
  reliant workflow answer <id> --select "Vanilla" --select "Sprinkles"
  reliant workflow answer <id> --text "please also add rate limiting"
  reliant workflow answer <id>            # interactive picker`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowAnswer(cmd, args[0], questionID, selects, text, interactive)
		},
	}
	cmd.Flags().StringVar(&questionID, "question", "", "Question id to answer (default: the pending question)")
	cmd.Flags().StringArrayVar(&selects, "select", nil, "Option label to select (repeatable, one per sub-question)")
	cmd.Flags().StringVar(&text, "text", "", "Free-text answer/feedback")
	cmd.Flags().BoolVar(&interactive, "interactive", false, "Prompt for each sub-question interactively (default when no --select)")
	return cmd
}

func runWorkflowAnswer(cmd *cobra.Command, executionID, questionID string, selects []string, text string, interactive bool) error {
	clients, err := newSuperviseClients(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()

	resp, err := clients.question.GetPendingQuestion(ctx, connect.NewRequest(&reliantv1.GetPendingQuestionRequest{
		ChatId: executionID,
	}))
	if err != nil {
		return clients.rpcError(err, "fetching pending question")
	}
	qi := resp.Msg.GetQuestion()
	if qi == nil {
		return fmt.Errorf("no pending question to answer on execution %s", executionID)
	}
	if questionID != "" && questionID != qi.GetQuestionId() {
		return fmt.Errorf("question %s is not the pending question (pending is %s)", questionID, qi.GetQuestionId())
	}

	md, ok := askuser.ParseMetadata(qi.GetMetadata())
	if !ok {
		return fmt.Errorf("pending question %s has no structured ask_user prompts — cannot build an answer automatically", qi.GetQuestionId())
	}

	// Default to interactive when the caller supplied no selections/text.
	if len(selects) == 0 && text == "" && !interactive {
		interactive = true
	}

	var answers []askuser.Answer
	if interactive && len(selects) == 0 {
		answers, err = interactiveAnswers(cmd, md, text)
	} else {
		answers, err = assembleAnswers(md, selects, text)
	}
	if err != nil {
		return err
	}

	responseData, err := askuser.BuildResponseData(answers)
	if err != nil {
		return err
	}

	res, err := clients.question.ResolveQuestion(ctx, connect.NewRequest(&reliantv1.ResolveQuestionRequest{
		QuestionId:   qi.GetQuestionId(),
		Action:       "reply",
		ResponseData: &responseData,
	}))
	if err != nil {
		return clients.rpcError(err, "resolving question")
	}
	if !res.Msg.GetSuccess() {
		return fmt.Errorf("server did not confirm the answer (ResolveQuestion returned success=false)")
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Answered question %s\n", qi.GetQuestionId())
	for _, a := range answers {
		if len(a.Selected) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s -> %s\n", truncate(a.Question, 48), strings.Join(a.Selected, ", "))
		}
		if a.Freetext != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s -> %q\n", truncate(a.Question, 48), a.Freetext)
		}
	}
	return nil
}

// assembleAnswers maps --select labels (and optional --text) onto the
// sub-questions. When the number of selects equals the number of sub-questions
// they bind positionally; otherwise each label is matched to whichever
// sub-question offers it. --text attaches to the first sub-question.
func assembleAnswers(md *askuser.Metadata, selects []string, text string) ([]askuser.Answer, error) {
	answers := make([]askuser.Answer, len(md.Questions))
	for i, q := range md.Questions {
		answers[i] = askuser.Answer{Question: q.Question, Selected: []string{}}
	}

	if len(selects) > 0 && len(selects) == len(md.Questions) {
		// Positional binding.
		for i, label := range selects {
			opt, ok := md.Questions[i].MatchOption(label)
			if !ok {
				return nil, fmt.Errorf("option %q is not valid for sub-question %d (%q); valid: %s",
					label, i+1, md.Questions[i].Question, strings.Join(md.Questions[i].OptionLabels(), ", "))
			}
			answers[i].Selected = append(answers[i].Selected, opt.Label)
		}
	} else {
		// Label matching: assign each label to the sub-question that offers it.
		for _, label := range selects {
			matched := -1
			for i, q := range md.Questions {
				if opt, ok := q.MatchOption(label); ok {
					_ = opt
					matched = i
					break
				}
			}
			if matched < 0 {
				return nil, fmt.Errorf("option %q is not offered by any sub-question of this ask", label)
			}
			opt, _ := md.Questions[matched].MatchOption(label)
			answers[matched].Selected = append(answers[matched].Selected, opt.Label)
		}
	}

	if text != "" {
		answers[0].Freetext = text
	}

	// Keep only sub-questions that were actually answered (selected or text).
	var out []askuser.Answer
	for _, a := range answers {
		if len(a.Selected) > 0 || a.Freetext != "" {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no answer provided — pass --select \"<label>\" or --text \"…\"")
	}
	return out, nil
}

// interactiveAnswers prompts each sub-question's options on the terminal.
func interactiveAnswers(cmd *cobra.Command, md *askuser.Metadata, text string) ([]askuser.Answer, error) {
	in := bufio.NewReader(cmd.InOrStdin())
	out := cmd.OutOrStdout()
	var answers []askuser.Answer

	for qi, q := range md.Questions {
		fmt.Fprintf(out, "\n%s\n", q.Question)
		for i, o := range q.Options {
			if o.Description != "" {
				fmt.Fprintf(out, "  %d) %s — %s\n", i+1, o.Label, o.Description)
			} else {
				fmt.Fprintf(out, "  %d) %s\n", i+1, o.Label)
			}
		}
		prompt := "Select"
		if q.AllowMultiple {
			prompt += " (comma-separated for multiple)"
		}
		prompt += ", or type free text: "
		fmt.Fprint(out, prompt)

		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return nil, fmt.Errorf("reading selection: %w", err)
		}
		line = strings.TrimSpace(line)

		ans := askuser.Answer{Question: q.Question, Selected: []string{}}
		if qi == 0 && text != "" {
			ans.Freetext = text
		}
		if line != "" {
			selected, freetext := resolveSelection(q, line)
			ans.Selected = selected
			if freetext != "" {
				ans.Freetext = freetext
			}
		}
		if len(ans.Selected) > 0 || ans.Freetext != "" {
			answers = append(answers, ans)
		}
	}

	if len(answers) == 0 {
		return nil, fmt.Errorf("no answer provided")
	}
	return answers, nil
}

// resolveSelection interprets an interactive input line as option numbers,
// option labels, or free text.
func resolveSelection(q askuser.Question, line string) (selected []string, freetext string) {
	parts := strings.Split(line, ",")
	allMatched := true
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if n, err := strconv.Atoi(p); err == nil && n >= 1 && n <= len(q.Options) {
			selected = append(selected, q.Options[n-1].Label)
			continue
		}
		if opt, ok := q.MatchOption(p); ok {
			selected = append(selected, opt.Label)
			continue
		}
		allMatched = false
	}
	if !allMatched || len(selected) == 0 {
		// Treat the whole line as free text when it doesn't cleanly map to options.
		return nil, line
	}
	return selected, ""
}

// ============================================================================
// workflow terminate / pause / resume
// ============================================================================

func newWorkflowTerminateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "terminate <execution-id>",
		Short: "Terminate a running workflow execution",
		Long: `Terminates the workflow for a chat via ChatService.TerminateChat. Terminate
is terminal and DROPS the resume checkpoint — the workflow cannot be resumed
afterward (start a new run instead). Use 'workflow pause' to stop while
preserving the ability to resume.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clients, err := newSuperviseClients(cmd)
			if err != nil {
				return err
			}
			resp, err := clients.chat.TerminateChat(cmd.Context(), connect.NewRequest(&reliantv1.TerminateChatRequest{ChatId: args[0]}))
			if err != nil {
				return clients.rpcError(err, "terminating workflow")
			}
			return reportMutation(cmd, resp.Msg.GetSuccess(), resp.Msg.GetMessage(), "Terminated workflow "+args[0])
		},
	}
	return cmd
}

func newWorkflowPauseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pause <execution-id>",
		Short: "Pause a running workflow execution (keeps it resumable)",
		Long: `Pauses the workflow for a chat via ChatService.PauseChat. Pause PRESERVES
the resume checkpoint: the workflow stops at a safe point and can be continued
later with 'workflow resume'. Contrast 'workflow terminate', which is terminal
and drops the checkpoint.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clients, err := newSuperviseClients(cmd)
			if err != nil {
				return err
			}
			resp, err := clients.chat.PauseChat(cmd.Context(), connect.NewRequest(&reliantv1.PauseChatRequest{ChatId: args[0]}))
			if err != nil {
				return clients.rpcError(err, "pausing workflow")
			}
			return reportMutation(cmd, resp.Msg.GetSuccess(), resp.Msg.GetMessage(), "Paused workflow "+args[0])
		},
	}
	return cmd
}

func newWorkflowResumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume <execution-id>",
		Short: "Resume a paused or expired workflow execution",
		Long: `Resumes a paused (or expired) workflow for a chat via ChatService.ResumeChat,
continuing from its preserved checkpoint. If the underlying Temporal workflow
was lost, the server reports that a new message is needed to recover.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clients, err := newSuperviseClients(cmd)
			if err != nil {
				return err
			}
			resp, err := clients.chat.ResumeChat(cmd.Context(), connect.NewRequest(&reliantv1.ResumeChatRequest{ChatId: args[0]}))
			if err != nil {
				return clients.rpcError(err, "resuming workflow")
			}
			if resp.Msg.GetNeedsRecovery() {
				fmt.Fprintf(cmd.OutOrStdout(), "Workflow %s needs recovery — send a new message to continue (the Temporal workflow was lost)\n", args[0])
				return nil
			}
			return reportMutation(cmd, resp.Msg.GetSuccess(), resp.Msg.GetMessage(), "Resumed workflow "+args[0])
		},
	}
	return cmd
}

func reportMutation(cmd *cobra.Command, success bool, serverMsg, okMsg string) error {
	if !success {
		if serverMsg != "" {
			return fmt.Errorf("%s", serverMsg)
		}
		return fmt.Errorf("server reported failure")
	}
	if serverMsg != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\n", okMsg, serverMsg)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), okMsg)
	}
	return nil
}

// ============================================================================
// shared helpers
// ============================================================================

func printJSONIndent(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// durationMs computes elapsed milliseconds between two RFC3339 timestamps,
// returning 0 when either is missing or unparseable.
func durationMs(start, end string) int64 {
	if start == "" || end == "" {
		return 0
	}
	s, err1 := time.Parse(time.RFC3339, start)
	e, err2 := time.Parse(time.RFC3339, end)
	if err1 != nil || err2 != nil {
		return 0
	}
	d := e.Sub(s).Milliseconds()
	if d < 0 {
		return 0
	}
	return d
}

func humanDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}
