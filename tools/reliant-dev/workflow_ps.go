// Copyright (c) 2025 Reliant Labs
package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/reconciliation"
)

// ============================================================================
// workflow ps — what each live run is actually doing.
//
// Reads workflow rows straight from the database and DERIVES an honest state
// per row: gated (parked on a human), running (making progress), or stalled
// (suspected wedged). Read-only.
// ============================================================================

// psState is the state `workflow ps` derives for one workflow row. Nothing in
// the database stores it, and that is not an oversight: a workflow parked on a
// signal and a workflow that is wedged are byte-identical in Temporal — RUNNING,
// zero pending work, frozen history. The only honest discriminator is the
// durable DB wait marker that every park point writes BEFORE it parks, which is
// the same invariant internal/workflow/reconciliation relies on. ps reads those
// markers and derives; it never invents a state and never writes one.
type psState string

const (
	// psStateGated: a durable wait marker exists FOR THIS ROW — a pending
	// questions row on this row's thread, a pending approvals row on this row's
	// execution, or this row's own paused status. The run is waiting for a
	// human and is behaving correctly.
	psStateGated psState = "gated"
	// psStateBackoff: a durable provider-backoff marker exists FOR THIS ROW and
	// its declared wait has not elapsed — the thread is asleep inside an LLM
	// provider's retry ladder. It is waiting, like gated, but on a provider
	// rather than a human, so it is NOT reported as gated: there is nothing to
	// answer and nobody to page.
	psStateBackoff psState = "backoff"
	// psStateRunning: no wait marker, and this row's thread produced durable
	// output within psStallAfter — or a descendant did. A parent blocked on its
	// children writes nothing of its own, so its children ARE its progress.
	psStateRunning psState = "running"
	// psStateStalled: no wait marker, no durable output for psStallAfter, and
	// nothing in this row's subtree is running or gated either. SUSPECTED, not
	// asserted — ps is a read-only view; the reconciler is the component that
	// adjudicates a stall and acts on it.
	psStateStalled psState = "stalled"
)

// psWaitKind names which durable wait marker parked a run.
type psWaitKind string

const (
	psWaitQuestion psWaitKind = "question"
	psWaitApproval psWaitKind = "approval"
	psWaitPause    psWaitKind = "pause"
)

// psStallAfter is how long a row may go with no wait marker and no durable
// progress before ps reports it as SUSPECTED stalled. It is the reconciler's own
// detection window, so this view and the component that actually adjudicates
// stalls agree on when "quiet" becomes "suspicious".
const psStallAfter = reconciliation.DefaultProgressStallWindow

