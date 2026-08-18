// Copyright (c) 2025 Reliant Labs
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/execfollow"
)

// ============================================================================
// Shared read-only DB wiring for run-analysis commands.
//
// `workflow analyze` and `workflow node` read the workflow's execution history
// straight from the database — the source of truth — rather than over the
// Connect RPCs. This is deliberate: the GetWorkflowExecutions RPC strips step
// output_json to keep responses small, so structured node outputs (a review
// node's grade/strategy/feedback verdict) and full per-step timings are only
// available from the DB. Every access here is a SELECT; OpenReadOnlyRepo does
// NOT run migrations.
// ============================================================================

// defaultDevDatabaseURL is the local dev-stack Postgres DSN (the reliant app DB
// hosted by control-plane-postgres). Used only when neither --db-url nor
// DATABASE_URL is set — these are supervision tools run against a dev stack.
//
//nolint:gosec // G101: the well-known local dev-stack DSN, not a real secret
const defaultDevDatabaseURL = "postgres://postgres:postgres@localhost:5434/reliant?sslmode=disable"

func resolveAnalyzeDBURL(flag string) string {
	if strings.TrimSpace(flag) != "" {
		return strings.TrimSpace(flag)
	}
	if env := strings.TrimSpace(os.Getenv("DATABASE_URL")); env != "" {
		return env
	}
	return defaultDevDatabaseURL
}

func openAnalyzeRepo(flag string) (*db.Repo, error) {
	url := resolveAnalyzeDBURL(flag)
	repo, err := db.OpenReadOnlyRepo(url)
	if err != nil {
		return nil, fmt.Errorf("connecting to database: %w\n(set --db-url or DATABASE_URL; default is the local dev stack)", err)
	}
	return repo, nil
}

// runData is the once-loaded snapshot of a chat's workflow run, shared by both
// commands so each does a single read pass.
type runData struct {
	chat         *db.Chat
	workflows    []*db.Workflow
	workflowByID map[string]*db.Workflow
	byThread     map[string]*db.Workflow // thread UUID -> workflow execution
	stepsByWF    map[string][]*db.StepExecution
	messages     []*db.Message
	blocksByMsg  map[string][]*db.MessageContentBlock
	questions    []*db.Question
	// backoffByThread is what each thread lost to LLM provider rate limiting.
	// Wall clock alone cannot separate a slow unit from a rate-limited one, and
	// the retry ladder leaves no other trace: it runs inside one activity attempt,
	// so it produces no message, no step execution and no status change.
	backoffByThread map[string]db.ProviderBackoff
}

func loadRunData(ctx context.Context, repo *db.Repo, chatID string) (*runData, error) {
	chat, err := repo.GetChat(ctx, chatID)
	if err != nil || chat == nil {
		return nil, fmt.Errorf("execution %q not found (looked up as chat id): %v", chatID, err)
	}

	workflows, err := repo.ListWorkflowsByChat(ctx, chatID)
	if err != nil {
		return nil, fmt.Errorf("listing workflow executions: %w", err)
	}

	rd := &runData{
		chat:            chat,
		workflows:       workflows,
		workflowByID:    map[string]*db.Workflow{},
		byThread:        map[string]*db.Workflow{},
		stepsByWF:       map[string][]*db.StepExecution{},
		blocksByMsg:     map[string][]*db.MessageContentBlock{},
		backoffByThread: map[string]db.ProviderBackoff{},
	}
	for _, wf := range workflows {
		rd.workflowByID[wf.ID] = wf
		if wf.Thread != "" {
			rd.byThread[wf.Thread] = wf
		}
		steps, serr := repo.GetStepExecutionsByWorkflow(ctx, wf.ID)
		if serr == nil {
			rd.stepsByWF[wf.ID] = steps
		}
	}

	msgs, err := repo.ListMessages(ctx, chatID, db.MessageListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}
	rd.messages = msgs

	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	if len(ids) > 0 {
		blocks, berr := repo.ListContentBlocksForMessages(ctx, ids)
		if berr == nil {
			for _, b := range blocks {
				rd.blocksByMsg[b.MessageID] = append(rd.blocksByMsg[b.MessageID], b)
			}
		}
	}

	if qs, qerr := repo.ListQuestionsByChat(ctx, chatID); qerr == nil {
		rd.questions = qs
	}

	if backoff, berr := repo.ProviderBackoffByChat(ctx, chatID); berr == nil {
		rd.backoffByThread = backoff
	}

	return rd, nil
}

// ============================================================================
// workflow node — show one node's conversation and its structured verdict.
// ============================================================================

