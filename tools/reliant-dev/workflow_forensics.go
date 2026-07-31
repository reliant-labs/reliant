// Copyright (c) 2025 Reliant Labs
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// ============================================================================
// workflow forensics — the mechanical half of a DOGFOOD.md §3 pass.
//
// §3 asks two kinds of question. "How many times did the orchestrator read a
// file?" has an exact answer sitting in the database; "was that turn worth its
// 28k tokens?" does not. This command answers the first kind exhaustively so a
// reviewer's effort goes to the second kind, and so three passes over one run
// stop producing three different numbers.
//
// It reads through loadRunData — the same single-pass snapshot `analyze` and
// `node` use: one SELECT for messages, one batched SELECT for their content
// blocks, one per workflow execution for steps. Nothing here queries per row.
//
// Two invariants this file exists to hold:
//
//  1. A TOOL CALL IS block_type=TOOL_CALL, NEVER TOOL_RESULT. Both rows carry a
//     non-null tool_name, so the obvious `GROUP BY tool_name` over
//     message_content_blocks double-counts every tool that returned — and
//     leaves the ones that did not (spawn, ask_user) at their true count. The
//     result is a histogram that is 2x wrong for most rows and 1x right for a
//     few, which looks plausible enough to publish. countable() is the only
//     place that decides what a call is.
//
//  2. AN EMPTY RESULT IS AN ERROR, NOT A ZERO. A typo'd chat id, a schema that
//     moved, an unpopulated table — each of those renders as "0 tool calls" in
//     a naive tool, which is indistinguishable from a run that made none. Every
//     load path here fails loudly instead. See the guards in runWorkflowForensics.
// ============================================================================

// Tool classification. Reads and writes are named explicitly rather than
// inferred, because the ratio is a headline number in §3 and a silent
// misclassification would move it without leaving a trace.
var (
	forensicsReadTools  = map[string]bool{"view": true}
	forensicsWriteTools = map[string]bool{"edit": true, "write": true, "find_replace": true}
)