func newWorkflowPsCmd() *cobra.Command {
	var (
		jsonOut bool
		dbURL   string
	)
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List live workflow runs and what each one is actually doing",
		Long: `Lists every live THREAD from the database — the threads carrying a RUNNING or
PAUSED workflow row, the same rows the reconciler watches
(db.ListWorkflowsByStatus) — so you can see what is live without grepping worker
logs. One row per thread: the runtime writes a second workflows row for the
inline executor that runs a spawn, and both rows are the same agent doing the
same work.

Each row gets a DERIVED state, per thread, so a run waiting for a human is
distinguishable from one that is wedged:

  gated    a durable wait marker exists for THIS row: a pending question on its
           thread, a pending approval on its execution, or its own paused
           status. Waiting for a human; behaving correctly.
  backoff  the thread is asleep in an LLM provider's rate-limit retry ladder.
           Waiting, like gated, but on the provider — there is nothing to answer.
           The cell reports the attempt and the total time this thread has spent
           in backoff.
  running  no wait marker, and the row's thread produced a message recently.
  stalled  no wait marker and nothing durable for ` + psStallAfter.String() + `. SUSPECTED,
           not asserted — ps is read-only; the reconciler adjudicates stalls.

A row with no output of its own is judged on its subtree, at any depth: a parent
blocked on its children writes no message by design, so a thread with running
children is running, and a thread whose whole subtree is gated is gated. Only a
thread with nothing moving anywhere below it is reported as stalled. The subtree
is the thread tree (threads.parent_thread_id) — workflows.parent_id points a
spawned unit at the ROOT execution, not at the thread that spawned it.

SINCE is time in that state (when the gate opened / since last progress) — the
number that separates "waiting 90 seconds" from "abandoned"; AGE is the thread's
total lifetime. Spawned agents appear as their own rows, sharing the parent's
chat id, and are attributed individually — one thread's gate is never shown
against its siblings.

Read-only: every access is a SELECT and no migrations are run.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowPs(cmd, jsonOut, dbURL)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&dbURL, "db-url", "", "Database URL (default: $DATABASE_URL, then the local dev stack)")
	return cmd
}

// psRow is one live THREAD as reported by `workflow ps` — one unit of work, not
// one workflows row. The runtime writes more than one workflows row per thread:
// a spawn creates the `builtin://agent` row and then the inline executor that
// runs it creates a second row, `thread:spawn-<toolcall>`, carrying the SAME
// thread. Both are the same agent doing the same work, so a per-row view
// reported twelve spawned units as twenty-one live runs.
type psRow struct {
	ChatID        string `json:"chat_id"`
	WorkflowID    string `json:"workflow_id"` // representative row for the thread (the one that created it)
	Thread        string `json:"thread"`
	WorkflowName  string `json:"workflow_name"`
	State         string `json:"state"`                  // derived: gated | backoff | running | stalled
	ViaChildren   bool   `json:"via_children,omitempty"` // state came from this row's subtree, not from its own output
	ParentThread  string `json:"parent_thread,omitempty"`
	Node          string `json:"node,omitempty"`
	SpawnedByNode string `json:"spawned_by_node,omitempty"`
	GateKind      string `json:"gate_kind,omitempty"`   // question | approval | pause
	Gate          string `json:"gate,omitempty"`        // step/node that raised the wait marker
	GatePrompt    string `json:"gate_prompt,omitempty"` // short summary of the gate
	SinceSeconds  int64  `json:"since_seconds"`         // time in the derived state
	Since         string `json:"since"`
	AgeSeconds    int64  `json:"age_seconds"` // total lifetime of the thread
	Age           string `json:"age"`

	// Provider backoff detail, set whenever this thread has EVER been rate
	// limited — not only while it is parked. A unit that spent 87% of its life in
	// backoff and is now working is still the answer to "why is this slow".
	BackoffAttempt     int64 `json:"backoff_attempt,omitempty"`      // request number that failed, while parked
	BackoffMaxAttempts int64 `json:"backoff_max_attempts,omitempty"` // the driver's retry ceiling
	BackoffStatus      int64 `json:"backoff_status,omitempty"`       // provider HTTP status (429, 503…)
	BackoffRetries     int64 `json:"backoff_retries,omitempty"`      // cumulative retries on this thread
	BackoffWaitedMs    int64 `json:"backoff_waited_ms,omitempty"`    // cumulative time asleep in backoff
}

// psWait is a durable wait marker found for one workflow row.
type psWait struct {
	kind    psWaitKind
	step    string    // step/node that raised it (questions only)
	summary string    // short human-readable description
	since   time.Time // when the marker was written; zero when the schema does not record it
}

// psChatMarkers is one chat's durable wait markers plus its per-thread progress,
// loaded once per chat and shared by that chat's rows.
//
// Note the asymmetry, and that it is a property of the SCHEMA, not of this
// command: a questions row carries thread_id (and workflow_id), so a question
// attributes to exactly one thread. An approvals row carries neither — its
// columns are id, chat_id, approval_type, entity_id, status, denial_reason,
// title, metadata, created_at, resolved_at, action_taken, temporal_workflow_id
// — so the only execution identity an approval records is the Temporal
// execution that will receive its signal, which for an inline spawned child is
// the PARENT. Approvals therefore cannot be attributed to a spawned thread by
// any join available here.
type psChatMarkers struct {
	questionByThread map[string]*db.Question   // workflow.Thread -> newest pending question
	approvalsByExec  map[string][]*db.Approval // approval.TemporalWorkflowID -> pending approvals
	lastActivity     map[string]time.Time      // workflow.Thread -> newest message
	parentThread     map[string]string         // threads.id -> threads.parent_thread_id, the spawn tree
	// backoffByThread is the provider-backoff marker per thread: waiting_since is
	// set while the thread is asleep in a provider retry ladder, and the
	// cumulative counters persist after it wakes.
	backoffByThread map[string]db.ProviderBackoff
}

func runWorkflowPs(cmd *cobra.Command, jsonOut bool, dbURL string) error {
	repo, err := openAnalyzeRepo(dbURL)
	if err != nil {
		return err
	}
	defer func() { _ = repo.Close() }()
	ctx := cmd.Context()

	// Live rows are RUNNING *and* PAUSED. A paused row is a run parked on
	// signal.resume — the clearest "waiting for a human" there is — and pause
	// propagates chat-wide, so listing only RUNNING hides a paused chat
	// completely.
	var workflows []*db.Workflow
	for _, status := range []db.WorkflowStatus{db.WorkflowStatusRunning, db.WorkflowStatusPaused} {
		batch, listErr := repo.ListWorkflowsByStatus(ctx, status)
		if listErr != nil {
			return fmt.Errorf("listing %s workflows: %w", wfaWorkflowStatus(status), listErr)
		}
		workflows = append(workflows, batch...)
	}

	markers := map[string]psChatMarkers{}
	nodes := map[string]string{}
	for _, wf := range workflows {
		if _, loaded := markers[wf.ChatID]; !loaded {
			m, mErr := loadPsChatMarkers(ctx, repo, wf.ChatID)
			if mErr != nil {
				return mErr
			}
			markers[wf.ChatID] = m
		}
		nodes[wf.ID] = psNode(ctx, repo, wf)
	}

	rows := buildPsRows(workflows, markers, nodes, time.Now())

	if jsonOut {
		return wfaPrintJSON(cmd.OutOrStdout(), rows)
	}
	printPsRows(cmd.OutOrStdout(), rows)
	printPsUnattributedApprovals(cmd.OutOrStdout(), workflows, markers)
	return nil
}

// loadPsChatMarkers reads every durable wait marker, progress marker, and spawn
// edge for one chat in five queries. Errors are returned, never swallowed: a
// failed marker query would make a gated run look like it is running, which is
// the exact falsehood this command exists to remove.
func loadPsChatMarkers(ctx context.Context, repo *db.Repo, chatID string) (psChatMarkers, error) {
	m := psChatMarkers{
		questionByThread: map[string]*db.Question{},
		approvalsByExec:  map[string][]*db.Approval{},
		lastActivity:     map[string]time.Time{},
		parentThread:     map[string]string{},
		backoffByThread:  map[string]db.ProviderBackoff{},
	}

	questions, err := repo.PendingQuestionsByThread(ctx, chatID)
	if err != nil {
		return m, fmt.Errorf("reading questions for chat %s: %w", chatID, err)
	}
	m.questionByThread = questions

	approvals, err := repo.ListPendingApprovalsByChat(ctx, chatID)
	if err != nil {
		return m, fmt.Errorf("reading approvals for chat %s: %w", chatID, err)
	}
	for _, a := range approvals {
		m.approvalsByExec[a.TemporalWorkflowID] = append(m.approvalsByExec[a.TemporalWorkflowID], a)
	}

	activity, err := repo.LastThreadActivityByChat(ctx, chatID)
	if err != nil {
		return m, fmt.Errorf("reading thread activity for chat %s: %w", chatID, err)
	}
	m.lastActivity = activity

	backoff, err := repo.ProviderBackoffByChat(ctx, chatID)
	if err != nil {
		return m, fmt.Errorf("reading provider backoff for chat %s: %w", chatID, err)
	}
	m.backoffByThread = backoff

	// threads.parent_thread_id is the spawn tree. workflows.parent_id is NOT:
	// see psThreadChildren.
	threadRows, err := repo.ListThreadsByConversation(ctx, chatID)
	if err != nil {
		return m, fmt.Errorf("reading threads for chat %s: %w", chatID, err)
	}
	for _, t := range threadRows {
		if t.ParentThreadID != nil && *t.ParentThreadID != "" {
			m.parentThread[t.ID] = *t.ParentThreadID
		}
	}

	return m, nil
}

// psThreadGroup is every live workflows row that belongs to ONE thread, oldest
// first. The oldest row is the representative: it is the row that created the
// thread, and it carries the workflow the unit is actually running
// (`builtin://agent`) rather than the inline-node label the second row carries
// (`thread:spawn-<toolcall>`).
type psThreadGroup struct {
	chatID string
	thread string
	rows   []*db.Workflow
}

func (g psThreadGroup) rep() *db.Workflow { return g.rows[0] }

// psGroupByThread collapses live workflow rows to one group per thread.
//
// Thread ids are the threads table's primary key, so they are unique across
// chats and need no chat qualifier here.
func psGroupByThread(workflows []*db.Workflow) []psThreadGroup {
	byThread := map[string]*psThreadGroup{}
	order := make([]string, 0, len(workflows))
	for _, wf := range workflows {
		key := wf.Thread
		g, ok := byThread[key]
		if !ok {
			g = &psThreadGroup{chatID: wf.ChatID, thread: wf.Thread}
			byThread[key] = g
			order = append(order, key)
		}
		g.rows = append(g.rows, wf)
	}
	groups := make([]psThreadGroup, 0, len(order))
	for _, thread := range order {
		g := byThread[thread]
		// Oldest first, id as the tiebreak so the representative is stable
		// across invocations rather than following map iteration order.
		sort.SliceStable(g.rows, func(i, j int) bool {
			if !g.rows[i].CreatedAt.Equal(g.rows[j].CreatedAt) {
				return g.rows[i].CreatedAt.Before(g.rows[j].CreatedAt)
			}
			return g.rows[i].ID < g.rows[j].ID
		})
		groups = append(groups, *g)
	}
	return groups
}

// psThreadChildren builds the spawn tree the rollup walks, over
// threads.parent_thread_id.
//
// workflows.parent_id does NOT express spawn parentage. Measured on
// forge-one-shot exec 83eef1c5: the eleven `builtin://agent` rows spawned from
// thread:implement all carry parent_id = the ROOT execution, as does the
// thread:implement row itself. Only the redundant `thread:spawn-<toolcall>` row
// points at its spawner — and it shares that spawner's thread, so it is the same
// unit of work, not a level of nesting. Rolling up over parent_id therefore made
// every fan-out parent a leaf: thread:build_mvp read stalled? for 18m45s with
// ten live grandchildren. The same run's threads.parent_thread_id records the
// real tree: root -> build_mvp -> implement -> the eleven agents.
//
// An intermediate thread whose own row already went terminal is not in the live
// set, so each thread attaches to its nearest LIVE ancestor. Dropping the edge
// instead would strand the grandchildren and re-create the false stall this
// exists to remove — and the intermediate cannot be assumed to have cascaded,
// because the cascade also runs over parent_id, under which those grandchildren
// are its SIBLINGS.
func psThreadChildren(groups []psThreadGroup, markers map[string]psChatMarkers) map[string][]string {
	live := make(map[string]bool, len(groups))
	for _, g := range groups {
		live[g.thread] = true
	}
	children := map[string][]string{}
	for _, g := range groups {
		parents := markers[g.chatID].parentThread
		seen := map[string]bool{g.thread: true}
		for p := parents[g.thread]; p != "" && !seen[p]; p = parents[p] {
			seen[p] = true
			if live[p] {
				children[p] = append(children[p], g.thread)
				break
			}
		}
	}
	return children
}

// buildPsRows turns live workflow rows into report rows, one per thread. Pure:
// everything it reads is an argument, so the per-thread attribution it performs
// is testable without a database.
func buildPsRows(workflows []*db.Workflow, markers map[string]psChatMarkers, nodes map[string]string, now time.Time) []psRow {
	groups := psGroupByThread(workflows)

	// Pass 1: what each thread's OWN evidence says. A thread's own evidence is
	// its newest message and any durable wait marker written against it.
	own := make(map[string]psOwnState, len(groups))
	waits := make(map[string]*psWait, len(groups))
	for _, g := range groups {
		m := markers[g.chatID]

		lastProgress := g.rep().CreatedAt
		if seen, ok := m.lastActivity[g.thread]; ok && seen.After(lastProgress) {
			lastProgress = seen
		}

		wait := psWaitFor(g, m)
		backoff := m.backoffByThread[g.thread]
		state, since := derivePsState(wait, backoff, lastProgress, now, psStallAfter)
		own[g.thread] = psOwnState{state: state, since: since}
		waits[g.thread] = wait
	}

	children := psThreadChildren(groups, markers)

	// Pass 2: roll the tree up. A parent blocked on its children writes no
	// message of its own while they work, so its own evidence is silence — which
	// pass 1 can only read as a suspected stall. Its children are the progress
	// signal it does not have.
	memo := make(map[string]psResolved, len(groups))
	rows := make([]psRow, 0, len(groups))
	for _, g := range groups {
		resolved := resolvePsSubtreeState(g.thread, own, children, memo, map[string]bool{})
		rep := g.rep()

		row := psRow{
			ChatID:       g.chatID,
			WorkflowID:   rep.ID,
			Thread:       g.thread,
			WorkflowName: rep.WorkflowName,
			ParentThread: markers[g.chatID].parentThread[g.thread],
			State:        string(resolved.state),
			ViaChildren:  resolved.viaChildren,
			Node:         psGroupNode(g, nodes),
			SinceSeconds: int64(now.Sub(resolved.since).Seconds()),
			Since:        wfaDuration(now.Sub(resolved.since)),
			AgeSeconds:   int64(now.Sub(rep.CreatedAt).Seconds()),
			Age:          wfaDuration(now.Sub(rep.CreatedAt)),
		}
		if rep.SpawnedByNodeID != nil {
			row.SpawnedByNode = *rep.SpawnedByNodeID
		}
		// Backoff detail is reported from THIS thread's own marker, whether or not
		// the thread is parked right now: the cumulative wait is the answer to
		// "why did this unit produce so little", and it outlives the wait itself.
		if b, ok := markers[g.chatID].backoffByThread[g.thread]; ok {
			row.BackoffRetries = b.Retries
			row.BackoffWaitedMs = b.WaitedMs
			row.BackoffStatus = b.StatusCode
			if psInBackoff(b, now) {
				row.BackoffAttempt = b.Attempt
				row.BackoffMaxAttempts = b.MaxAttempts
				// While parked, the honest total includes the wait in progress.
				row.BackoffWaitedMs = b.WaitedMs + now.Sub(b.WaitingSince).Milliseconds()
			}
		}
		// Gate detail is reported only for a marker written against THIS thread.
		// A parent that is gated because a descendant is gated has no marker of
		// its own to describe; the descendant's own row carries the prompt.
		if wait := waits[g.thread]; wait != nil {
			row.GateKind = string(wait.kind)
			row.Gate = wait.step
			row.GatePrompt = wfaTruncate(wait.summary, 60)
		}
		rows = append(rows, row)
	}

	// Most-in-need-of-attention first: suspected stalls, then gates, then threads
	// parked on a provider, then the runs that are simply working — each
	// longest-in-state first.
	rank := map[string]int{
		string(psStateStalled): 0,
		string(psStateGated):   1,
		string(psStateBackoff): 2,
		string(psStateRunning): 3,
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rank[rows[i].State] != rank[rows[j].State] {
			return rank[rows[i].State] < rank[rows[j].State]
		}
		return rows[i].SinceSeconds > rows[j].SinceSeconds
	})
	return rows
}