func newWorkflowNodeCmd() *cobra.Command {
	var (
		tail    int
		jsonOut bool
		dbURL   string
	)
	cmd := &cobra.Command{
		Use:   "node <execution-id> <node-id>",
		Short: "Show one node's conversation and structured output (e.g. a review verdict)",
		Long: `Renders a single workflow node's activity read from the database.

For a review / structured-agent node, the node's decision is a structured
response (a response-tool call, e.g. submit_evaluation with grade/strategy/
feedback) rather than plain conversation text. This command surfaces that
verdict at the top — the value that was previously buried in the worker log —
and then renders the node's conversation below it.

A node's activity is found two ways, covering both node shapes:
  - inline nodes tag their messages with the node id (and "<node>-save" steps);
  - nodes that spawn an agent sub-workflow get their own thread, resolved via
    the execution tree's spawned_by_node_id (and its descendant threads).

Use --json for the structured verdict + messages as machine-readable JSON.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowNode(cmd, args[0], args[1], tail, jsonOut, dbURL)
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 30, "Show only the last N messages (0 = all)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&dbURL, "db-url", "", "Database URL (default: $DATABASE_URL, then the local dev stack)")
	return cmd
}

type nodeBlock struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Input   string `json:"input,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

type nodeMessage struct {
	Ordinal int64       `json:"ordinal"`
	Role    string      `json:"role"`
	Agent   string      `json:"agent,omitempty"`
	Model   string      `json:"model,omitempty"`
	Thread  string      `json:"thread,omitempty"`
	Blocks  []nodeBlock `json:"blocks"`
}

// structuredOutput is a response-tool call (verdict / decision) surfaced from a
// node's messages.
type structuredOutput struct {
	Tool   string                 `json:"tool"`
	Agent  string                 `json:"agent,omitempty"`
	Thread string                 `json:"thread,omitempty"`
	Data   map[string]interface{} `json:"data"`
}

type nodeReport struct {
	ExecutionID       string             `json:"execution_id"`
	NodeID            string             `json:"node_id"`
	Threads           []string           `json:"threads,omitempty"`
	StructuredOutputs []structuredOutput `json:"structured_outputs,omitempty"`
	Gates             []analyzeGate      `json:"gates,omitempty"`
	Total             int                `json:"total"`
	Shown             int                `json:"shown"`
	Messages          []nodeMessage      `json:"messages"`
}

func runWorkflowNode(cmd *cobra.Command, executionID, nodeID string, tail int, jsonOut bool, dbURL string) error {
	repo, err := openAnalyzeRepo(dbURL)
	if err != nil {
		return err
	}
	defer func() { _ = repo.Close() }()
	ctx := cmd.Context()

	rd, err := loadRunData(ctx, repo, executionID)
	if err != nil {
		return err
	}

	// Threads owned by this node: the sub-workflows it spawned, plus their
	// descendants (a review node spawns a structured-agent, which may itself
	// spawn tool sub-workflows).
	nodeThreads := collectNodeThreadsDB(rd, nodeID)

	// Match messages: tagged with the node id (inline / "<node>-save") or living
	// in one of the node's threads.
	var matched []*db.Message
	for _, m := range rd.messages {
		if messageBelongsToNodeDB(m, nodeID, nodeThreads) {
			matched = append(matched, m)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].Ordinal < matched[j].Ordinal
		}
		return matched[i].CreatedAt.Before(matched[j].CreatedAt)
	})

	// Extract structured outputs (verdicts / decisions) across all matched msgs.
	var outputs []structuredOutput
	for _, m := range matched {
		outputs = append(outputs, extractStructuredOutputs(m, rd.blocksByMsg[m.ID])...)
	}

	total := len(matched)
	if tail > 0 && total > tail {
		matched = matched[total-tail:]
	}

	// A gate node (e.g. review_checkpoint) produces no conversation of its own —
	// its content is the question it raised. Surface any gate for this node so
	// inspecting the checkpoint shows what was asked/answered.
	var gates []analyzeGate
	for _, g := range buildGates(rd) {
		if g.Node == nodeID {
			gates = append(gates, g)
		}
	}

	report := nodeReport{
		ExecutionID:       executionID,
		NodeID:            nodeID,
		StructuredOutputs: outputs,
		Gates:             gates,
		Total:             total,
		Shown:             len(matched),
		Messages:          []nodeMessage{},
	}
	for t := range nodeThreads {
		report.Threads = append(report.Threads, t)
	}
	sort.Strings(report.Threads)
	for _, m := range matched {
		report.Messages = append(report.Messages, renderNodeMessageDB(m, rd.blocksByMsg[m.ID]))
	}

	if jsonOut {
		return wfaPrintJSON(cmd.OutOrStdout(), report)
	}
	printNodeReport(cmd.OutOrStdout(), report)
	return nil
}

// collectNodeThreadsDB returns the set of thread UUIDs owned by nodeID: threads
// of workflow executions spawned by the node, plus all descendant threads.
func collectNodeThreadsDB(rd *runData, nodeID string) map[string]bool {
	out := map[string]bool{}
	// childrenByParent for descendant walk.
	childrenByParent := map[string][]*db.Workflow{}
	for _, wf := range rd.workflows {
		if wf.ParentID != nil {
			childrenByParent[*wf.ParentID] = append(childrenByParent[*wf.ParentID], wf)
		}
	}
	var addSubtree func(wf *db.Workflow)
	addSubtree = func(wf *db.Workflow) {
		if wf.Thread != "" {
			out[wf.Thread] = true
		}
		for _, c := range childrenByParent[wf.ID] {
			addSubtree(c)
		}
	}
	for _, wf := range rd.workflows {
		if wf.SpawnedByNodeID != nil && *wf.SpawnedByNodeID == nodeID {
			addSubtree(wf)
		}
	}
	return out
}

func messageBelongsToNodeDB(m *db.Message, nodeID string, nodeThreads map[string]bool) bool {
	if m.NodeID != nil && *m.NodeID != "" {
		mn := *m.NodeID
		if mn == nodeID || strings.HasPrefix(mn, nodeID+"-") {
			return true
		}
	}
	if nodeThreads[m.ThreadID] {
		return true
	}
	return false
}

// extractStructuredOutputs finds response-tool calls (verdicts / routing
// decisions) on a message: tool_call blocks whose input is a JSON object,
// emitted by a structured-agent / router, or shaped like a review verdict.
func extractStructuredOutputs(m *db.Message, blocks []*db.MessageContentBlock) []structuredOutput {
	agent := ""
	if m.Agent != nil {
		agent = *m.Agent
	}
	var out []structuredOutput
	for _, b := range blocks {
		if b.BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL {
			continue
		}
		if b.ToolInput == nil || strings.TrimSpace(*b.ToolInput) == "" {
			continue
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(*b.ToolInput), &parsed); err != nil {
			continue
		}
		tool := ""
		if b.ToolName != nil {
			tool = *b.ToolName
		}
		if !looksLikeStructuredResponse(agent, parsed) {
			continue
		}
		out = append(out, structuredOutput{
			Tool:   tool,
			Agent:  agent,
			Thread: m.ThreadID,
			Data:   parsed,
		})
	}
	return out
}