func newWorkflowForensicsCmd() *cobra.Command {
	var (
		jsonOut     bool
		dbURL       string
		top         int
		projectRoot string
		showQueries bool
	)
	cmd := &cobra.Command{
		Use:   "forensics <execution-id>",
		Short: "Answer the mechanical questions of a DOGFOOD.md §3 forensics pass",
		Long: `Computes, from the database, the parts of a hardening-run review that have
exact answers — so the reviewer spends their effort on the parts that do not.

Per thread (orchestrator and each spawned unit) and in total:
  - timeline:   spawn, start, end, span, messages, peak context, cost
  - tools:      the per-thread tool-call histogram
  - first write: spawn -> first edit/write/find_replace (a unit reading for ten
                minutes before it edits is briefed badly, not slow)
  - reads:      read/write ratio, and files viewed more than once within a
                thread and across threads (each repeat is re-derivation)
  - skills:     which skills loaded, at what offset from thread start
  - idle:       per unit, from its own finish to the last unit's finish
  - commands:   summed step_executions duration vs wall clock, gap NAMED not
                attributed
  - spawns:     which thread spawned which, and when

It does NOT judge what a turn produced, whether an artifact was read, or
whether a repeat was justified. Those are read from the transcript.

Counting rule: a tool CALL is a TOOL_CALL content block. TOOL_RESULT blocks
carry the same tool_name and are excluded — counting both double-counts every
tool that returned a result. Use --queries to print the SQL behind every number.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowForensics(cmd, args[0], forensicsOpts{
				jsonOut:     jsonOut,
				dbURL:       dbURL,
				top:         top,
				projectRoot: projectRoot,
				showQueries: showQueries,
			})
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&dbURL, "db-url", "", "Database URL (default: $DATABASE_URL, then the local dev stack)")
	cmd.Flags().IntVar(&top, "top", 12, "Histogram rows per thread in table output (0 = all; --json is always complete)")
	cmd.Flags().StringVar(&projectRoot, "project-root", "", "Absolute project path to strip from tool paths, so an absolute and a relative read of one file count as one file")
	cmd.Flags().BoolVar(&showQueries, "queries", false, "Print the SQL each section is derived from")
	return cmd
}

type forensicsOpts struct {
	jsonOut     bool
	dbURL       string
	top         int
	projectRoot string
	showQueries bool
}

// ============================================================================
// Report shape
// ============================================================================

type forensicsToolCount struct {
	Tool  string `json:"tool"`  // display name, mcp__server__ prefix stripped
	Full  string `json:"full"`  // name as recorded
	Count int    `json:"count"`
}

type forensicsSkillLoad struct {
	Skill    string `json:"skill"`
	Action   string `json:"action,omitempty"`
	At       string `json:"at"`
	Offset   string `json:"offset"`
	OffsetMs int64  `json:"offset_ms"`
}

type forensicsRepeatRead struct {
	Path    string   `json:"path"`
	Count   int      `json:"count"`
	Threads []string `json:"threads,omitempty"` // cross-thread only
}

type forensicsThread struct {
	Thread string `json:"thread"`
	Short  string `json:"short"`
	// Kind is root | orchestrator | unit, decided by how the thread's owning
	// workflow execution was spawned, never by name matching.
	Kind         string `json:"kind"`
	Label        string `json:"label,omitempty"` // spawning node id
	Title        string `json:"title,omitempty"` // spawn tool call's title
	ParentThread string `json:"parent_thread,omitempty"`

	SpawnedAt string `json:"spawned_at,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	EndedAt   string `json:"ended_at,omitempty"`
	Span      string `json:"span"`
	SpanMs    int64  `json:"span_ms"`

	Messages       int     `json:"messages"`
	AssistantTurns int     `json:"assistant_turns"`
	PeakCtxTokens  int     `json:"peak_ctx_tokens"`
	CostUSD        float64 `json:"cost_usd"`

	// CostRecorded is false when no message in the thread carried a cost at
	// all. Without it a run whose cost column was never populated reports
	// "$0.00", which reads as a measured free run rather than as absent data.
	CostRecorded bool `json:"cost_recorded"`

	ToolCalls int                  `json:"tool_calls"`
	Histogram []forensicsToolCount `json:"histogram"`

	Reads  int `json:"reads"`
	Writes int `json:"writes"`
	// ReadWriteRatio is reads per write. -1 means no writes at all: a thread
	// that read and never wrote is a distinct finding from a balanced one, and
	// rendering it as 0 would invert the meaning.
	ReadWriteRatio float64 `json:"read_write_ratio"`

	TimeToFirstWrite   string `json:"time_to_first_write,omitempty"`
	TimeToFirstWriteMs int64  `json:"time_to_first_write_ms"`
	FirstWriteTool     string `json:"first_write_tool,omitempty"`
	FirstWritePath     string `json:"first_write_path,omitempty"`
	// FirstWriteFrom names what the offset was measured from — "spawn" when the
	// spawning tool call was located, "thread-start" when it was not. The two
	// are not comparable and the report must not silently mix them.
	FirstWriteFrom string `json:"first_write_from,omitempty"`

	Skills        []forensicsSkillLoad  `json:"skills,omitempty"`
	RepeatedReads []forensicsRepeatRead `json:"repeated_reads,omitempty"`
	RepeatedCost  int                   `json:"repeated_read_calls"` // view calls beyond the first per file

	Spawns int `json:"spawns"`

	IdleAfter   string `json:"idle_after,omitempty"`
	IdleAfterMs int64  `json:"idle_after_ms"`
}

type forensicsSpawn struct {
	ParentThread string `json:"parent_thread"`
	ChildThread  string `json:"child_thread"`
	Title        string `json:"title,omitempty"`
	Preset       string `json:"preset,omitempty"`
	At           string `json:"at"`
	Offset       string `json:"offset"`
	Linked       bool   `json:"linked"` // resolved via tool_call_id, not guessed
}

type forensicsCommandTime struct {
	WallClock     string `json:"wall_clock"`
	WallClockMs   int64  `json:"wall_clock_ms"`
	CommandMs     int64  `json:"command_ms"`
	CommandTime   string `json:"command_time"`
	StepCount     int    `json:"step_count"`
	FailedSteps   int    `json:"failed_steps"`
	// UntimedSteps is steps whose duration_ms was NULL. They contribute nothing
	// to CommandMs, so a nonzero count means command time is a LOWER BOUND.
	UntimedSteps  int    `json:"untimed_steps"`
	UnmeasuredMs  int64  `json:"unmeasured_ms"`
	Unmeasured    string `json:"unmeasured"`
	CommandPct    float64 `json:"command_pct"`
	// Note carries the caveat that makes the gap honest. step_executions is the
	// only record of real command time; everything else in the gap is model
	// generation, engine overhead and gate waiting, which this tool does not
	// separate and must not pretend to.
	Note string `json:"note"`
}

type forensicsTotals struct {
	Threads        int                  `json:"threads"`
	Units          int                  `json:"units"`
	Messages       int                  `json:"messages"`
	AssistantTurns int                  `json:"assistant_turns"`
	ToolCalls      int                  `json:"tool_calls"`
	CostUSD        float64              `json:"cost_usd"`
	Reads          int                  `json:"reads"`
	Writes         int                  `json:"writes"`
	CostRecorded   bool                 `json:"cost_recorded"`
	Histogram      []forensicsToolCount `json:"histogram"`
	IdleMs         int64                `json:"unit_idle_ms"`
	Idle           string               `json:"unit_idle"`
	RepeatedCost   int                  `json:"repeated_read_calls"`
}

type forensicsQuery struct {
	Section string `json:"section"`
	SQL     string `json:"sql"`
}

type forensicsReport struct {
	ExecutionID string               `json:"execution_id"`
	Chat        analyzeChatInfo      `json:"chat"`
	StartedAt   string               `json:"started_at"`
	EndedAt     string               `json:"ended_at"`
	WallClock   string               `json:"wall_clock"`
	ProjectRoot string               `json:"project_root,omitempty"`
	Threads     []forensicsThread    `json:"threads"`
	Totals      forensicsTotals      `json:"totals"`
	SpawnTree   []forensicsSpawn     `json:"spawn_tree,omitempty"`
	CrossThread []forensicsRepeatRead `json:"cross_thread_repeated_reads,omitempty"`
	CommandTime forensicsCommandTime `json:"command_time"`
	Queries     []forensicsQuery     `json:"queries,omitempty"`
}

// ============================================================================
// Entry point + the loud-failure guards
// ============================================================================

func runWorkflowForensics(cmd *cobra.Command, executionID string, opts forensicsOpts) error {
	repo, err := openAnalyzeRepo(opts.dbURL)
	if err != nil {
		return err
	}
	defer func() { _ = repo.Close() }()

	rd, err := loadRunData(cmd.Context(), repo, executionID)
	if err != nil {
		return err
	}

	if err := forensicsGuardInput(executionID, rd); err != nil {
		return err
	}

	report := buildForensicsReport(executionID, rd, opts)

	if err := forensicsGuardOutput(executionID, rd, report); err != nil {
		return err
	}

	if opts.jsonOut {
		return wfaPrintJSON(cmd.OutOrStdout(), report)
	}
	printForensicsReport(cmd.OutOrStdout(), report, opts)
	return nil
}

// forensicsAssistantTurns counts turns the model actually took. It is the
// denominator every guard below reasons against: "no tool calls" is only
// suspicious if somebody was there to make one.
func forensicsAssistantTurns(rd *runData) int {
	n := 0
	for _, m := range rd.messages {
		if m.Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
			n++
		}
	}
	return n
}