// psWaitFor returns the durable wait marker parking this thread, or nil if none
// does.
//
// Every check is scoped to THIS thread. A pause parks the whole Temporal
// execution, so a pause on any of the thread's rows parks the thread; the
// question is matched on thread id. A pending approval attributes only to a row
// that IS its temporal_workflow_id — approvals record no thread anywhere in the
// schema, so an approval raised inside an inline spawned child cannot be
// attributed to that child and is reported separately rather than pinned on a
// sibling that is genuinely executing.
func psWaitFor(g psThreadGroup, m psChatMarkers) *psWait {
	for _, wf := range g.rows {
		if wf.Status == db.WorkflowStatusPaused {
			// A pause records no timestamp: workflows.paused_at was dropped
			// (migration 20260213000000) and the pause path updates status only,
			// so there is nothing to report as "since" here. derivePsState falls
			// back to the thread's last durable output, which is an upper bound
			// on how long the row has been parked.
			return &psWait{kind: psWaitPause, summary: "awaiting resume"}
		}
	}
	if q := m.questionByThread[g.thread]; q != nil {
		return &psWait{
			kind:    psWaitQuestion,
			step:    q.StepID,
			summary: summarizeAskUser(q.Metadata),
			since:   q.CreatedAt,
		}
	}
	var approvals []*db.Approval
	for _, wf := range g.rows {
		approvals = append(approvals, m.approvalsByExec[wf.ID]...)
	}
	if len(approvals) > 0 {
		oldest := approvals[0]
		for _, a := range approvals[1:] {
			if a.CreatedAt.Before(oldest.CreatedAt) {
				oldest = a
			}
		}
		summary := oldest.Title
		if len(approvals) > 1 {
			summary = fmt.Sprintf("%s (+%d more)", summary, len(approvals)-1)
		}
		return &psWait{kind: psWaitApproval, summary: summary, since: oldest.CreatedAt}
	}
	return nil
}