// looksLikeStructuredResponse decides whether a JSON tool-call input is a
// node's structured verdict/decision rather than an ordinary tool call. True
// when the emitting agent is a structured-agent/router, or the payload has the
// shape of a review verdict / routing decision.
func looksLikeStructuredResponse(agent string, data map[string]interface{}) bool {
	a := strings.ToLower(agent)
	if strings.Contains(a, "structured-agent") || strings.Contains(a, "router") {
		return true
	}
	if _, ok := data["grade"]; ok {
		if _, ok2 := data["strategy"]; ok2 {
			return true
		}
	}
	if _, ok := data["strategy"]; ok {
		if _, ok2 := data["feedback"]; ok2 {
			return true
		}
	}
	return false
}

func renderNodeMessageDB(m *db.Message, blocks []*db.MessageContentBlock) nodeMessage {
	nm := nodeMessage{
		Ordinal: m.Ordinal,
		Role:    wfaRoleString(m.Role),
		Thread:  m.ThreadID,
		Blocks:  []nodeBlock{},
	}
	if m.Agent != nil {
		nm.Agent = *m.Agent
	}
	if m.Model != nil {
		nm.Model = *m.Model
	}
	for _, b := range blocks {
		nb := nodeBlock{}
		if b.IsError != nil {
			nb.IsError = *b.IsError
		}
		content := ""
		if b.Content != nil {
			content = *b.Content
		}
		toolName := ""
		if b.ToolName != nil {
			toolName = *b.ToolName
		}
		toolInput := ""
		if b.ToolInput != nil {
			toolInput = *b.ToolInput
		}
		switch b.BlockType {
		case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT:
			nb.Type = "text"
			nb.Text = content
		case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING:
			nb.Type = "thinking"
			nb.Text = content
		case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL:
			nb.Type = "tool_call"
			nb.Tool = toolName
			nb.Input = toolInput
		case reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_RESULT:
			nb.Type = "tool_result"
			nb.Tool = toolName
			nb.Text = content
		default:
			nb.Type = "other"
			nb.Text = content
		}
		nm.Blocks = append(nm.Blocks, nb)
	}
	return nm
}

func printNodeReport(w io.Writer, r nodeReport) {
	fmt.Fprintf(w, "Node %s of execution %s\n", r.NodeID, r.ExecutionID)
	if len(r.Threads) > 0 {
		fmt.Fprintf(w, "  threads: %s\n", strings.Join(wfaShortIDs(r.Threads), ", "))
	}

	if len(r.StructuredOutputs) > 0 {
		fmt.Fprintln(w, "\n── Structured output (verdict) ──")
		for _, so := range r.StructuredOutputs {
			label := so.Tool
			if label == "" {
				label = "response"
			}
			fmt.Fprintf(w, "  %s\n", label)
			printVerdictFields(w, so.Data, "    ")
		}
	}

	if len(r.Gates) > 0 {
		fmt.Fprintln(w, "\n── Gate / question ──")
		for _, g := range r.Gates {
			iter := ""
			if g.Iteration != nil {
				iter = fmt.Sprintf(" iter %d", *g.Iteration)
			}
			fmt.Fprintf(w, "  [%s%s] %s\n", g.Node, iter, g.Status)
			if g.Asked != "" {
				fmt.Fprintf(w, "      asked:    %s\n", g.Asked)
			}
			if g.Answered != "" {
				fmt.Fprintf(w, "      answered: %s\n", g.Answered)
			} else if g.Status == "pending" {
				fmt.Fprintln(w, "      answered: (still pending)")
			}
		}
	}

	if r.Total == 0 {
		if len(r.StructuredOutputs) == 0 && len(r.Gates) == 0 {
			fmt.Fprintln(w, "\nNo messages found for this node.")
			fmt.Fprintln(w, "(Tip: 'reliant-dev workflow analyze' lists the node ids that produced activity.)")
		}
		return
	}

	if r.Shown < r.Total {
		fmt.Fprintf(w, "\n  showing last %d of %d messages (use --tail 0 for all)\n", r.Shown, r.Total)
	} else {
		fmt.Fprintf(w, "\n  %d messages\n", r.Total)
	}

	for _, m := range r.Messages {
		hdr := fmt.Sprintf("#%d %s", m.Ordinal, strings.ToUpper(m.Role))
		if m.Agent != "" {
			hdr += " | " + m.Agent
		}
		fmt.Fprintf(w, "\n=== %s | thread %s ===\n", hdr, wfaShortID(m.Thread))
		for _, b := range m.Blocks {
			printNodeBlock(w, b)
		}
	}
}

// printVerdictFields prints the well-known review-verdict fields (grade /
// strategy / feedback) first and in full, then any remaining keys compactly.
func printVerdictFields(w io.Writer, data map[string]interface{}, indent string) {
	shown := map[string]bool{}
	for _, k := range []string{"grade", "strategy", "feedback"} {
		if v, ok := data[k]; ok {
			fmt.Fprintf(w, "%s%s: %s\n", indent, k, wfaScalar(v))
			shown[k] = true
		}
	}
	for k, v := range data {
		if shown[k] {
			continue
		}
		fmt.Fprintf(w, "%s%s: %s\n", indent, k, wfaTruncate(wfaCollapseWS(wfaScalar(v)), 300))
	}
}