// forensicsGuardInput rejects loaded data that cannot support a report, BEFORE
// any number is computed from it. Each case here is one that a naive tool
// renders as a legitimate-looking zero.
func forensicsGuardInput(executionID string, rd *runData) error {
	// A chat with no messages is not a run. loadRunData returns an empty slice
	// for an id that resolved to a row but holds nothing.
	if len(rd.messages) == 0 {
		return fmt.Errorf("no messages for execution %s — nothing to analyze.\n"+
			"The chat row exists but holds no conversation: either the id names a chat that never ran, or messages are in another database.\n"+
			"Check: SELECT count(*) FROM messages WHERE chat_id = '%s';", executionID, executionID)
	}

	// loadRunData deliberately swallows the content-block query's error, since
	// blocks are optional for `analyze`. For forensics the blocks ARE the
	// measurement, so an empty map next to assistant turns is a failed load,
	// not a run that used no tools. This is exactly the guard-that-cannot-fail
	// DOGFOOD.md §4 names: a silent 0 here reads like a real result.
	if assistant := forensicsAssistantTurns(rd); len(rd.blocksByMsg) == 0 && assistant > 0 {
		return fmt.Errorf("loaded %d messages (%d assistant turns) for %s but zero content blocks — refusing to report an empty histogram.\n"+
			"Tool calls live in message_content_blocks; an empty result here means the batched block query failed or the table is not populated, NOT that the run made no tool calls.\n"+
			"Check: SELECT count(*) FROM message_content_blocks b JOIN messages m ON m.id = b.message_id WHERE m.chat_id = '%s';",
			len(rd.messages), assistant, executionID, executionID)
	}
	return nil
}

// forensicsGuardOutput rejects a computed report that is internally impossible:
// turns were taken but nothing was counted, which means the counting rule and
// the stored data disagree — a moved enum, most likely.
func forensicsGuardOutput(executionID string, rd *runData, report forensicsReport) error {
	assistant := forensicsAssistantTurns(rd)
	if report.Totals.ToolCalls == 0 && assistant > 0 {
		return fmt.Errorf("counted 0 tool calls across %d assistant turns in %s — refusing to report zero.\n"+
			"Blocks were loaded, so this means no block matched block_type=CONTENT_BLOCK_TYPE_TOOL_CALL (%d) with a tool_name. The enum or the write path has moved.\n"+
			"Check: SELECT block_type, count(*), count(tool_name) FROM message_content_blocks b JOIN messages m ON m.id = b.message_id WHERE m.chat_id = '%s' GROUP BY 1;",
			assistant, executionID, int32(reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL), executionID)
	}
	return nil
}

// ============================================================================
// Counting primitives — the rules every number below is built from
// ============================================================================

// countableToolCall reports whether a block is a tool INVOCATION. A TOOL_RESULT
// block carries the same tool_name as the call it answers, so counting anything
// but TOOL_CALL inflates every tool that returned — while leaving tools whose
// result was never recorded (spawn, ask_user) at their true count. That mix is
// what makes a hand-rolled histogram wrong in a way that still looks sane.
func countableToolCall(b *db.MessageContentBlock) (string, bool) {
	if b == nil || b.BlockType != reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_TOOL_CALL {
		return "", false
	}
	if b.ToolName == nil {
		return "", false
	}
	name := strings.TrimSpace(*b.ToolName)
	if name == "" {
		return "", false
	}
	return name, true
}

// forensicsDisplayTool strips the mcp__<server>__ wrapper so a histogram reads
// as the tools an agent used rather than how they were routed. The recorded
// name is preserved alongside it in JSON.
func forensicsDisplayTool(name string) string {
	if !strings.HasPrefix(name, "mcp__") {
		return name
	}
	rest := name[len("mcp__"):]
	if i := strings.Index(rest, "__"); i >= 0 {
		return rest[i+2:]
	}
	return rest
}