// psGroupNode reports the thread's current node. Checkpoints are keyed by
// workflow row, and a thread's rows do not all carry one, so the first row that
// has a checkpoint answers for the thread.
func psGroupNode(g psThreadGroup, nodes map[string]string) string {
	for _, wf := range g.rows {
		if node := nodes[wf.ID]; node != "" {
			return node
		}
	}
	return ""
}

// derivePsState decides one row's state from its wait markers (if any) and the
// time of its last durable progress, and returns the instant the state began so
// the caller can report time-in-state.
//
// A human gate outranks provider backoff: both are waits, but only one has
// something a supervisor can do about it, and a thread parked on a question is
// not issuing LLM calls anyway — a backoff marker alongside a pending question
// is a leftover, not a live wait.
func derivePsState(wait *psWait, backoff db.ProviderBackoff, lastProgress, now time.Time, stallAfter time.Duration) (psState, time.Time) {
	if wait != nil {
		if !wait.since.IsZero() {
			return psStateGated, wait.since
		}
		// No recorded timestamp for this marker kind (pause); the honest floor
		// is the thread's last durable output.
		return psStateGated, lastProgress
	}
	if psInBackoff(backoff, now) {
		return psStateBackoff, backoff.WaitingSince
	}
	if now.Sub(lastProgress) >= stallAfter {
		return psStateStalled, lastProgress
	}
	return psStateRunning, lastProgress
}