func printNodeBlock(w io.Writer, b nodeBlock) {
	switch b.Type {
	case "text":
		if strings.TrimSpace(b.Text) != "" {
			fmt.Fprintf(w, "%s\n", strings.TrimRight(b.Text, "\n"))
		}
	case "thinking":
		fmt.Fprintf(w, "[thinking] %s\n", wfaTruncate(wfaCollapseWS(b.Text), 500))
	case "tool_call":
		fmt.Fprintf(w, "→ %s(%s)\n", b.Tool, wfaTruncate(wfaCollapseWS(b.Input), 200))
	case "tool_result":
		tag := "← result"
		if b.IsError {
			tag = "← ERROR"
		}
		name := ""
		if b.Tool != "" {
			name = " " + b.Tool
		}
		fmt.Fprintf(w, "%s%s: %s\n", tag, name, wfaTruncate(wfaCollapseWS(b.Text), 400))
	default:
		if strings.TrimSpace(b.Text) != "" {
			fmt.Fprintf(w, "%s\n", wfaTruncate(wfaCollapseWS(b.Text), 400))
		}
	}
}

// ============================================================================
// workflow analyze — one-shot run analysis from the database.
// ============================================================================

func newWorkflowAnalyzeCmd() *cobra.Command {
	var (
		jsonOut bool
		dbURL   string
	)
	cmd := &cobra.Command{
		Use:   "analyze <execution-id>",
		Short: "Analyze a completed/in-progress workflow run from the database",
		Long: `Reconstructs a workflow run from the database (the source of truth) so you
can understand what happened without grepping the worker log. Read-only.

Reports, per the data available:
  - overview: workflow, status, wall-clock, total cost, LLM calls, steps
  - phases:   per workflow-execution timing, model(s), thinking, tokens, cost
              (get-it-right iterations appear as repeated executions)
  - nodes:    per-node step timings and execution (iteration) counts
  - verdicts: review-node grade/strategy/feedback across iterations
  - gates:    the question/approval history — what was asked and answered

Use --json for machine consumption; a readable report is printed by default.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowAnalyze(cmd, args[0], jsonOut, dbURL)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&dbURL, "db-url", "", "Database URL (default: $DATABASE_URL, then the local dev stack)")
	return cmd
}

type analyzeChatInfo struct {
	ID           string `json:"id"`
	Title        string `json:"title,omitempty"`
	WorkflowName string `json:"workflow_name,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

type analyzeOverview struct {
	// Workflow is the workflow that actually RAN — the newest root execution's
	// name. Deliberately not the chat's workflow_name: a pipeline that declares
	// `transition_to` rewrites the chat's workflow on completion, so the chat
	// says "builtin://agent" for a run that was builtin://forge-one-shot. The
	// forensics header must name the run, not what the chat became afterwards.
	Workflow string `json:"workflow"`
	// Status is the LIFECYCLE of the root execution.
	Status string `json:"status"`
	// Outcome is the run's own VERDICT — "success"/"failure" as declared by the
	// terminal node the graph reached — or empty when the workflow declares
	// none. Orthogonal to Status: a run that failed verification routes to its
	// failure terminal and finishes cleanly (completed + failure).
	Outcome       string   `json:"outcome,omitempty"`
	StartedAt     string   `json:"started_at,omitempty"`
	EndedAt       string   `json:"ended_at,omitempty"`
	WallClock     string   `json:"wall_clock"`
	WallClockMs   int64    `json:"wall_clock_ms"`
	Models        []string `json:"models,omitempty"`
	LLMCalls      int      `json:"llm_calls"`
	TotalSteps    int      `json:"total_steps"`
	FailedSteps   int      `json:"failed_steps"`
	WorkflowExecs int      `json:"workflow_execs"`
	TotalCostUSD  float64  `json:"total_cost_usd"`
	PeakCtxTokens int      `json:"peak_ctx_tokens"`
}

type analyzePhase struct {
	Depth        int    `json:"depth"`
	Label        string `json:"label"` // spawned_by node id, or workflow name for a root
	WorkflowName string `json:"workflow_name"`
	Thread       string `json:"thread"`
	Iteration    *int64 `json:"iteration,omitempty"`
	Status       string `json:"status"`
	// Outcome is this execution's own verdict, when its workflow declares one.
	// Child phases usually declare none; the root of a pipeline with a failure
	// terminal does.
	Outcome       string   `json:"outcome,omitempty"`
	StartedAt     string   `json:"started_at,omitempty"`
	Offset        string   `json:"offset,omitempty"` // start relative to run start
	WallClock     string   `json:"wall_clock"`
	WallClockMs   int64    `json:"wall_clock_ms"`
	Models        []string `json:"models,omitempty"`
	Thinking      bool     `json:"thinking"`
	LLMCalls      int      `json:"llm_calls"`
	PeakCtxTokens int      `json:"peak_ctx_tokens"`
	CostUSD       float64  `json:"cost_usd"`
	Steps         int      `json:"steps"`
	// ProviderWaitMs is how much of this phase's wall clock was spent asleep in
	// an LLM provider's rate-limit ladder, and ProviderRetries how many rungs it
	// took. Measured on run b7aa4056: fan-out units whose wall clock read ~129s
	// spent ~113s of it here, which wall clock alone reports as "the model was
	// slow".
	ProviderWaitMs  int64 `json:"provider_wait_ms,omitempty"`
	ProviderRetries int64 `json:"provider_retries,omitempty"`
}

type analyzeNodeTiming struct {
	NodeID     string   `json:"node_id"`
	Execs      int      `json:"execs"` // number of step executions (≈ iteration count)
	Failures   int      `json:"failures"`
	TotalMs    int64    `json:"total_ms"`
	AvgMs      int64    `json:"avg_ms"`
	Activities []string `json:"activities,omitempty"`
}

type analyzeVerdict struct {
	Node      string `json:"node,omitempty"`
	Iteration *int64 `json:"iteration,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Grade     string `json:"grade,omitempty"`
	Strategy  string `json:"strategy,omitempty"`
	Feedback  string `json:"feedback,omitempty"`
}

type analyzeGate struct {
	Node       string `json:"node"`
	Iteration  *int   `json:"iteration,omitempty"`
	Status     string `json:"status"`
	Asked      string `json:"asked,omitempty"`
	Answered   string `json:"answered,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	ResolvedAt string `json:"resolved_at,omitempty"`
}

type analyzeReport struct {
	ExecutionID string              `json:"execution_id"`
	Chat        analyzeChatInfo     `json:"chat"`
	Overview    analyzeOverview     `json:"overview"`
	Phases      []analyzePhase      `json:"phases"`
	NodeTimings []analyzeNodeTiming `json:"node_timings"`
	Verdicts    []analyzeVerdict    `json:"verdicts,omitempty"`
	Gates       []analyzeGate       `json:"gates,omitempty"`
}

func runWorkflowAnalyze(cmd *cobra.Command, executionID string, jsonOut bool, dbURL string) error {
	repo, err := openAnalyzeRepo(dbURL)
	if err != nil {
		return err
	}
	defer func() { _ = repo.Close() }()

	rd, err := loadRunData(cmd.Context(), repo, executionID)
	if err != nil {
		return err
	}
	report := buildAnalyzeReport(executionID, rd)

	if jsonOut {
		return wfaPrintJSON(cmd.OutOrStdout(), report)
	}
	printAnalyzeReport(cmd.OutOrStdout(), report)
	return nil
}

func buildAnalyzeReport(executionID string, rd *runData) analyzeReport {
	rep := analyzeReport{
		ExecutionID: executionID,
		Chat: analyzeChatInfo{
			ID:        rd.chat.ID,
			Title:     rd.chat.Title,
			CreatedAt: wfaTime(rd.chat.CreatedAt),
		},
	}
	if rd.chat.WorkflowName != nil {
		rep.Chat.WorkflowName = *rd.chat.WorkflowName
	}

	// Group messages by thread; collect run-wide aggregates.
	msgsByThread := map[string][]*db.Message{}
	var runStart, runEnd time.Time
	modelSet := map[string]bool{}
	var totalCost float64
	peakCtx := 0
	llmCalls := 0
	for _, m := range rd.messages {
		msgsByThread[m.ThreadID] = append(msgsByThread[m.ThreadID], m)
		if runStart.IsZero() || m.CreatedAt.Before(runStart) {
			runStart = m.CreatedAt
		}
		if m.CreatedAt.After(runEnd) {
			runEnd = m.CreatedAt
		}
		if m.Model != nil && *m.Model != "" {
			modelSet[*m.Model] = true
			if m.Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
				llmCalls++
			}
		}
		if m.Cost != nil {
			totalCost += *m.Cost
		}
		if m.TokenCount != nil && *m.TokenCount > peakCtx {
			peakCtx = *m.TokenCount
		}
	}

	// Run start/end also bounded by workflow + step timestamps.
	totalSteps, failedSteps := 0, 0
	for _, wf := range rd.workflows {
		if runStart.IsZero() || wf.CreatedAt.Before(runStart) {
			runStart = wf.CreatedAt
		}
		if wf.CompletedAt != nil && wf.CompletedAt.After(runEnd) {
			runEnd = *wf.CompletedAt
		}
		for _, s := range rd.stepsByWF[wf.ID] {
			totalSteps++
			if s.Success.Valid && !s.Success.Bool {
				failedSteps++
			}
			if s.CreatedAt.After(runEnd) {
				runEnd = s.CreatedAt
			}
		}
	}

	// Root workflow identity/status/outcome (newest root) for the overview.
	// The NAME comes from here, not from the chat: `transition_to` rewrites the
	// chat's workflow_name when a one-shot pipeline finishes, so the chat would
	// name the workflow the user is talking to NOW rather than the one this run
	// executed — a forensics header contradicting its own phases table.
	rootStatus, rootOutcome, rootName := "", "", ""
	var newestRoot *db.Workflow
	for _, wf := range rd.workflows {
		if wf.ParentID == nil {
			if newestRoot == nil || wf.CreatedAt.After(newestRoot.CreatedAt) {
				newestRoot = wf
			}
		}
	}
	if newestRoot != nil {
		rootStatus = wfaWorkflowStatus(newestRoot.Status)
		rootName = newestRoot.WorkflowName
		if newestRoot.Outcome != nil {
			rootOutcome = *newestRoot.Outcome
		}
	}

	wall := time.Duration(0)
	if !runStart.IsZero() && runEnd.After(runStart) {
		wall = runEnd.Sub(runStart)
	}
	rep.Overview = analyzeOverview{
		Workflow:      rootName,
		Status:        rootStatus,
		Outcome:       rootOutcome,
		StartedAt:     wfaTime(runStart),
		EndedAt:       wfaTime(runEnd),
		WallClock:     wfaDuration(wall),
		WallClockMs:   wall.Milliseconds(),
		Models:        wfaSortedKeys(modelSet),
		LLMCalls:      llmCalls,
		TotalSteps:    totalSteps,
		FailedSteps:   failedSteps,
		WorkflowExecs: len(rd.workflows),
		TotalCostUSD:  totalCost,
		PeakCtxTokens: peakCtx,
	}

	rep.Phases = buildPhases(rd, msgsByThread, runStart)
	rep.NodeTimings = buildNodeTimings(rd)
	rep.Verdicts = buildVerdicts(rd)
	rep.Gates = buildGates(rd)
	return rep
}

// buildPhases renders the workflow-execution tree as an ordered, depth-tagged
// list of phases. Each execution's model/thinking/tokens/cost come from the
// messages in its thread.
func buildPhases(rd *runData, msgsByThread map[string][]*db.Message, runStart time.Time) []analyzePhase {
	childrenByParent := map[string][]*db.Workflow{}
	var roots []*db.Workflow
	for _, wf := range rd.workflows {
		if wf.ParentID == nil {
			roots = append(roots, wf)
		} else {
			childrenByParent[*wf.ParentID] = append(childrenByParent[*wf.ParentID], wf)
		}
	}
	sortWF := func(s []*db.Workflow) {
		sort.Slice(s, func(i, j int) bool { return s[i].CreatedAt.Before(s[j].CreatedAt) })
	}
	sortWF(roots)

	var phases []analyzePhase
	var walk func(wf *db.Workflow, depth int)
	walk = func(wf *db.Workflow, depth int) {
		phases = append(phases, buildPhase(wf, depth, msgsByThread[wf.Thread], rd, runStart))
		kids := childrenByParent[wf.ID]
		sortWF(kids)
		for _, c := range kids {
			walk(c, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return phases
}

func buildPhase(wf *db.Workflow, depth int, msgs []*db.Message, rd *runData, runStart time.Time) analyzePhase {
	label := wf.WorkflowName
	if wf.SpawnedByNodeID != nil && *wf.SpawnedByNodeID != "" {
		label = *wf.SpawnedByNodeID
	}

	// End of this execution: completed_at, else last activity in its thread/steps.
	end := time.Time{}
	if wf.CompletedAt != nil {
		end = *wf.CompletedAt
	}
	modelSet := map[string]bool{}
	thinking := false
	llmCalls := 0
	peakCtx := 0
	var cost float64
	for _, m := range msgs {
		if m.CreatedAt.After(end) {
			end = m.CreatedAt
		}
		if m.Model != nil && *m.Model != "" {
			modelSet[*m.Model] = true
			if m.Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
				llmCalls++
			}
		}
		if m.Cost != nil {
			cost += *m.Cost
		}
		if m.TokenCount != nil && *m.TokenCount > peakCtx {
			peakCtx = *m.TokenCount
		}
		for _, b := range rd.blocksByMsg[m.ID] {
			if b.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_THINKING {
				thinking = true
			}
		}
	}
	steps := 0
	for _, s := range rd.stepsByWF[wf.ID] {
		steps++
		if s.CreatedAt.After(end) {
			end = s.CreatedAt
		}
	}

	wall := time.Duration(0)
	if !end.IsZero() && end.After(wf.CreatedAt) {
		wall = end.Sub(wf.CreatedAt)
	}
	offset := time.Duration(0)
	if !runStart.IsZero() && wf.CreatedAt.After(runStart) {
		offset = wf.CreatedAt.Sub(runStart)
	}

	p := analyzePhase{
		Depth:         depth,
		Label:         label,
		WorkflowName:  wf.WorkflowName,
		Thread:        wf.Thread,
		Status:        wfaWorkflowStatus(wf.Status),
		StartedAt:     wfaTime(wf.CreatedAt),
		Offset:        "+" + wfaDuration(offset),
		WallClock:     wfaDuration(wall),
		WallClockMs:   wall.Milliseconds(),
		Models:        wfaSortedKeys(modelSet),
		Thinking:      thinking,
		LLMCalls:      llmCalls,
		PeakCtxTokens: peakCtx,
		CostUSD:       cost,
		Steps:         steps,
	}
	if wf.Outcome != nil {
		p.Outcome = *wf.Outcome
	}
	if b, ok := rd.backoffByThread[wf.Thread]; ok {
		p.ProviderWaitMs = b.WaitedMs
		p.ProviderRetries = b.Retries
	}
	if wf.LoopIteration != nil {
		p.Iteration = wf.LoopIteration
	}
	return p
}

// buildNodeTimings aggregates step executions across all workflows by step_id.
func buildNodeTimings(rd *runData) []analyzeNodeTiming {
	type agg struct {
		execs      int
		failures   int
		totalMs    int64
		activities map[string]bool
	}
	byNode := map[string]*agg{}
	for _, steps := range rd.stepsByWF {
		for _, s := range steps {
			a := byNode[s.StepID]
			if a == nil {
				a = &agg{activities: map[string]bool{}}
				byNode[s.StepID] = a
			}
			a.execs++
			if s.Success.Valid && !s.Success.Bool {
				a.failures++
			}
			if s.DurationMs.Valid {
				a.totalMs += s.DurationMs.Int64
			}
			if s.ActivityName != "" {
				a.activities[s.ActivityName] = true
			}
		}
	}
	var out []analyzeNodeTiming
	for node, a := range byNode {
		avg := int64(0)
		if a.execs > 0 {
			avg = a.totalMs / int64(a.execs)
		}
		out = append(out, analyzeNodeTiming{
			NodeID:     node,
			Execs:      a.execs,
			Failures:   a.failures,
			TotalMs:    a.totalMs,
			AvgMs:      avg,
			Activities: wfaSortedKeys(a.activities),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalMs != out[j].TotalMs {
			return out[i].TotalMs > out[j].TotalMs
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out
}

// buildVerdicts extracts review verdicts (structured response-tool calls that
// carry grade/strategy/feedback) across the run, tagged with node + iteration.
func buildVerdicts(rd *runData) []analyzeVerdict {
	var verdicts []analyzeVerdict
	for _, m := range rd.messages {
		for _, so := range extractStructuredOutputs(m, rd.blocksByMsg[m.ID]) {
			_, hasGrade := so.Data["grade"]
			_, hasStrategy := so.Data["strategy"]
			if !hasGrade && !hasStrategy {
				continue
			}
			v := analyzeVerdict{
				CreatedAt: wfaTime(m.CreatedAt),
				Tool:      so.Tool,
				Grade:     wfaScalar(so.Data["grade"]),
				Strategy:  wfaScalar(so.Data["strategy"]),
				Feedback:  wfaScalar(so.Data["feedback"]),
			}
			if wf := rd.byThread[m.ThreadID]; wf != nil {
				if wf.SpawnedByNodeID != nil {
					v.Node = *wf.SpawnedByNodeID
				}
				v.Iteration = wf.LoopIteration
			}
			verdicts = append(verdicts, v)
		}
	}
	sort.SliceStable(verdicts, func(i, j int) bool { return verdicts[i].CreatedAt < verdicts[j].CreatedAt })
	return verdicts
}

// buildGates reconstructs the question/gate history from the questions table.
func buildGates(rd *runData) []analyzeGate {
	var gates []analyzeGate
	for _, q := range rd.questions {
		g := analyzeGate{
			Node:      q.StepID,
			Status:    wfaQuestionStatus(q.Status),
			CreatedAt: wfaTime(q.CreatedAt),
			Asked:     summarizeAskUser(q.Metadata),
		}
		if q.LoopIteration != nil {
			g.Iteration = q.LoopIteration
		}
		if q.ResponseData != nil && strings.TrimSpace(*q.ResponseData) != "" {
			g.Answered = summarizeAnswer(*q.ResponseData)
		}
		if q.ResolvedAt != nil {
			g.ResolvedAt = wfaTime(*q.ResolvedAt)
		}
		gates = append(gates, g)
	}
	return gates
}

// summarizeAskUser pulls the human-readable prompt(s) out of an ask_user
// metadata blob: {"type":"ask_user","questions":[{"question":...,"options":[...]}]}.
func summarizeAskUser(metadata *string) string {
	if metadata == nil || strings.TrimSpace(*metadata) == "" {
		return ""
	}
	var meta struct {
		Questions []struct {
			Question string `json:"question"`
			Options  []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal([]byte(*metadata), &meta); err != nil || len(meta.Questions) == 0 {
		return wfaTruncate(wfaCollapseWS(*metadata), 200)
	}
	var parts []string
	for _, q := range meta.Questions {
		s := q.Question
		if len(q.Options) > 0 {
			var labels []string
			for _, o := range q.Options {
				labels = append(labels, o.Label)
			}
			s += " [" + strings.Join(labels, " / ") + "]"
		}
		parts = append(parts, s)
	}
	return wfaTruncate(wfaCollapseWS(strings.Join(parts, "; ")), 300)
}

// summarizeAnswer renders the answer a gate received. response_data is a JSON
// blob whose shape varies; we surface the answers compactly.
func summarizeAnswer(responseData string) string {
	var parsed struct {
		Answers []struct {
			Answer   string   `json:"answer"`
			Reply    string   `json:"reply"`
			Selected []string `json:"selected"`
			Freetext string   `json:"freetext"`
		} `json:"answers"`
	}
	if err := json.Unmarshal([]byte(responseData), &parsed); err == nil && len(parsed.Answers) > 0 {
		var parts []string
		for _, a := range parsed.Answers {
			var seg []string
			if len(a.Selected) > 0 {
				seg = append(seg, strings.Join(a.Selected, ", "))
			}
			if a.Answer != "" {
				seg = append(seg, a.Answer)
			}
			if a.Reply != "" {
				seg = append(seg, a.Reply)
			}
			if a.Freetext != "" {
				seg = append(seg, a.Freetext)
			}
			if len(seg) > 0 {
				parts = append(parts, strings.Join(seg, " — "))
			}
		}
		if len(parts) > 0 {
			return wfaTruncate(wfaCollapseWS(strings.Join(parts, "; ")), 300)
		}
	}
	return wfaTruncate(wfaCollapseWS(responseData), 300)
}

func printAnalyzeReport(w io.Writer, r analyzeReport) {
	o := r.Overview
	fmt.Fprintf(w, "Run analysis for execution %s\n", r.ExecutionID)
	// Name the workflow that RAN. The chat's workflow_name is a different fact
	// — what the chat is running now — and for any pipeline with `transition_to`
	// the two differ the moment the run ends.
	name := o.Workflow
	if name == "" {
		name = r.Chat.WorkflowName
	}
	if name == "" {
		name = "(unknown workflow)"
	}
	fmt.Fprintf(w, "  workflow: %s   status: %s\n", name, o.Status)
	// The verdict, on its own line, because "completed" answers the lifecycle
	// question and says nothing about whether the work passed.
	fmt.Fprintf(w, "  outcome:  %s\n", wfaOutcomeText(o))
	if r.Chat.WorkflowName != "" && r.Chat.WorkflowName != o.Workflow && o.Workflow != "" {
		fmt.Fprintf(w, "  chat now: %s   (the chat transitioned after this run)\n", r.Chat.WorkflowName)
	}
	fmt.Fprintf(w, "  wall-clock: %s   started: %s\n", o.WallClock, o.StartedAt)
	fmt.Fprintf(w, "  LLM calls: %d   steps: %d (%d failed)   execs: %d\n", o.LLMCalls, o.TotalSteps, o.FailedSteps, o.WorkflowExecs)
	fmt.Fprintf(w, "  cost: $%.4f   peak ctx tokens: %s\n", o.TotalCostUSD, wfaThousands(o.PeakCtxTokens))
	if len(o.Models) > 0 {
		fmt.Fprintf(w, "  models: %s\n", strings.Join(o.Models, ", "))
	}

	// Phases. WAIT is the share of WALL the phase spent asleep in an LLM
	// provider's rate-limit ladder — the difference between a unit that was slow
	// and one that was never allowed to start.
	fmt.Fprintln(w, "\n── Phases (workflow executions, in start order) ──")
	fmt.Fprintf(w, "  %-28s %-5s %-11s %8s %14s %6s %8s  %s\n", "PHASE", "ITER", "STATUS", "WALL", "PROVIDER WAIT", "LLM", "COST", "MODEL / THINKING")
	for _, p := range r.Phases {
		indent := strings.Repeat("  ", p.Depth)
		label := wfaTruncate(indent+p.Label, 28)
		iter := ""
		if p.Iteration != nil {
			iter = fmt.Sprintf("%d", *p.Iteration)
		}
		model := strings.Join(p.Models, ",")
		if p.Thinking {
			model += " +think"
		}
		fmt.Fprintf(w, "  %-28s %-5s %-11s %8s %14s %6d %8s  %s\n",
			label, iter, wfaPhaseStatus(p), p.WallClock, wfaProviderWait(p), p.LLMCalls, fmt.Sprintf("$%.3f", p.CostUSD), strings.TrimSpace(model))
	}

	// Node timings
	if len(r.NodeTimings) > 0 {
		fmt.Fprintln(w, "\n── Node timings (step executions across the run) ──")
		fmt.Fprintf(w, "  %-30s %6s %6s %10s %10s\n", "NODE", "EXECS", "FAILS", "TOTAL", "AVG")
		for _, n := range r.NodeTimings {
			fmt.Fprintf(w, "  %-30s %6d %6d %10s %10s\n",
				wfaTruncate(n.NodeID, 30), n.Execs, n.Failures, wfaDurationMs(n.TotalMs), wfaDurationMs(n.AvgMs))
		}
	}

	// Verdicts
	if len(r.Verdicts) > 0 {
		fmt.Fprintln(w, "\n── Review verdicts (get-it-right) ──")
		for _, v := range r.Verdicts {
			iter := ""
			if v.Iteration != nil {
				iter = fmt.Sprintf(" iter %d", *v.Iteration)
			}
			node := v.Node
			if node == "" {
				node = "review"
			}
			fmt.Fprintf(w, "  [%s%s] grade=%s strategy=%s\n", node, iter, wfaOrDash(v.Grade), wfaOrDash(v.Strategy))
			if strings.TrimSpace(v.Feedback) != "" {
				fmt.Fprintf(w, "      %s\n", wfaTruncate(wfaCollapseWS(v.Feedback), 400))
			}
		}
	}

	// Gates
	if len(r.Gates) > 0 {
		fmt.Fprintln(w, "\n── Gate / question history ──")
		for _, g := range r.Gates {
			iter := ""
			if g.Iteration != nil {
				iter = fmt.Sprintf(" iter %d", *g.Iteration)
			}
			fmt.Fprintf(w, "  [%s%s] %s\n", g.Node, iter, g.Status)
			if g.Asked != "" {
				fmt.Fprintf(w, "      asked:    %s\n", g.Asked)
			}
			if g.Answered != "" {
				fmt.Fprintf(w, "      answered: %s\n", g.Answered)
			} else if g.Status == "pending" {
				fmt.Fprintf(w, "      answered: (still pending)\n")
			}
		}
	}
}

// ============================================================================
// small formatting helpers (wfa-prefixed to avoid collisions with the
// supervise command helpers when these files are merged).
// ============================================================================

// wfaProviderWait renders a phase's provider-backoff cost as duration and share
// of its wall clock. A bare duration is not the finding — "113s" reads as noise
// next to a 129s phase until it is named as 87% of it.
// wfaOutcomeText renders the run's verdict for the analyze header. An absent
// declaration is stated as absent — never silently rendered as a pass, and
// never as a failure either.
func wfaOutcomeText(o analyzeOverview) string {
	switch o.Outcome {
	case execfollow.OutcomeFailure:
		return "✗ FAILURE — reached a terminal node declaring failure; the run ended and the work did not pass"
	case execfollow.OutcomeSuccess:
		return "✓ SUCCESS"
	}
	switch o.Status {
	case "running", "paused", "pending":
		return "(still running)"
	default:
		return "(not declared — this workflow states no pass/fail verdict)"
	}
}

func wfaProviderWait(p analyzePhase) string {
	if p.ProviderWaitMs == 0 {
		return "-"
	}
	d := wfaDurationMs(p.ProviderWaitMs)
	if p.WallClockMs <= 0 {
		return d
	}
	return fmt.Sprintf("%s (%d%%)", d, p.ProviderWaitMs*100/p.WallClockMs)
}

func wfaPrintJSON(w io.Writer, v interface{}) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("failed to encode JSON: %w", err)
	}
	return nil
}

func wfaRoleString(r reliantv1.MessageRole) string {
	switch r {
	case reliantv1.MessageRole_MESSAGE_ROLE_USER:
		return "user"
	case reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT:
		return "assistant"
	case reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM:
		return "system"
	case reliantv1.MessageRole_MESSAGE_ROLE_TOOL:
		return "tool"
	default:
		return "unknown"
	}
}

func wfaWorkflowStatus(s db.WorkflowStatus) string {
	if s.State == db.WorkflowStatePending || s.State == db.WorkflowStateActive || s.IsStopped() {
		return s.Label()
	}
	return "unspecified"
}

// wfaPhaseStatus renders a phase's cell. An execution that ran to a
// failure-outcome terminal is COMPLETED and did not pass, and the table has one
// column — so say NOT-PASSED there rather than "completed", which is the exact
// word that made a red run look green.
func wfaPhaseStatus(p analyzePhase) string {
	if p.Outcome == execfollow.OutcomeFailure {
		return "NOT-PASSED"
	}
	return wfaShortStatus(p.Status)
}

func wfaShortStatus(s string) string {
	if len(s) > 9 {
		return s[:9]
	}
	return s
}

func wfaQuestionStatus(s int) string {
	switch s {
	case db.QuestionStatusPending:
		return "pending"
	case db.QuestionStatusResolved:
		return "answered"
	default:
		return fmt.Sprintf("status-%d", s)
	}
}

func wfaShortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func wfaShortIDs(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = wfaShortID(id)
	}
	return out
}

func wfaTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func wfaDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm%ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func wfaDurationMs(ms int64) string {
	return wfaDuration(time.Duration(ms) * time.Millisecond)
}

func wfaScalar(v interface{}) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case bool:
		return fmt.Sprintf("%t", t)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

func wfaOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func wfaTruncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func wfaCollapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func wfaSortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func wfaThousands(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}