// forensicsToolPath pulls the file a tool acted on out of its JSON tool_input.
// Different tools name the field differently; an unrecognised shape yields "",
// which callers treat as "no path" rather than inventing one.
func forensicsToolPath(toolInput *string) string {
	if toolInput == nil || strings.TrimSpace(*toolInput) == "" {
		return ""
	}
	var in map[string]interface{}
	if err := json.Unmarshal([]byte(*toolInput), &in); err != nil {
		return ""
	}
	for _, key := range []string{"file_path", "path", "file_glob", "filePath"} {
		if v, ok := in[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// forensicsNormalizePath makes an absolute and a relative reference to one file
// compare equal, so a repeat is not hidden by the form the agent happened to
// use. Without --project-root paths stay verbatim: guessing a root would
// silently merge two different files.
func forensicsNormalizePath(p, projectRoot string) string {
	if p == "" {
		return ""
	}
	if projectRoot != "" {
		root := strings.TrimSuffix(projectRoot, "/")
		if strings.HasPrefix(p, root+"/") {
			return strings.TrimPrefix(p, root+"/")
		}
	}
	return p
}

// forensicsSkillTarget extracts the skill a `skill` call loaded.
func forensicsSkillTarget(toolInput *string) (skill, action string) {
	if toolInput == nil {
		return "", ""
	}
	var in map[string]interface{}
	if err := json.Unmarshal([]byte(*toolInput), &in); err != nil {
		return "", ""
	}
	if v, ok := in["action"].(string); ok {
		action = v
	}
	for _, key := range []string{"path", "skill", "query"} {
		if v, ok := in[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), action
		}
	}
	return "", action
}

// forensicsSpawnMeta reads the title/preset a spawn call was issued with, which
// is the only human-readable name a unit thread ever gets.
func forensicsSpawnMeta(toolInput *string) (title, preset string) {
	if toolInput == nil {
		return "", ""
	}
	var in map[string]interface{}
	if err := json.Unmarshal([]byte(*toolInput), &in); err != nil {
		return "", ""
	}
	if v, ok := in["title"].(string); ok {
		title = v
	}
	if v, ok := in["preset"].(string); ok {
		preset = v
	}
	return title, preset
}

// blockTime prefers the block's own timestamp and falls back to its message's.
func blockTime(b *db.MessageContentBlock, m *db.Message) time.Time {
	if b != nil && !b.CreatedAt.IsZero() {
		return b.CreatedAt
	}
	if m != nil {
		return m.CreatedAt
	}
	return time.Time{}
}

// ============================================================================
// Report construction
// ============================================================================

// threadCall is one counted tool invocation, flattened and time-ordered.
type threadCall struct {
	tool       string
	path       string
	at         time.Time
	toolCallID string
	input      *string
}

func buildForensicsReport(executionID string, rd *runData, opts forensicsOpts) forensicsReport {
	rep := forensicsReport{
		ExecutionID: executionID,
		Chat: analyzeChatInfo{
			ID:        rd.chat.ID,
			Title:     rd.chat.Title,
			CreatedAt: wfaTime(rd.chat.CreatedAt),
		},
		ProjectRoot: opts.projectRoot,
	}
	if rd.chat.WorkflowName != nil {
		rep.Chat.WorkflowName = *rd.chat.WorkflowName
	}

	// --- Thread ownership -------------------------------------------------
	// A thread can carry several workflow executions (a spawn_tool node and the
	// spawn-call_* child it wraps share one). The OWNING execution is the
	// earliest-created of them; its spawned_by_node_id is what classifies the
	// thread.
	owner := map[string]*db.Workflow{}
	for _, wf := range rd.workflows {
		if wf.Thread == "" {
			continue
		}
		cur, ok := owner[wf.Thread]
		if !ok || wf.CreatedAt.Before(cur.CreatedAt) {
			owner[wf.Thread] = wf
		}
	}
	// spawned_by_node_id "spawn-<tool_call_id>" links a child execution back to
	// the exact spawn call that created it. That link is exact; nothing here
	// pairs threads by timestamp proximity.
	threadBySpawnCall := map[string]string{}
	for _, wf := range rd.workflows {
		if wf.SpawnedByNodeID == nil || wf.Thread == "" {
			continue
		}
		if id, ok := strings.CutPrefix(*wf.SpawnedByNodeID, "spawn-"); ok {
			threadBySpawnCall[id] = wf.Thread
		}
	}

	// --- Flatten messages and blocks into per-thread ordered calls ---------
	msgsByThread := map[string][]*db.Message{}
	for _, m := range rd.messages {
		msgsByThread[m.ThreadID] = append(msgsByThread[m.ThreadID], m)
	}
	callsByThread := map[string][]threadCall{}
	for tid, msgs := range msgsByThread {
		sort.Slice(msgs, func(i, j int) bool {
			if msgs[i].CreatedAt.Equal(msgs[j].CreatedAt) {
				return msgs[i].Ordinal < msgs[j].Ordinal
			}
			return msgs[i].CreatedAt.Before(msgs[j].CreatedAt)
		})
		var calls []threadCall
		for _, m := range msgs {
			blocks := rd.blocksByMsg[m.ID]
			sort.Slice(blocks, func(i, j int) bool { return blocks[i].Position < blocks[j].Position })
			for _, b := range blocks {
				name, ok := countableToolCall(b)
				if !ok {
					continue
				}
				c := threadCall{
					tool:  name,
					path:  forensicsNormalizePath(forensicsToolPath(b.ToolInput), opts.projectRoot),
					at:    blockTime(b, m),
					input: b.ToolInput,
				}
				if b.ToolCallID != nil {
					c.toolCallID = *b.ToolCallID
				}
				calls = append(calls, c)
			}
		}
		sort.SliceStable(calls, func(i, j int) bool { return calls[i].at.Before(calls[j].at) })
		callsByThread[tid] = calls
	}

	// --- Spawn tree --------------------------------------------------------
	spawnedAt := map[string]time.Time{}
	spawnParent := map[string]string{}
	spawnTitle := map[string]string{}
	var runStart time.Time
	for _, m := range rd.messages {
		if runStart.IsZero() || m.CreatedAt.Before(runStart) {
			runStart = m.CreatedAt
		}
	}
	for tid, calls := range callsByThread {
		for _, c := range calls {
			if c.tool != "spawn" {
				continue
			}
			child, linked := threadBySpawnCall[c.toolCallID]
			title, preset := forensicsSpawnMeta(c.input)
			sp := forensicsSpawn{
				ParentThread: wfaShortID(tid),
				ChildThread:  wfaShortID(child),
				Title:        title,
				Preset:       preset,
				At:           wfaTime(c.at),
				Offset:       wfaDuration(c.at.Sub(runStart)),
				Linked:       linked,
			}
			if !linked {
				sp.ChildThread = "(unresolved)"
			} else {
				spawnedAt[child] = c.at
				spawnParent[child] = tid
				spawnTitle[child] = title
			}
			rep.SpawnTree = append(rep.SpawnTree, sp)
		}
	}
	sort.Slice(rep.SpawnTree, func(i, j int) bool { return rep.SpawnTree[i].At < rep.SpawnTree[j].At })

	// --- Per-thread rollup -------------------------------------------------
	totalHist := map[string]int{}
	readsByPathGlobal := map[string]map[string]int{} // path -> thread -> count
	var runEnd time.Time

	for tid, msgs := range msgsByThread {
		th := forensicsThread{
			Thread:   tid,
			Short:    wfaShortID(tid),
			Messages: len(msgs),
			Title:    spawnTitle[tid],
		}
		wf := owner[tid]
		switch {
		case wf == nil:
			th.Kind = "unknown"
		case wf.ParentID == nil:
			th.Kind = "root"
		case wf.SpawnedByNodeID != nil && strings.HasPrefix(*wf.SpawnedByNodeID, "spawn"):
			th.Kind = "unit"
		default:
			th.Kind = "orchestrator"
		}
		if wf != nil && wf.SpawnedByNodeID != nil {
			th.Label = *wf.SpawnedByNodeID
		} else if wf != nil {
			th.Label = wf.WorkflowName
		}
		if p, ok := spawnParent[tid]; ok {
			th.ParentThread = wfaShortID(p)
		}

		start, end := msgs[0].CreatedAt, msgs[0].CreatedAt
		for _, m := range msgs {
			if m.CreatedAt.Before(start) {
				start = m.CreatedAt
			}
			if m.CreatedAt.After(end) {
				end = m.CreatedAt
			}
			if m.Role == reliantv1.MessageRole_MESSAGE_ROLE_ASSISTANT {
				th.AssistantTurns++
			}
			if m.Cost != nil {
				th.CostUSD += *m.Cost
				th.CostRecorded = true
			}
			if m.TokenCount != nil && *m.TokenCount > th.PeakCtxTokens {
				th.PeakCtxTokens = *m.TokenCount
			}
		}
		th.StartedAt, th.EndedAt = wfaTime(start), wfaTime(end)
		th.Span, th.SpanMs = wfaDuration(end.Sub(start)), end.Sub(start).Milliseconds()
		if end.After(runEnd) {
			runEnd = end
		}

		// Measure first-write from the spawn call when we have it: a unit's
		// clock starts when it was asked, not when it first spoke.
		measureFrom, fromLabel := start, "thread-start"
		if sa, ok := spawnedAt[tid]; ok {
			measureFrom, fromLabel = sa, "spawn"
			th.SpawnedAt = wfaTime(sa)
		}

		hist := map[string]int{}
		readCounts := map[string]int{}
		var firstWrite *threadCall
		for i := range callsByThread[tid] {
			c := callsByThread[tid][i]
			hist[c.tool]++
			totalHist[c.tool]++
			th.ToolCalls++
			switch {
			case forensicsReadTools[c.tool]:
				th.Reads++
				if c.path != "" {
					readCounts[c.path]++
					if readsByPathGlobal[c.path] == nil {
						readsByPathGlobal[c.path] = map[string]int{}
					}
					readsByPathGlobal[c.path][tid]++
				}
			case forensicsWriteTools[c.tool]:
				th.Writes++
				if firstWrite == nil {
					firstWrite = &callsByThread[tid][i]
				}
			case c.tool == "spawn":
				th.Spawns++
			case c.tool == "skill":
				if name, action := forensicsSkillTarget(c.input); name != "" {
					off := c.at.Sub(start)
					th.Skills = append(th.Skills, forensicsSkillLoad{
						Skill: name, Action: action,
						At: wfaTime(c.at), Offset: wfaDuration(off), OffsetMs: off.Milliseconds(),
					})
				}
			}
		}
		th.Histogram = sortedHistogram(hist)
		if th.Writes > 0 {
			th.ReadWriteRatio = float64(th.Reads) / float64(th.Writes)
		} else {
			th.ReadWriteRatio = -1
		}
		if firstWrite != nil {
			d := firstWrite.at.Sub(measureFrom)
			th.TimeToFirstWrite, th.TimeToFirstWriteMs = wfaDuration(d), d.Milliseconds()
			th.FirstWriteTool, th.FirstWritePath, th.FirstWriteFrom = firstWrite.tool, firstWrite.path, fromLabel
		}
		for p, n := range readCounts {
			if n > 1 {
				th.RepeatedReads = append(th.RepeatedReads, forensicsRepeatRead{Path: p, Count: n})
				th.RepeatedCost += n - 1
			}
		}
		sort.Slice(th.RepeatedReads, func(i, j int) bool {
			if th.RepeatedReads[i].Count != th.RepeatedReads[j].Count {
				return th.RepeatedReads[i].Count > th.RepeatedReads[j].Count
			}
			return th.RepeatedReads[i].Path < th.RepeatedReads[j].Path
		})

		rep.Threads = append(rep.Threads, th)
	}

	// Order: root, then by start time — the order the run actually happened in.
	sort.Slice(rep.Threads, func(i, j int) bool {
		a, b := rep.Threads[i], rep.Threads[j]
		if (a.Kind == "root") != (b.Kind == "root") {
			return a.Kind == "root"
		}
		return a.StartedAt < b.StartedAt
	})

	// --- Unit idle: own finish -> last unit's finish -----------------------
	var lastUnitEnd time.Time
	for _, th := range rep.Threads {
		if th.Kind != "unit" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, th.EndedAt); err == nil && t.After(lastUnitEnd) {
			lastUnitEnd = t
		}
	}
	for i := range rep.Threads {
		if rep.Threads[i].Kind != "unit" || lastUnitEnd.IsZero() {
			continue
		}
		t, err := time.Parse(time.RFC3339, rep.Threads[i].EndedAt)
		if err != nil {
			continue
		}
		idle := lastUnitEnd.Sub(t)
		rep.Threads[i].IdleAfter, rep.Threads[i].IdleAfterMs = wfaDuration(idle), idle.Milliseconds()
		rep.Totals.IdleMs += idle.Milliseconds()
	}
	rep.Totals.Idle = wfaDurationMs(rep.Totals.IdleMs)

	// --- Cross-thread repeated reads --------------------------------------
	for p, byThread := range readsByPathGlobal {
		if len(byThread) < 2 {
			continue
		}
		total := 0
		var threads []string
		for tid, n := range byThread {
			total += n
			threads = append(threads, fmt.Sprintf("%s(%d)", wfaShortID(tid), n))
		}
		sort.Strings(threads)
		rep.CrossThread = append(rep.CrossThread, forensicsRepeatRead{Path: p, Count: total, Threads: threads})
	}
	sort.Slice(rep.CrossThread, func(i, j int) bool {
		if rep.CrossThread[i].Count != rep.CrossThread[j].Count {
			return rep.CrossThread[i].Count > rep.CrossThread[j].Count
		}
		return rep.CrossThread[i].Path < rep.CrossThread[j].Path
	})

	// --- Totals ------------------------------------------------------------
	for _, th := range rep.Threads {
		rep.Totals.Threads++
		if th.Kind == "unit" {
			rep.Totals.Units++
		}
		rep.Totals.Messages += th.Messages
		rep.Totals.AssistantTurns += th.AssistantTurns
		rep.Totals.ToolCalls += th.ToolCalls
		rep.Totals.CostUSD += th.CostUSD
		if th.CostRecorded {
			rep.Totals.CostRecorded = true
		}
		rep.Totals.Reads += th.Reads
		rep.Totals.Writes += th.Writes
		rep.Totals.RepeatedCost += th.RepeatedCost
	}
	rep.Totals.Histogram = sortedHistogram(totalHist)

	rep.StartedAt, rep.EndedAt = wfaTime(runStart), wfaTime(runEnd)
	wall := runEnd.Sub(runStart)
	rep.WallClock = wfaDuration(wall)

	// --- Command time vs the rest -----------------------------------------
	ct := forensicsCommandTime{WallClock: rep.WallClock, WallClockMs: wall.Milliseconds()}
	// duration_ms and success are nullable. A NULL duration is a step that
	// recorded no time, which is not the same as a step that took 0ms — count
	// it as untimed rather than folding a guess into the total.
	for _, steps := range rd.stepsByWF {
		for _, s := range steps {
			ct.StepCount++
			if s.DurationMs.Valid {
				ct.CommandMs += s.DurationMs.Int64
			} else {
				ct.UntimedSteps++
			}
			if s.Success.Valid && !s.Success.Bool {
				ct.FailedSteps++
			}
		}
	}
	ct.CommandTime = wfaDurationMs(ct.CommandMs)
	ct.UnmeasuredMs = ct.WallClockMs - ct.CommandMs
	if ct.UnmeasuredMs < 0 {
		// Steps run concurrently across executions, so the sum can exceed wall
		// clock. Say so rather than printing a negative remainder.
		ct.UnmeasuredMs = 0
		ct.Note = "summed step time exceeds wall clock: steps overlapped across concurrent executions, so command time is not a slice of the timeline here"
	} else {
		ct.Note = "unmeasured = wall clock - summed step_executions. It is model generation, engine overhead and gate waiting COMBINED; step_executions is the only record of real command time and nothing here separates the remainder."
	}
	ct.Unmeasured = wfaDurationMs(ct.UnmeasuredMs)
	if ct.WallClockMs > 0 {
		ct.CommandPct = float64(ct.CommandMs) / float64(ct.WallClockMs) * 100
	}
	rep.CommandTime = ct

	if opts.showQueries {
		rep.Queries = forensicsQueries(rd.chat.ID)
	}
	return rep
}

func sortedHistogram(h map[string]int) []forensicsToolCount {
	out := make([]forensicsToolCount, 0, len(h))
	for name, n := range h {
		out = append(out, forensicsToolCount{Tool: forensicsDisplayTool(name), Full: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tool < out[j].Tool
	})
	return out
}

// forensicsQueries returns the SQL each section is derived from, so any number
// in the report can be checked against the database by hand. DOGFOOD.md's
// standard is that a reported figure is traceable to something that produced
// it; these are that trace.
func forensicsQueries(chatID string) []forensicsQuery {
	q := func(section, sql string) forensicsQuery {
		return forensicsQuery{Section: section, SQL: strings.ReplaceAll(strings.TrimSpace(sql), "$CHAT", "'"+chatID+"'")}
	}
	return []forensicsQuery{
		q("threads / timeline", `
SELECT thread_id, count(*) AS messages, min(created_at) AS started, max(created_at) AS ended,
       max(token_count) AS peak_ctx, sum(cost) AS cost
FROM messages WHERE chat_id = $CHAT GROUP BY thread_id ORDER BY started;`),
		q("tool histogram (block_type=2 ONLY; 3 is the RESULT of the same call)", `
SELECT m.thread_id, b.tool_name, count(*) AS calls
FROM message_content_blocks b JOIN messages m ON m.id = b.message_id
WHERE m.chat_id = $CHAT AND b.block_type = 2 AND b.tool_name IS NOT NULL AND b.tool_name <> ''
GROUP BY 1, 2 ORDER BY 1, calls DESC;`),
		q("double-count check (calls vs results per tool)", `
SELECT b.tool_name, count(*) FILTER (WHERE b.block_type = 2) AS calls,
       count(*) FILTER (WHERE b.block_type = 3) AS results
FROM message_content_blocks b JOIN messages m ON m.id = b.message_id
WHERE m.chat_id = $CHAT AND b.tool_name IS NOT NULL GROUP BY 1 ORDER BY calls DESC;`),
		q("repeated reads (path parsed from tool_input JSON)", `
SELECT m.thread_id, b.tool_input::json ->> 'file_path' AS path, count(*) AS views
FROM message_content_blocks b JOIN messages m ON m.id = b.message_id
WHERE m.chat_id = $CHAT AND b.block_type = 2 AND b.tool_name = 'view'
GROUP BY 1, 2 HAVING count(*) > 1 ORDER BY views DESC;`),
		q("skill loads with offset from thread start", `
SELECT m.thread_id, b.tool_input::json ->> 'path' AS skill, b.created_at
FROM message_content_blocks b JOIN messages m ON m.id = b.message_id
WHERE m.chat_id = $CHAT AND b.block_type = 2 AND b.tool_name = 'skill' ORDER BY b.created_at;`),
		q("spawn tree (child linked by spawned_by_node_id = 'spawn-' || tool_call_id)", `
SELECT w.thread AS child_thread, w.spawned_by_node_id, w.created_at
FROM workflows w WHERE w.chat_id = $CHAT AND w.spawned_by_node_id LIKE 'spawn-%' ORDER BY w.created_at;`),
		q("command time (the ONLY record of real command duration)", `
SELECT s.step_id, s.activity_name, s.success, s.duration_ms, s.created_at
FROM step_executions s JOIN workflows w ON w.id = s.workflow_id
WHERE w.chat_id = $CHAT ORDER BY s.created_at;`),
	}
}

// ============================================================================
// Table rendering
// ============================================================================

func printForensicsReport(w io.Writer, r forensicsReport, opts forensicsOpts) {
	fmt.Fprintf(w, "FORENSICS  %s\n", r.ExecutionID)
	if r.Chat.Title != "" {
		fmt.Fprintf(w, "  chat:       %s\n", r.Chat.Title)
	}
	fmt.Fprintf(w, "  window:     %s -> %s  (%s wall clock)\n", r.StartedAt, r.EndedAt, r.WallClock)
	fmt.Fprintf(w, "  threads:    %d (%d spawned units)   messages: %d   assistant turns: %d\n",
		r.Totals.Threads, r.Totals.Units, r.Totals.Messages, r.Totals.AssistantTurns)
	fmt.Fprintf(w, "  tool calls: %d   reads: %d   writes: %d   cost: %s\n",
		r.Totals.ToolCalls, r.Totals.Reads, r.Totals.Writes, forensicsCost(r.Totals.CostUSD, r.Totals.CostRecorded))
	if !r.Totals.CostRecorded {
		fmt.Fprintf(w, "  NOTE: no message in this run carried a cost — cost is UNRECORDED, not zero.\n")
	}
	if r.ProjectRoot != "" {
		fmt.Fprintf(w, "  paths relative to: %s\n", r.ProjectRoot)
	}

	fmt.Fprintf(w, "\nTIMELINE\n")
	fmt.Fprintf(w, "  %-10s %-13s %-22s %9s %6s %8s %9s %9s\n",
		"THREAD", "KIND", "LABEL", "SPAN", "MSGS", "CALLS", "COST", "IDLE")
	for _, t := range r.Threads {
		label := t.Label
		if t.Title != "" {
			label = t.Title
		}
		fmt.Fprintf(w, "  %-10s %-13s %-22s %9s %6d %8d %9s %9s\n",
			t.Short, t.Kind, wfaTruncate(wfaOrDash(label), 22), t.Span, t.Messages, t.ToolCalls,
			forensicsCost(t.CostUSD, t.CostRecorded), wfaOrDash(t.IdleAfter))
	}
	if r.Totals.IdleMs > 0 {
		fmt.Fprintf(w, "  unit idle total: %s (recoverable if the fan-out were balanced)\n", r.Totals.Idle)
	}

	fmt.Fprintf(w, "\nTOOL CALLS PER THREAD   (TOOL_CALL blocks; TOOL_RESULT excluded)\n")
	for _, t := range r.Threads {
		if t.ToolCalls == 0 {
			continue
		}
		name := t.Short
		if t.Title != "" {
			name = t.Short + " " + t.Title
		} else if t.Label != "" {
			name = t.Short + " " + t.Label
		}
		fmt.Fprintf(w, "\n  %s  [%s]  %d calls\n    ", name, t.Kind, t.ToolCalls)
		hist := t.Histogram
		if opts.top > 0 && len(hist) > opts.top {
			hist = hist[:opts.top]
		}
		parts := make([]string, 0, len(hist))
		for _, h := range hist {
			parts = append(parts, fmt.Sprintf("%s %d", h.Tool, h.Count))
		}
		fmt.Fprintln(w, strings.Join(parts, ", "))
		if opts.top > 0 && len(t.Histogram) > opts.top {
			fmt.Fprintf(w, "    (+%d more tools; --top 0 or --json for all)\n", len(t.Histogram)-opts.top)
		}
	}

	fmt.Fprintf(w, "\nRUN TOTAL\n    ")
	parts := make([]string, 0, len(r.Totals.Histogram))
	for _, h := range r.Totals.Histogram {
		parts = append(parts, fmt.Sprintf("%s %d", h.Tool, h.Count))
	}
	fmt.Fprintln(w, strings.Join(parts, ", "))

	fmt.Fprintf(w, "\nSPAWN -> FIRST WRITE\n")
	fmt.Fprintf(w, "  %-10s %-22s %12s %-13s %s\n", "THREAD", "TITLE", "TO 1ST WRITE", "MEASURED FROM", "FIRST WRITE")
	for _, t := range r.Threads {
		if t.FirstWriteTool == "" {
			if t.Writes == 0 && t.ToolCalls > 0 {
				fmt.Fprintf(w, "  %-10s %-22s %12s %-13s %s\n",
					t.Short, wfaTruncate(wfaOrDash(t.Title), 22), "never", "-", "NO WRITES — thread produced no file changes")
			}
			continue
		}
		fmt.Fprintf(w, "  %-10s %-22s %12s %-13s %s %s\n",
			t.Short, wfaTruncate(wfaOrDash(t.Title), 22), t.TimeToFirstWrite, t.FirstWriteFrom,
			t.FirstWriteTool, wfaTruncate(t.FirstWritePath, 46))
	}

	fmt.Fprintf(w, "\nREAD / WRITE\n")
	fmt.Fprintf(w, "  %-10s %7s %7s %9s %10s\n", "THREAD", "READS", "WRITES", "R:W", "REPEATS")
	for _, t := range r.Threads {
		if t.ToolCalls == 0 {
			continue
		}
		ratio := "no writes"
		if t.ReadWriteRatio >= 0 {
			ratio = fmt.Sprintf("%.1f:1", t.ReadWriteRatio)
		}
		fmt.Fprintf(w, "  %-10s %7d %7d %9s %10d\n", t.Short, t.Reads, t.Writes, ratio, t.RepeatedCost)
	}
	fmt.Fprintf(w, "  %d of %d read calls were re-reads of a file the same thread had already viewed.\n",
		r.Totals.RepeatedCost, r.Totals.Reads)

	printed := false
	for _, t := range r.Threads {
		if len(t.RepeatedReads) == 0 {
			continue
		}
		if !printed {
			fmt.Fprintf(w, "\nREPEATED READS WITHIN A THREAD\n")
			printed = true
		}
		fmt.Fprintf(w, "  %s:\n", t.Short)
		for i, rr := range t.RepeatedReads {
			if i >= 8 {
				fmt.Fprintf(w, "    (+%d more; --json for all)\n", len(t.RepeatedReads)-8)
				break
			}
			fmt.Fprintf(w, "    %2dx  %s\n", rr.Count, rr.Path)
		}
	}

	if len(r.CrossThread) > 0 {
		fmt.Fprintf(w, "\nSAME FILE READ IN MORE THAN ONE THREAD   (context failing to carry)\n")
		for i, rr := range r.CrossThread {
			if i >= 15 {
				fmt.Fprintf(w, "  (+%d more; --json for all)\n", len(r.CrossThread)-15)
				break
			}
			fmt.Fprintf(w, "  %2dx across %d threads  %s\n      %s\n", rr.Count, len(rr.Threads), rr.Path, strings.Join(rr.Threads, " "))
		}
	}

	fmt.Fprintf(w, "\nSKILL LOADS\n")
	any := false
	for _, t := range r.Threads {
		if len(t.Skills) == 0 {
			continue
		}
		any = true
		fmt.Fprintf(w, "  %s (%s):\n", t.Short, wfaOrDash(t.Title))
		for _, s := range t.Skills {
			fmt.Fprintf(w, "    +%-9s %s\n", s.Offset, s.Skill)
		}
	}
	if !any {
		fmt.Fprintf(w, "  none — no thread loaded a skill\n")
	}

	if len(r.SpawnTree) > 0 {
		fmt.Fprintf(w, "\nSPAWN TREE\n")
		for _, s := range r.SpawnTree {
			fmt.Fprintf(w, "  +%-9s %s -> %-10s %s\n", s.Offset, s.ParentThread, s.ChildThread, wfaOrDash(s.Title))
		}
	}

	fmt.Fprintf(w, "\nCOMMAND TIME vs EVERYTHING ELSE\n")
	fmt.Fprintf(w, "  wall clock:        %s\n", r.CommandTime.WallClock)
	fmt.Fprintf(w, "  command time:      %s across %d step executions (%d failed)  = %.1f%%\n",
		r.CommandTime.CommandTime, r.CommandTime.StepCount, r.CommandTime.FailedSteps, r.CommandTime.CommandPct)
	if r.CommandTime.UntimedSteps > 0 {
		fmt.Fprintf(w, "  NOTE: %d step(s) had a NULL duration_ms — command time is a lower bound.\n", r.CommandTime.UntimedSteps)
	}
	fmt.Fprintf(w, "  unmeasured:        %s\n", r.CommandTime.Unmeasured)
	fmt.Fprintf(w, "  %s\n", wfaWrap(r.CommandTime.Note, 76, "  "))

	if len(r.Queries) > 0 {
		fmt.Fprintf(w, "\nQUERIES\n")
		for _, q := range r.Queries {
			fmt.Fprintf(w, "\n-- %s\n%s\n", q.Section, q.SQL)
		}
	}
}

// forensicsCost renders an absent cost as "n/a" rather than "$0.00". The two
// mean opposite things and a reader cannot tell them apart once formatted.
func forensicsCost(v float64, recorded bool) string {
	if !recorded {
		return "n/a"
	}
	return fmt.Sprintf("$%.2f", v)
}

// wfaWrap soft-wraps a note so a long caveat stays readable in a terminal.
func wfaWrap(s string, width int, indent string) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var lines []string
	cur := words[0]
	for _, word := range words[1:] {
		if len(cur)+1+len(word) > width {
			lines = append(lines, cur)
			cur = word
			continue
		}
		cur += " " + word
	}
	lines = append(lines, cur)
	return strings.Join(lines, "\n"+indent)
}