// psInBackoff reports whether a provider-backoff marker describes a wait that is
// happening RIGHT NOW.
//
// The marker is only trusted up to the resume_at the driver itself declared
// before sleeping. That bound is what makes the marker self-cleaning: an
// activity killed mid-sleep leaves its marker open, and without the bound ps
// would report a dead thread as parked forever. Past resume_at the request is
// back in flight (or the thread is gone) and the normal progress derivation is
// the honest answer again.
func psInBackoff(b db.ProviderBackoff, now time.Time) bool {
	return b.Waiting() && !b.ResumeAt.IsZero() && !now.After(b.ResumeAt)
}

// psOwnState is what one thread's own evidence — its newest message and any
// durable wait marker written against it — says about that thread alone.
type psOwnState struct {
	state psState
	since time.Time
}

// psResolved is a thread's reported state after its subtree is taken into
// account. viaChildren records that the state came from descendants rather than
// from evidence on the thread itself.
type psResolved struct {
	state       psState
	since       time.Time
	viaChildren bool
}

// resolvePsSubtreeState reports one thread's state from its own evidence, and
// from its subtree when its own evidence is silence.
//
// A parent thread blocked on its children produces no message of its own by
// design — that is the whole shape of a fan-out — so "no message in the stall
// window" is not evidence of a stall for a thread that has running children. It
// is only evidence of a stall for a LEAF. Reading the parent's newest message as
// the progress signal reported every healthy fan-out as stalled? about ten
// minutes in, which is a false alarm on the one signal this command exists to
// give.
//
// Precedence, on the subtree, keeping the four honest outcomes:
//   - own evidence first: recent output means running, a durable wait marker on
//     this thread means gated or backoff. Direct evidence always beats inference.
//   - otherwise any running descendant means the subtree is working: running.
//   - otherwise any gated descendant means the subtree is waiting on a human:
//     gated.
//   - otherwise any descendant in provider backoff means the subtree is waiting
//     on the provider: backoff. Without this rung a fan-out whose units are ALL
//     rate limited rolls up to the parent as stalled? — a false alarm on exactly
//     the run that most needs the true reason.
//   - otherwise nothing in the subtree is moving, and a leaf with no children
//     has nothing to appeal to: stalled.
//
// Recurses, so it holds at any depth rather than for one level of spawn. The
// spawn tool currently caps depth at 1, but a forge-one-shot run already reaches
// four levels — root -> build_mvp -> implement -> the spawned agents — and a cap
// is not an invariant this derivation should bake in.
//
// Keyed by THREAD id: see psThreadChildren for why the edge is
// threads.parent_thread_id and not workflows.parent_id.
func resolvePsSubtreeState(id string, own map[string]psOwnState, children map[string][]string, memo map[string]psResolved, onStack map[string]bool) psResolved {
	self := own[id]
	mine := psResolved{state: self.state, since: self.since}

	if onStack[id] {
		// A cycle in parent_thread_id. Break it on this thread's own evidence
		// rather than recursing forever, and do not memoize the partial answer.
		return mine
	}
	if r, ok := memo[id]; ok {
		return r
	}
	// Direct evidence beats inference: only silence needs the subtree.
	if self.state != psStateStalled {
		memo[id] = mine
		return mine
	}

	onStack[id] = true
	var (
		sawRunning     bool
		newestRun      time.Time
		sawGated       bool
		longestGated   time.Time
		sawBackoff     bool
		longestBackoff time.Time
	)
	for _, childID := range children[id] {
		c := resolvePsSubtreeState(childID, own, children, memo, onStack)
		switch c.state {
		case psStateRunning:
			sawRunning = true
			if c.since.After(newestRun) {
				newestRun = c.since
			}
		case psStateGated:
			sawGated = true
			if longestGated.IsZero() || c.since.Before(longestGated) {
				longestGated = c.since
			}
		case psStateBackoff:
			sawBackoff = true
			if longestBackoff.IsZero() || c.since.Before(longestBackoff) {
				longestBackoff = c.since
			}
		}
	}
	delete(onStack, id)

	resolved := mine
	switch {
	case sawRunning:
		// SINCE becomes the newest progress anywhere below: the last time this
		// subtree did anything.
		resolved = psResolved{state: psStateRunning, since: newestRun, viaChildren: true}
	case sawGated:
		// SINCE becomes the longest-waiting gate below, so the row sorts by how
		// long a human has been holding the subtree up.
		resolved = psResolved{state: psStateGated, since: longestGated, viaChildren: true}
	case sawBackoff:
		// SINCE becomes the longest-waiting provider park below.
		resolved = psResolved{state: psStateBackoff, since: longestBackoff, viaChildren: true}
	}
	memo[id] = resolved
	return resolved
}

// psNode returns the current node/phase of a running workflow from its position
// checkpoint (last top-level node entered), with the loop iteration appended for
// loop nodes. Empty when no checkpoint exists yet (run hasn't entered a node, or
// it is a spawned child — checkpoints are written for the root run only).
func psNode(ctx context.Context, repo *db.Repo, wf *db.Workflow) string {
	cp, err := repo.GetWorkflowCheckpoint(ctx, wf.ID)
	if err != nil || cp == nil || cp.NodeID == "" {
		return ""
	}
	if cp.LoopIteration > 0 {
		return fmt.Sprintf("%s#%d", cp.NodeID, cp.LoopIteration)
	}
	return cp.NodeID
}

func printPsRows(w io.Writer, rows []psRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No workflows running")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "CHAT/EXEC\tTHREAD\tWORKFLOW\tSTATE\tNODE\tWAITING ON\tSINCE\tAGE")
	for _, r := range rows {
		name := r.WorkflowName
		if r.SpawnedByNode != "" {
			name = fmt.Sprintf("%s (spawn:%s)", name, r.SpawnedByNode)
		}
		state := r.State
		if r.State == string(psStateStalled) {
			state = "stalled?"
		}
		// Full ids, not 8-char prefixes. `ps` is the surface that enumerates
		// live gated runs, and nothing else in the CLI resolves a prefix — so a
		// truncated id here is an id that cannot be fed to `workflow status`,
		// `questions`, `answer`, or `cancel`. The narrower column bought
		// nothing that a supervisor could use.
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ChatID,
			wfaOrDash(r.Thread),
			wfaTruncate(name, 40),
			state,
			wfaOrDash(r.Node),
			psWaitingOn(r),
			r.Since,
			r.Age,
		)
	}
	_ = tw.Flush()

	fmt.Fprintf(w, "\n%s\n", psStateTally(rows))
}

// psWaitingOn renders the WAITING ON cell: what parked this row, and where.
func psWaitingOn(r psRow) string {
	if r.GateKind == "" {
		// A parent gated by a descendant has no marker of its own to describe.
		// Say where the gate is rather than rendering "-", which would read as
		// "gated by nothing".
		if r.State == string(psStateGated) && r.ViaChildren {
			return "⏸ child thread"
		}
		if r.State == string(psStateBackoff) && r.ViaChildren {
			return "⏳ child thread"
		}
		if cell := psBackoffCell(r); cell != "" {
			return cell
		}
		return "-"
	}
	cell := "⏸ " + r.GateKind
	if r.Gate != "" {
		cell = fmt.Sprintf("%s:%s", cell, r.Gate)
	}
	if r.GatePrompt != "" {
		cell = fmt.Sprintf("%s — %s", cell, r.GatePrompt)
	}
	return cell
}

// psBackoffCell renders provider-backoff detail for a row, or "" when the thread
// has never been rate limited.
//
// A thread that is parked RIGHT NOW leads with the attempt; a thread that has
// woken up still reports what it lost, because the cumulative wait is the answer
// to "why did this unit produce so little" long after the wait ended. The number
// that matters is the TOTAL — a unit at attempt 7 of a doubling ladder has been
// waiting two minutes, not the 64 seconds of its current rung.
func psBackoffCell(r psRow) string {
	if r.BackoffRetries == 0 && r.BackoffWaitedMs == 0 {
		return ""
	}
	waited := wfaDuration(time.Duration(r.BackoffWaitedMs) * time.Millisecond)
	if r.State == string(psStateBackoff) {
		return fmt.Sprintf("⏳ provider %d — attempt %d/%d, %s waited",
			r.BackoffStatus, r.BackoffAttempt, r.BackoffMaxAttempts, waited)
	}
	return fmt.Sprintf("(provider %d: %d retries, %s waited)", r.BackoffStatus, r.BackoffRetries, waited)
}

// psStateTally summarizes the run by derived state, so the footer answers the
// question the table was built for rather than repeating a row count.
func psStateTally(rows []psRow) string {
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.State]++
	}
	return fmt.Sprintf("%d live: %d running, %d gated, %d in provider backoff, %d suspected stalled",
		len(rows), counts[string(psStateRunning)], counts[string(psStateGated)],
		counts[string(psStateBackoff)], counts[string(psStateStalled)])
}

// printPsUnattributedApprovals reports pending approvals that match no listed
// run. An approvals row records no thread and its temporal_workflow_id is the
// PARENT execution for an inline spawned child, so such an approval genuinely
// cannot be attributed to a thread. Saying so is the honest alternative to
// showing it against every sibling.
func printPsUnattributedApprovals(w io.Writer, workflows []*db.Workflow, markers map[string]psChatMarkers) {
	listed := map[string]bool{}
	for _, wf := range workflows {
		listed[wf.ID] = true
	}
	orphans := map[string]int{}
	for chatID, m := range markers {
		for execID, approvals := range m.approvalsByExec {
			if !listed[execID] {
				orphans[chatID] += len(approvals)
			}
		}
	}
	if len(orphans) == 0 {
		return
	}
	chats := make([]string, 0, len(orphans))
	for chatID := range orphans {
		chats = append(chats, chatID)
	}
	sort.Strings(chats)
	fmt.Fprintln(w, "\nPending approvals that match no live run (approvals record no thread):")
	for _, chatID := range chats {
		fmt.Fprintf(w, "  %s  %d pending\n", chatID, orphans[chatID])
	}
}
