// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/workflow"
	"github.com/reliant-labs/reliant/internal/workflow/model"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// TemporalStarter is the one Temporal call a launch makes. Declared here rather
// than taking client.Client so the worker, the api-server and a test fake all
// satisfy it without carrying the whole SDK surface.
type TemporalStarter interface {
	ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (client.WorkflowRun, error)
}

// RunRecorder writes the run ids back onto the chat once Temporal has accepted
// the start. Satisfied by *runs.Service.
type RunRecorder interface {
	RecordRun(ctx context.Context, chatID, workflowID, runID string)
}

// Launcher turns an (Event, Spec) into a running session. See the package doc.
//
// It never talks to a daemon. Anything that needs the user's machine — the
// greenfield probe included — happens inside the run, so a launch costs a
// transaction and a Temporal start whether the machine is up, asleep or gone.
type Launcher struct {
	repo      Store
	temporal  TemporalStarter
	runs      RunRecorder
	threads   ThreadCreator
	taskQueue string
	pinner    ChatPinner
}

// ChatPinner decides which machine a chat that names none runs on, so the
// choice is made once and stored instead of re-made by default resolution on
// every tool call (research/CHAT_MACHINE_PLACEMENT.md). *daemonplacement.Pinner
// implements it.
type ChatPinner interface {
	// Target is the machine chat should be pinned to, or "" to leave it
	// unpinned. It writes nothing.
	Target(ctx context.Context, chat *db.Chat) string
	// Pin stores that choice on the chat row and on chat itself, leaving an
	// existing pin alone. It never fails a launch.
	Pin(ctx context.Context, chat *db.Chat)
}

// LauncherOption configures a Launcher.
type LauncherOption func(*Launcher)

// WithChatPinner makes every run this launcher starts pin an unpinned chat
// first. Without it a launch pins nothing: the chat runs wherever default
// resolution points on each call.
func WithChatPinner(p ChatPinner) LauncherOption {
	return func(l *Launcher) { l.pinner = p }
}

// NewLauncher builds a launcher over the store, the thread creator, Temporal and the task
// queue runs execute on.
func NewLauncher(
	repo Store,
	threadCreator ThreadCreator,
	temporal TemporalStarter,
	runRecorder RunRecorder,
	taskQueue string,
	opts ...LauncherOption,
) *Launcher {
	l := &Launcher{
		repo:      repo,
		temporal:  temporal,
		runs:      runRecorder,
		threads:   threadCreator,
		taskQueue: taskQueue,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// errEventExists aborts the session transaction when another launch recorded
// the same (kind, dedupe key) first, so this attempt's chat rows roll back and
// the caller falls through to finishing the earlier attempt.
var errEventExists = errors.New("trigger event already recorded")

// seedContent is the Spec's messages split the way the start path consumes them.
type seedContent struct {
	userContent    string
	systemMessages []SeedMessage
	hasUserContent bool
}

// Launch materializes the session and starts its root run.
//
// It is the only door a root run starts through, and it is safe to call twice
// for the same event. The trigger_events row is written in the same
// transaction as the chat rows, so the database — not the caller — decides
// whether this (kind, dedupe key) has launched. The Temporal start comes
// after that commit and cannot join it, which is why a launch is resumable:
// when the event and chat are committed but the run never started, calling
// Launch again finishes the start instead of reporting a duplicate.
func (l *Launcher) Launch(ctx context.Context, ev Event, spec Spec) (*Result, error) {
	result, err := l.launch(ctx, ev, spec)
	if errors.Is(err, errAdmissionSuperseded) {
		return nil, &AlreadyLaunchedError{ChatID: spec.NewChatID}
	}
	var already *AlreadyLaunchedError
	if spec.ClientMessageID != "" && errors.As(err, &already) && already.ChatID != "" {
		// A start the client named is idempotent: its repeat is answered with
		// the chat the first call started, not reported as a duplicate.
		if replayed, replayErr := l.replayOfNamedStart(ctx, ev, spec, already); replayed != nil || replayErr != nil {
			return replayed, replayErr
		}
	}
	return result, err
}

// replayOfNamedStart answers a start the client named (Spec.ClientMessageID)
// that has already launched: the same message gets the chat it started, a
// different one under the same id is refused. Nil, nil when the earlier launch
// was not this send (another id, or no record of it), which leaves the caller
// with the duplicate report.
func (l *Launcher) replayOfNamedStart(ctx context.Context, ev Event, spec Spec, already *AlreadyLaunchedError) (*Result, error) {
	if ev.DedupeKey == "" {
		return nil, nil
	}
	stored, err := l.repo.GetTriggerEventByDedupe(ctx, ev.Kind, ev.DedupeKey)
	if err != nil {
		return nil, nil
	}
	if recordedClientMessageID(stored) != spec.ClientMessageID {
		return nil, nil
	}
	if !isRetryOf(stored, ev.Kind, seedFingerprint(spec.Messages, spec.Attachments)) {
		return nil, ErrClientMessageReused
	}
	chat, err := l.repo.GetChat(ctx, already.ChatID)
	if err != nil || chat == nil || chat.UserID != spec.OwnerUserID {
		return nil, nil
	}
	result := &Result{Chat: chat, WorkflowID: chat.MainThreadID(), EventID: stored.ID}
	if chat.RunID != nil {
		result.RunID = *chat.RunID
	}
	return result, nil
}

func (l *Launcher) launch(ctx context.Context, ev Event, spec Spec) (*Result, error) {
	userContent, systemMessages, hasUserContent := splitSeedMessages(spec.Messages)
	seed := seedContent{userContent: userContent, systemMessages: systemMessages, hasUserContent: hasUserContent}

	// Require at least one user message or attachments
	if !hasUserContent && len(spec.Attachments) == 0 {
		return nil, &ValidationError{Reason: "at least one user message or attachment is required"}
	}

	if spec.NoMachine && spec.DaemonID != "" {
		return nil, &ValidationError{Reason: "no_machine and daemon_id are mutually exclusive: a run either has a machine or it does not"}
	}

	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	// An event kind with nobody behind it launches an unattended run whatever
	// the caller set. Unattended decides more than whether the run may ask a
	// question: an unattended run is withheld the tools that change workflows,
	// start standing work or act through an integration
	// (tools.UnattendedWithholding), so a launch path that forgot to set it
	// must fail closed rather than hand a trigger's payload an attended run.
	if ev.Kind.Unattended() {
		spec.Unattended = true
	}
	spec.Params = paramsWithMode(spec.Params, spec.Mode)

	if spec.ChatID != "" {
		return l.launchPending(ctx, ev, spec, seed)
	}
	return l.launchNew(ctx, ev, spec, seed)
}

// paramsWithMode folds Spec.Mode into the workflow params, where the runtime
// reads it. The caller's map is not mutated.
func paramsWithMode(params map[string]*structpb.Value, mode *string) map[string]*structpb.Value {
	if mode == nil || *mode == "" {
		return params
	}
	merged := make(map[string]*structpb.Value, len(params)+1)
	for key, value := range params {
		merged[key] = value
	}
	merged["mode"] = structpb.NewStringValue(*mode)
	return merged
}

// launchNew creates a chat and starts it.
func (l *Launcher) launchNew(ctx context.Context, ev Event, spec Spec, seed seedContent) (*Result, error) {
	if spec.ProjectID == "" {
		return nil, &ValidationError{Reason: "project_id is required"}
	}

	userID := spec.OwnerUserID

	// Create chat ID - root workflow ID equals chat ID for simple identification
	// workflow_name is stored separately in the chat record for querying/debugging
	//
	// An idempotent source (a scheduled fire) derives NewChatID from its fire id
	// so a retry cannot create a second chat.
	chatID := spec.NewChatID
	if chatID == "" {
		chatID = uuid.New().String()
	}
	workflowID := chatID // Root workflow ID = chat ID
	if ev.DedupeKey == "" && isAttendedStartKind(ev.Kind) {
		ev.DedupeKey = chatID
	}

	// A retry of an earlier launch: finish it rather than create a second chat.
	//
	// The one exception is an inbound event a receiver recorded as PENDING:
	// that row is not an earlier launch but this launch's own record of
	// intent, written on the api-server before the worker got here. It is
	// adopted (claimed inside the launch transaction) rather than inserted.
	var pending *core.TriggerEvent
	if ev.DedupeKey != "" {
		if existing, err := l.repo.GetTriggerEventByDedupe(ctx, ev.Kind, ev.DedupeKey); err == nil {
			if existing.Outcome != core.TriggerEventPending {
				return l.finishExisting(ctx, ev, spec, seed)
			}
			pending = existing
		} else if !errors.Is(err, core.ErrTriggerEventNotFound) {
			return nil, &InternalError{Reason: "failed to check for an earlier launch", Err: err}
		}
	}
	if spec.NewChatID != "" {
		if _, err := l.repo.GetChat(ctx, chatID); err == nil {
			return nil, &AlreadyLaunchedError{ChatID: chatID}
		} else if !errors.Is(err, core.ErrChatNotFound) {
			return nil, &InternalError{Reason: "failed to check for an existing chat", Err: err}
		}
	}

	// Verify user owns the project and get project details
	project, err := l.repo.GetProjectWithUserCheck(ctx, spec.ProjectID, userID)
	if err != nil {
		return nil, projectLookupError(err)
	}

	// Resolve workflow - use user's default if not specified
	workflowName := l.ResolveDefaultWorkflow(ctx, userID, spec.Workflow)
	now := time.Now().UTC()

	// Prepare title
	title := ""
	if spec.Title != nil {
		title = *spec.Title
	}

	// Create chat object (not yet persisted) - model/temperature/max_tokens are workflow input params, not stored on chat
	chat := &db.Chat{
		ID:           chatID,
		UserID:       userID,
		Title:        title,
		ProjectID:    spec.ProjectID,
		WorkflowName: &workflowName,
		State:        db.ChatStateIdle,
		// Who started it decides whether it is a conversation (the chat
		// list) or a run (Runs), for the life of the chat.
		Origin:          core.ChatOriginOf(ev.Kind),
		WorkflowID:      &workflowID,
		SelectedPresets: spec.Presets,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActive:      now,
	}

	// The chat's launch event, as the chat reads it back once stored: its
	// kind decides the precedence the inputs are built and validated under
	// (AutomationPrecedence), the same rule tree admission (admissionScope)
	// and the run's own loads use.
	chat.LaunchKind = string(ev.Kind)
	if ev.TriggerID != "" {
		triggerID := ev.TriggerID
		chat.TriggerID = &triggerID
	}

	chat.NoMachine = spec.NoMachine

	// Every chat must name a resolvable worktree, or the UI's worktree-grouped
	// chat list silently hides it. Callers that omit one (e.g. the CLI) bind to
	// the project's main worktree. Its error is reported after the request
	// validators below, so a malformed request still reports its own error
	// rather than a project-state precondition; it is resolved first because
	// it decides the machine the chat is pinned to.
	worktreeID, worktreeErr := l.ResolveChatWorktreeID(ctx, spec.ProjectID, spec.WorktreeID)
	if worktreeErr == nil {
		chat.WorktreeID = worktreeID
		if spec.DaemonID != "" {
			chat.ActiveDaemonID = &spec.DaemonID
		} else if l.pinner != nil && !chat.NoMachine {
			// No machine chosen: the chat still lands on exactly one, decided now
			// and stored with the row, not left to default resolution on every
			// tool call (which moves it when another machine connects).
			if target := l.pinner.Target(ctx, chat); target != "" {
				chat.ActiveDaemonID = &target
			}
		}
	}

	// Validate workflow tree BEFORE creating chat to avoid runtime graph failures and orphaned chats.
	// Uses runtime-equivalent loader semantics: builtin:// and usable workflow drafts only,
	// read from the catalog of the machine the chat is pinned to, which is what the run's
	// own loads read.
	if err := l.validateWorkflowTree(ctx, userID, workflowName, project.ID, RunMachine(chat), ev); err != nil {
		return nil, err
	}
	if spec.NoMachine {
		if err := l.validateNoMachine(ctx, admissionScope(userID, project.ID, "", workflowName, ev), workflowName); err != nil {
			return nil, err
		}
	}

	if err := ValidateWorkflowParamStructure(spec.Params); err != nil {
		return nil, &ValidationError{Reason: err.Error()}
	}
	if worktreeErr != nil {
		return nil, worktreeErr
	}
	// A branch worktree's checkout lives on one machine (worktree.DaemonID
	// routes its tools there), so a chat with no machine cannot run in one,
	// and connecting it to a different machine later would split the two.
	// The main checkout carries no daemon and is where every no-machine chat
	// binds (research/NO_MACHINE_CHATS.md).
	if spec.NoMachine && spec.WorktreeID != nil && *spec.WorktreeID != "" {
		if wt, wtErr := l.repo.GetWorktree(ctx, *worktreeID); wtErr == nil && wt != nil && !wt.IsMain && wt.DaemonID != nil && *wt.DaemonID != "" {
			return nil, &ValidationError{Reason: "this worktree's checkout lives on a machine, so a chat with no machine cannot run in it: omit worktree_id"}
		}
	}

	// Build and validate workflow inputs BEFORE creating chat
	initialData, err := l.buildInputs(ctx, userID, chat, workflowName, spec.Presets, spec.Params)
	if err != nil {
		return nil, err
	}

	// Root workflow + thread, created atomically with the chat below.
	//
	// OwnerUserID is recorded on the run itself rather than left to be read off
	// the chat later. Nothing reads it yet — see the migration — but writing it
	// from the start is what lets the read sites flip without a second backfill.
	rootWorkflow := &db.Workflow{
		ID:           workflowID,
		ChatID:       chatID,
		WorkflowName: workflowName,
		Thread:       workflowID, // Root workflow: thread = workflow ID
		Status:       db.Pending(),
		CreatedAt:    now,
		OwnerUserID:  &userID,
	}

	// chat_created payload for the global websocket, computed from data we
	// already have (no DB round trip) so it can be emitted inside the same
	// transaction as the row it announces.
	chatCreatedData := map[string]interface{}{
		"chat_id":     chatID,
		"title":       chat.Title,
		"project_id":  chat.ProjectID,
		"worktree_id": chat.WorktreeID,
		"workflow":    chat.WorkflowName,
		"state":       string(chat.State),
		"created_at":  chat.CreatedAt.Format(time.RFC3339),
	}
	chatCreatedJSON, marshalErr := json.Marshal(chatCreatedData)
	if marshalErr != nil {
		logging.ErrorContext(ctx, "Failed to marshal chat_created data", "error", marshalErr, "chatID", chatID)
	}

	eventRow := newEventRow(ev, userID, chatID, workflowName, worktreeID, startRecord{
		Fingerprint:     seedFingerprint(spec.Messages, spec.Attachments),
		ClientMessageID: spec.ClientMessageID,
		Workflow:        workflowName,
		Presets:         spec.Presets,
		Params:          spec.Params,
		Prompt:          seed.userContent,
	})
	if pending != nil {
		// The adopted row keeps its identity, the time the source says the
		// event happened and the sender intake recorded; only its payload
		// gains the start record.
		eventRow.ID = pending.ID
		eventRow.OccurredAt = pending.OccurredAt
		eventRow.Sender = pending.Sender
		eventRow.Origin = pending.Origin
	}

	// The event row is inserted before the chat, so the (kind, dedupe_key)
	// constraint — not the chat's primary key — decides which of two
	// concurrent launches of the same event wins. It goes in without its
	// chat_id because trigger_events.chat_id references chats; the id is
	// attached once the chat exists.
	insertRow := *eventRow
	insertRow.ChatID = nil

	// The chat row, its root workflow+thread, the initial messages, the
	// chat_created announcement and the trigger event must not be observed
	// apart: a client that sees chat_created must be able to load a chat that
	// already has a thread, and a chat without its event row could be started
	// twice. Group them in one transaction so any failure leaves nothing
	// behind (no orphan chat, no thread-less chat, no announcement for a
	// chat that doesn't exist).
	//
	// READ COMMITTED, because what must hold here is atomicity, and nothing in
	// a launch is a read-check-write that only SERIALIZABLE would keep
	// correct:
	//   - two launches of one event: the (kind, dedupe_key) unique constraint
	//     on the insert, and the outcome predicate on the claim's UPDATE,
	//     which READ COMMITTED re-checks against the row it waited for;
	//   - the selection merge: its advisory lock (MutateChatSelection);
	//   - the chat_created sequence number: the user's counter row, raised
	//     by one UPDATE … RETURNING;
	//   - the thread's ordinals, seqs and context window: the chat and its
	//     thread are new and invisible to every other transaction until this
	//     one commits, so nothing else can number them;
	//   - a trigger's overlap guard (Spec.Guard): it row-locks the trigger
	//     first, so a fire queued behind another reads only after that one
	//     committed, and READ COMMITTED's next statement sees its rows. What
	//     it reads there is unambiguous: a predecessor's root is PENDING with
	//     no failed start while its start is in flight (blocks), and the one
	//     write it makes, abandoning a predecessor whose start failed, is a
	//     single conditional UPDATE that a concurrent retry of that start
	//     (ClaimWorkflowStart) serializes with on the row lock.
	// SERIALIZABLE bought none of that and cost every launch its predicate
	// locks: on the young index pages of chats, workflows, threads,
	// context_windows, messages and chat_selections (foreign-key checks and
	// ON CONFLICT probes included), so concurrent launches for DIFFERENT
	// users aborted one another with SQLSTATE 40001 at whichever insert
	// closed the cycle — "failed to create workflow" when it was the root
	// workflow's — until one ran out of attempts
	// (TestConcurrentStartChatsTakeNoPredicateLocks).
	launchTx := orm.ReadCommitted.WithMaxAttempts(orm.DefaultTxMaxAttempts)
	// attemptAt precedes this attempt's durable stamp (PrepareExecution, in
	// the transaction below), so the start's deadline is measured from no
	// later than the stamp capacity recovery ages.
	var attemptAt time.Time
	if err := l.repo.RunTx(ctx, launchTx, func(txCtx context.Context) error {
		attemptAt = time.Now()
		if spec.Guard != nil {
			reason, err := spec.Guard(txCtx)
			if err != nil {
				return fmt.Errorf("launch guard: %w", err)
			}
			if reason != "" {
				return &DeclinedError{Reason: reason}
			}
		}

		if pending != nil {
			// The outcome predicate on the claim is what decides between two
			// fires of one inbound event, as the unique key does for inserts.
			claimed, err := l.repo.ClaimPendingTriggerEvent(txCtx, pending.ID, insertRow.Payload)
			if err != nil {
				return fmt.Errorf("failed to claim trigger event: %w", err)
			}
			if !claimed {
				return errEventExists
			}
		} else {
			created, err := l.repo.CreateTriggerEvent(txCtx, &insertRow)
			if err != nil {
				return fmt.Errorf("failed to record trigger event: %w", err)
			}
			if !created {
				return errEventExists
			}
		}

		if err := l.repo.CreateChat(txCtx, chat); err != nil {
			return fmt.Errorf("failed to create chat: %w", err)
		}
		if err := l.recordStartSelection(txCtx, userID, chat, workflowName, spec.Params); err != nil {
			return err
		}

		if err := l.repo.UpdateTriggerEventOutcome(txCtx, eventRow.ID, core.TriggerEventLaunched, "", &chatID); err != nil {
			return fmt.Errorf("failed to attach chat to trigger event: %w", err)
		}
		if spec.PrepareExecution != nil {
			if err := spec.PrepareExecution(txCtx, eventRow.ID, workflowID); err != nil {
				return err
			}
		}

		if _, _, _, err := l.threads.CreateWorkflowWithThread(txCtx, threads.CreateWorkflowWithThreadOpts{
			Workflow: rootWorkflow,
			ThreadID: workflowID,
			ChatID:   chatID,
		}); err != nil {
			return fmt.Errorf("failed to create workflow and thread: %w", err)
		}

		if err := l.saveSeedMessages(txCtx, chatID, workflowID, seed.systemMessages, seed, spec.Attachments, spec.ClientMessageID); err != nil {
			return err
		}

		if chatCreatedJSON != nil {
			if err := l.repo.CreateUserUpdate(txCtx, &db.UserUpdate{
				UserID:     userID,
				ProjectID:  &chat.ProjectID,
				WorktreeID: chat.WorktreeID,
				ChatID:     &chatID,
				UpdateType: db.UserUpdateChatCreated,
				EntityType: db.EntityTypeChat,
				EntityID:   chatID,
				Data:       chatCreatedJSON,
			}); err != nil {
				return fmt.Errorf("failed to create chat_created user update: %w", err)
			}
		}
		return nil
	}); err != nil {
		var declined *DeclinedError
		if errors.As(err, &declined) {
			return nil, declined
		}
		if errors.Is(err, errEventExists) {
			// Lost a race with a concurrent launch of the same event.
			return l.finishExisting(ctx, ev, spec, seed)
		}
		if errors.Is(err, ErrClientMessageReused) {
			return nil, ErrClientMessageReused
		}
		logging.ErrorContext(ctx, "Failed to create chat", "error", err, "chatID", chatID)
		return nil, &InternalError{Reason: "failed to create chat", Err: err}
	}

	return l.start(ctx, startParams{
		chat:          chat,
		workflowName:  workflowName,
		spec:          spec,
		initialData:   initialData,
		userContent:   seed.userContent,
		eventID:       eventRow.ID,
		trigger:       TriggerInfoFromEvent(eventRow),
		generateTitle: spec.GenerateTitle,
		attemptAt:     attemptAt,
	})
}

// launchPending starts an existing PENDING chat: a branched chat's first send,
// or the retry of a start that committed but never reached Temporal.
//
// Whether a call is a retry or a new turn is decided from durable state: the
// event row for (kind, dedupe key) records a fingerprint of the seed it was
// started with, plus the effective workflow, presets and params.
//
//   - No event yet: the first start. Apply the workflow switch, presets and
//     seed messages and record the event, in one transaction.
//   - Event with the same fingerprint (or any non-interactive source, whose
//     retries are by definition the same fire): a true retry. Nothing is
//     written; the run starts from the PERSISTED workflow, presets and params,
//     never from this call's Spec, which may have drifted (an edited trigger).
//   - Event with a different fingerprint on an interactive start: the caller
//     is sending something new into a chat that has not started. Nothing has
//     run, so the switch, presets and messages apply to the pending chat in
//     one transaction (appending after the earlier seed, which stays), the
//     event's record is updated, and the run starts from the persisted
//     result. The alternative — start the original, then deliver the new
//     message as a continuation — needs SendMessage's machinery, which this
//     package cannot call, and would run the original workflow only to
//     interrupt it.
//
// The pending check is repeated inside the transaction, so a start that lands
// between the check and the write reports ErrNotPending rather than saving a
// message onto a run that has already begun.
func (l *Launcher) launchPending(ctx context.Context, ev Event, spec Spec, seed seedContent) (*Result, error) {
	userID := spec.OwnerUserID

	chat, err := l.repo.GetChat(ctx, spec.ChatID)
	if err != nil && !errors.Is(err, core.ErrChatNotFound) {
		return nil, &InternalError{Reason: "failed to load chat", Err: err}
	}
	if err != nil || chat == nil || chat.UserID != userID {
		return nil, &NotFoundError{Reason: "chat not found", Err: err}
	}

	// The chat already decided where it runs; a mismatch means the caller is
	// talking about a different chat than it thinks.
	if spec.ProjectID != "" && spec.ProjectID != chat.ProjectID {
		return nil, &ValidationError{Reason: "project_id does not match the chat's project"}
	}
	if spec.WorktreeID != nil && *spec.WorktreeID != "" && (chat.WorktreeID == nil || *chat.WorktreeID != *spec.WorktreeID) {
		return nil, &ValidationError{Reason: "worktree_id does not match the chat's worktree"}
	}

	workflowID := chat.MainThreadID()
	if workflowID == "" {
		return nil, &ValidationError{Kind: ValidationFailedPrecondition, Reason: "chat has no root workflow"}
	}
	root, err := l.repo.GetWorkflow(ctx, workflowID)
	if err != nil || root == nil {
		return nil, &InternalError{Reason: "failed to check workflow status", Err: err}
	}
	pending := root.Status == db.Pending()

	if ev.DedupeKey == "" && isAttendedStartKind(ev.Kind) {
		ev.DedupeKey = chat.ID
	}
	fingerprint := seedFingerprint(spec.Messages, spec.Attachments)

	// The event row decides what this call is, not the chat's state. A call
	// that repeats an event already recorded is a duplicate of it: finish it
	// while the chat is still pending, and report "already launched" once the
	// run has started — never "use SendMessage", which is only the answer to a
	// NEW event aimed at a chat that has already begun.
	existing, err := l.repo.GetTriggerEventByDedupe(ctx, ev.Kind, ev.DedupeKey)
	switch {
	case err == nil:
		if isRetryOf(existing, ev.Kind, fingerprint) {
			if !pending {
				return nil, &AlreadyLaunchedError{ChatID: chat.ID, EventID: existing.ID}
			}
			return l.startRecorded(ctx, existing, spec, seed)
		}
		if spec.ClientMessageID != "" && recordedClientMessageID(existing) == spec.ClientMessageID {
			// The id names the message already written; a second message under
			// it would be a second row with the same primary key.
			return nil, ErrClientMessageReused
		}
	case !errors.Is(err, core.ErrTriggerEventNotFound):
		return nil, &InternalError{Reason: "failed to check for an earlier launch", Err: err}
	}
	if !pending {
		return nil, ErrNotPending
	}
	// A pending chat is a branch. A machine branch's worktree is a checkout on
	// one machine, so it cannot start without that machine. A branch made with
	// no machine (BranchChatRequest.no_machine) recorded that on its row and
	// starts like any no-machine chat; naming a daemon at its first send pins
	// one below, which ends no-machine in the same write.
	if spec.NoMachine && !chat.NoMachine {
		return nil, &ValidationError{Reason: "a branched chat runs on its worktree's machine, so it cannot start with no machine"}
	}
	noMachine := chat.NoMachine && spec.DaemonID == ""

	project, err := l.repo.GetProjectWithUserCheck(ctx, chat.ProjectID, userID)
	if err != nil {
		return nil, projectLookupError(err)
	}

	// Validate the new request BEFORE writing anything, against the workflow
	// it would run. A pending chat has produced nothing, so its workflow can
	// still change; this is the one place the system allows it.
	workflowName, _, _ := effectiveStart(chat, spec)
	// The chat as this start leaves it: a daemon named at the first send pins
	// it and ends no-machine. Every admission read below is scoped by it, so
	// the tree, the presets and the input schema all come from that machine.
	checked := *chat
	// A pending chat has no launch event yet: this start is it.
	if checked.LaunchKind == "" {
		checked.LaunchKind = string(ev.Kind)
	}
	if spec.DaemonID != "" {
		checked.ActiveDaemonID = &spec.DaemonID
		checked.NoMachine = false
	} else if RunMachine(&checked) == "" && !checked.NoMachine && l.pinner != nil {
		// An unpinned pending chat is pinned when it starts (start); admit it
		// against the machine that pin will choose, not the union.
		if target := l.pinner.Target(ctx, &checked); target != "" {
			checked.ActiveDaemonID = &target
		}
	}
	if err := l.validateWorkflowTree(ctx, userID, workflowName, project.ID, RunMachine(&checked), ev); err != nil {
		return nil, err
	}
	if noMachine {
		if err := l.validateNoMachine(ctx, admissionScope(userID, project.ID, "", workflowName, ev), workflowName); err != nil {
			return nil, err
		}
	}
	if err := ValidateWorkflowParamStructure(spec.Params); err != nil {
		return nil, &ValidationError{Reason: err.Error()}
	}
	{
		_, presets, _ := effectiveStart(chat, spec)
		if _, err := l.buildInputs(ctx, userID, &checked, workflowName, presets, spec.Params); err != nil {
			return nil, err
		}
	}

	if err := l.repo.RunTx(ctx, orm.Serializable, func(txCtx context.Context) error {
		cur, err := l.repo.GetChat(txCtx, chat.ID)
		if err != nil {
			return fmt.Errorf("failed to reload chat: %w", err)
		}
		curRoot, err := l.repo.GetWorkflow(txCtx, workflowID)
		if err != nil || curRoot == nil {
			return fmt.Errorf("failed to reload workflow: %w", err)
		}
		if curRoot.Status != db.Pending() {
			return ErrNotPending
		}

		workflowName, presets, switching := effectiveStart(cur, spec)
		eventRow := newEventRow(ev, userID, cur.ID, workflowName, cur.WorktreeID, startRecord{
			Fingerprint:     fingerprint,
			ClientMessageID: spec.ClientMessageID,
			Workflow:        workflowName,
			Presets:         presets,
			Params:          spec.Params,
			Prompt:          seed.userContent,
		})

		created, err := l.repo.CreateTriggerEvent(txCtx, eventRow)
		if err != nil {
			return fmt.Errorf("failed to record trigger event: %w", err)
		}
		if !created {
			stored, err := l.repo.GetTriggerEventByDedupe(txCtx, ev.Kind, ev.DedupeKey)
			if err != nil {
				return fmt.Errorf("failed to load earlier trigger event: %w", err)
			}
			if isRetryOf(stored, ev.Kind, fingerprint) {
				// A concurrent identical start committed first; its messages
				// are already on the thread.
				return nil
			}
			if err := l.repo.UpdateTriggerEventPayload(txCtx, stored.ID, eventRow.Payload); err != nil {
				return fmt.Errorf("failed to record the new start: %w", err)
			}
		}

		if switching {
			if err := l.repo.UpdateWorkflowName(txCtx, workflowID, workflowName); err != nil {
				return fmt.Errorf("failed to update workflow name: %w", err)
			}
		}
		if spec.DaemonID != "" && (cur.ActiveDaemonID == nil || *cur.ActiveDaemonID != spec.DaemonID) {
			daemonID := spec.DaemonID
			if err := l.repo.UpdateChatActiveDaemon(txCtx, cur.ID, &daemonID); err != nil {
				return fmt.Errorf("failed to pin chat to daemon: %w", err)
			}
		}
		if switching || len(spec.Presets) > 0 {
			cur.WorkflowName = &workflowName
			cur.SelectedPresets = presets
			cur.UpdatedAt = time.Now().UTC()
			if err := l.repo.UpdateChat(txCtx, cur); err != nil {
				return fmt.Errorf("failed to update chat: %w", err)
			}
		}

		if err := l.recordStartSelection(txCtx, userID, cur, workflowName, spec.Params); err != nil {
			return err
		}

		// The branch's thread was forked by BranchChat; the seed messages
		// append to it. Whether the run probes for greenfield is the
		// caller's Spec.GreenfieldProbe, which a branch's first send leaves
		// off: a branch is not a first turn of new work.
		return l.saveSeedMessages(txCtx, cur.ID, workflowID, seed.systemMessages, seed, spec.Attachments, spec.ClientMessageID)
	}); err != nil {
		if errors.Is(err, ErrNotPending) {
			// The chat started between the check above and the transaction.
			// If that start was a duplicate of this very event, say so.
			if stored, lookupErr := l.repo.GetTriggerEventByDedupe(ctx, ev.Kind, ev.DedupeKey); lookupErr == nil && isRetryOf(stored, ev.Kind, fingerprint) {
				return nil, &AlreadyLaunchedError{ChatID: chat.ID, EventID: stored.ID}
			}
			return nil, ErrNotPending
		}
		if errors.Is(err, ErrClientMessageReused) {
			return nil, ErrClientMessageReused
		}
		logging.ErrorContext(ctx, "Failed to start pending chat", "error", err, "chatID", chat.ID)
		return nil, &InternalError{Reason: "failed to start chat", Err: err}
	}

	recorded, err := l.repo.GetTriggerEventByDedupe(ctx, ev.Kind, ev.DedupeKey)
	if err != nil {
		return nil, &InternalError{Reason: "failed to reload the recorded start", Err: err}
	}
	return l.startRecorded(ctx, recorded, spec, seed)
}

// effectiveStart is what a start of chat with spec would run as: the workflow
// (the spec's, when it names one, else the chat's), the presets (the chat's as
// the base, the spec's overriding) and whether that is a workflow switch.
func effectiveStart(chat *db.Chat, spec Spec) (workflowName string, presets map[string]string, switching bool) {
	if chat.WorkflowName != nil {
		workflowName = *chat.WorkflowName
	}
	if spec.Workflow != "" && spec.Workflow != workflowName {
		workflowName = spec.Workflow
		switching = true
	}
	presets = make(map[string]string)
	for key, value := range chat.SelectedPresets {
		if value != "" {
			presets[key] = value
		}
	}
	for key, value := range spec.Presets {
		if value != "" {
			presets[key] = value
		}
	}
	return workflowName, presets, switching
}

// isAttendedStartKind reports whether a launch kind is a human starting a chat
// by hand: the interactive start, a test run pressed in the workflow builder,
// "Run now" on a workflow, and `reliant workflow run`. All go through
// ChatService.StartChat's path and dedupe on the chat id, so a second call with
// different content is a second thing the user said rather than a retry.
func isAttendedStartKind(kind core.TriggerEventKind) bool {
	switch kind {
	case core.TriggerEventKindChatStart, core.TriggerEventKindBuilderTest,
		core.TriggerEventKindManualRun, core.TriggerEventKindCLIRun:
		return true
	}
	return false
}

// isRetryOf reports whether a call carrying fingerprint is a retry of the
// start the event recorded. Only an interactive start can be a new turn: its
// dedupe key is the chat id, so a second call with different content is a
// second thing the user said. Any other source dedupes on its own identity (a
// fire id), so a repeat is the same fire even if its definition has since
// been edited.
func isRetryOf(stored *core.TriggerEvent, kind core.TriggerEventKind, fingerprint string) bool {
	if !isAttendedStartKind(kind) {
		return true
	}
	recorded, _ := stored.Payload[payloadSeedFingerprint].(string)
	return recorded == fingerprint
}

// startRecorded starts the chat from the state the database holds: the chat
// row's workflow and presets, and the params recorded on the event. The call's
// own Spec contributes only what is never persisted (unattended, the JWT, the
// title flag).
func (l *Launcher) startRecorded(ctx context.Context, recorded *core.TriggerEvent, spec Spec, seed seedContent) (*Result, error) {
	if recorded.ChatID == nil {
		return nil, &AlreadyLaunchedError{EventID: recorded.ID}
	}
	chat, err := l.repo.GetChat(ctx, *recorded.ChatID)
	if err != nil && !errors.Is(err, core.ErrChatNotFound) {
		return nil, &InternalError{Reason: "failed to load chat", Err: err}
	}
	if err != nil || chat == nil || chat.UserID != spec.OwnerUserID {
		return nil, &NotFoundError{Reason: "chat not found", Err: err}
	}

	workflowName := ""
	if chat.WorkflowName != nil {
		workflowName = *chat.WorkflowName
	}
	params, err := recordedParams(recorded)
	if err != nil {
		return nil, &InternalError{Reason: "failed to read the recorded start", Err: err}
	}

	// Pin an unpinned chat before its inputs are built: they are read from
	// its machine's catalog, the one the run's own loads resolve against.
	if l.pinner != nil {
		l.pinner.Pin(ctx, chat)
	}
	initialData, err := l.buildInputs(ctx, spec.OwnerUserID, chat, workflowName, chat.SelectedPresets, params)
	if err != nil {
		return nil, err
	}

	// This may be the retry of a start that failed, so the root may carry a
	// failed-start stamp, which a trigger's next fire reads as "stranded" and
	// abandons. Clearing it is how this start says it is in flight again, and
	// it is conditional on the root still being PENDING: if a later fire
	// abandoned it first, this fire's slot is spent and must not start a run
	// beside the one that replaced it.
	// attemptAt precedes the claim's durable start-attempt stamp; see start.
	attemptAt := time.Now()
	stillPending, err := l.repo.ClaimWorkflowStart(ctx, chat.ID)
	if err != nil {
		return nil, &InternalError{Reason: "failed to claim the start", Err: err}
	}
	if !stillPending {
		if err := l.recoverAdmission(ctx, recorded.ID, chat.MainThreadID(), spec); err != nil {
			return nil, &InternalError{Reason: "failed to recover admitted execution", Err: err}
		}
		return nil, &AlreadyLaunchedError{ChatID: chat.ID, EventID: recorded.ID}
	}

	return l.start(ctx, startParams{
		chat:          chat,
		workflowName:  workflowName,
		spec:          spec,
		initialData:   initialData,
		userContent:   seed.userContent,
		eventID:       recorded.ID,
		trigger:       TriggerInfoFromEvent(recorded),
		generateTitle: spec.GenerateTitle && chat.Title == "",
		attemptAt:     attemptAt,
	})
}

// finishExisting handles a Launch whose (kind, dedupe key) already has an
// event. When that launch's chat is still pending, its attempt committed but
// never started Temporal, so this finishes it; otherwise it already launched.
func (l *Launcher) finishExisting(ctx context.Context, ev Event, spec Spec, seed seedContent) (*Result, error) {
	existing, err := l.repo.GetTriggerEventByDedupe(ctx, ev.Kind, ev.DedupeKey)
	if err != nil {
		return nil, &InternalError{Reason: "failed to load earlier launch", Err: err}
	}
	if existing.ChatID == nil {
		return nil, &AlreadyLaunchedError{EventID: existing.ID}
	}

	chat, err := l.repo.GetChat(ctx, *existing.ChatID)
	if err != nil && !errors.Is(err, core.ErrChatNotFound) {
		return nil, &InternalError{Reason: "failed to load chat", Err: err}
	}
	if err != nil || chat == nil || chat.UserID != spec.OwnerUserID {
		return nil, &NotFoundError{Reason: "chat not found", Err: err}
	}

	if workflowID := chat.MainThreadID(); workflowID != "" {
		if root, err := l.repo.GetWorkflow(ctx, workflowID); err == nil && root != nil && root.Status == db.Pending() {
			spec.ChatID = chat.ID
			spec.ProjectID = chat.ProjectID
			spec.WorktreeID = nil
			return l.launchPending(ctx, ev, spec, seed)
		}
	}
	if err := l.recoverAdmission(ctx, existing.ID, chat.MainThreadID(), spec); err != nil {
		return nil, &InternalError{Reason: "failed to recover admitted execution", Err: err}
	}
	return nil, &AlreadyLaunchedError{ChatID: chat.ID, EventID: existing.ID}
}

const (
	payloadSeedFingerprint = "seed_fingerprint"
	payloadStart           = "start"
	payloadClientMessageID = "client_message_id"
)

// recordedClientMessageID is the client message id a start was recorded with.
func recordedClientMessageID(row *core.TriggerEvent) string {
	id, _ := row.Payload[payloadClientMessageID].(string)
	return id
}

// startRecord is what the event row remembers about a start, so a retry can be
// told from a new turn and can rebuild the run without trusting the retrying
// caller's Spec.
type startRecord struct {
	Fingerprint string
	Workflow    string
	Presets     map[string]string
	Params      map[string]*structpb.Value
	// ClientMessageID is the id the client gave the first message, when it did.
	ClientMessageID string
	// Prompt is the seed user text, so a re-run starts from exactly what this
	// run did instead of guessing it back out of the transcript.
	Prompt string
}

// maxRecordedPrompt bounds the prompt copied into the event payload. A longer
// prompt is left out rather than truncated: a clipped prompt would re-run as a
// different run, and the transcript still holds the full text for a reader
// that falls back to it.
const maxRecordedPrompt = 32 * 1024

func (r startRecord) addTo(payload map[string]any) {
	params := make(map[string]any, len(r.Params))
	for key, value := range r.Params {
		params[key] = value.AsInterface()
	}
	presets := make(map[string]any, len(r.Presets))
	for key, value := range r.Presets {
		presets[key] = value
	}
	payload[payloadSeedFingerprint] = r.Fingerprint
	if r.ClientMessageID != "" {
		payload[payloadClientMessageID] = r.ClientMessageID
	}
	start := map[string]any{
		"workflow": r.Workflow,
		"presets":  presets,
		"params":   params,
	}
	if r.Prompt != "" && len(r.Prompt) <= maxRecordedPrompt {
		start["prompt"] = r.Prompt
	}
	payload[payloadStart] = start
}

// recordedParams reads back the workflow params a start was recorded with.
func recordedParams(row *core.TriggerEvent) (map[string]*structpb.Value, error) {
	start, _ := row.Payload[payloadStart].(map[string]any)
	raw, _ := start["params"].(map[string]any)
	params := make(map[string]*structpb.Value, len(raw))
	for key, value := range raw {
		converted, err := structpb.NewValue(value)
		if err != nil {
			return nil, fmt.Errorf("param %q: %w", key, err)
		}
		params[key] = converted
	}
	return params, nil
}

// seedFingerprint identifies what a start was asked to say: its messages, in
// order, and its attachments.
func seedFingerprint(messages []SeedMessage, attachments []string) string {
	hash := sha256.New()
	for _, message := range messages {
		fmt.Fprintf(hash, "m\x00%d\x00%s\x00", message.Role, message.Content)
	}
	for _, attachment := range attachments {
		fmt.Fprintf(hash, "a\x00%s\x00", attachment)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// projectLookupError separates a project that is not there (final) from a
// store failure (worth retrying).
func projectLookupError(err error) error {
	if errors.Is(err, core.ErrProjectNotFound) {
		return &NotFoundError{Reason: "project not found", Err: err}
	}
	return &InternalError{Reason: "failed to load project", Err: err}
}

// newEventRow builds the trigger_events row a launch records. For an ad hoc
// chat start it fills in the resolved workflow and worktree, which the
// handler cannot know when the request left them to defaults.
func newEventRow(ev Event, userID, chatID, workflowName string, worktreeID *string, record startRecord) *core.TriggerEvent {
	payload := make(map[string]any, len(ev.Payload)+4)
	for key, value := range ev.Payload {
		payload[key] = value
	}
	record.addTo(payload)
	if isAttendedStartKind(ev.Kind) {
		payload["workflow"] = workflowName
		if worktreeID != nil {
			payload["worktree_id"] = *worktreeID
		}
	}

	row := &core.TriggerEvent{
		ID:         uuid.NewString(),
		UserID:     userID,
		Kind:       ev.Kind,
		DedupeKey:  ev.DedupeKey,
		OccurredAt: ev.OccurredAt,
		Payload:    payload,
		Sender:     ev.Sender,
		Origin:     ev.Origin,
		Outcome:    core.TriggerEventLaunched,
		ChatID:     &chatID,
	}
	if ev.TriggerID != "" {
		triggerID := ev.TriggerID
		row.TriggerID = &triggerID
	}
	return row
}

// buildInputs resolves and validates the root run's inputs. Callers run it
// BEFORE anything is persisted, so a spec that cannot launch leaves no rows.
func (l *Launcher) buildInputs(
	ctx context.Context,
	userID string,
	chat *db.Chat,
	workflowName string,
	presets map[string]string,
	params map[string]*structpb.Value,
) (map[string]interface{}, error) {
	initialData, err := l.BuildChatWorkflowInputs(ctx, userID, chat, workflowName, presets, params)
	if err != nil {
		return nil, err
	}

	// Validate resolved inputs (catches empty model after defaults resolution)
	if validationErrors := l.ValidateChatWorkflowInputs(ctx, userID, chat, workflowName, initialData); len(validationErrors) > 0 {
		errMsgs := make([]string, len(validationErrors))
		for i, e := range validationErrors {
			if errors.Is(e, drivers.ErrDriverLookupFailed) {
				// Could not read the user's provider settings: a store
				// problem, not a verdict on the inputs.
				return nil, &InternalError{Reason: "failed to read provider settings", Err: e}
			}
			errMsgs[i] = e.Error()
		}
		return nil, &ValidationError{
			Reason: fmt.Sprintf("workflow input validation failed: %s", strings.Join(errMsgs, "; ")),
		}
	}
	return initialData, nil
}

// saveSeedMessages writes the seed messages to the root thread. System
// messages go first, then the user message, so the model reads them in order.
func (l *Launcher) saveSeedMessages(
	ctx context.Context,
	chatID, threadID string,
	systemMessages []SeedMessage,
	seed seedContent,
	attachments []string,
	userMessageID string,
) error {
	for _, sysMsg := range systemMessages {
		if _, err := l.repo.SaveMessageToThread(ctx, chatID, threadID, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sysMsg.Content, &threadID, nil, displayStyleProtoToInt32Ptr(sysMsg.DisplayStyle)); err != nil {
			return fmt.Errorf("failed to save system message: %w", err)
		}
	}
	if seed.hasUserContent || len(attachments) > 0 {
		if userMessageID == "" {
			if _, err := l.repo.SaveMessageToThread(ctx, chatID, threadID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), seed.userContent, &threadID, attachments, nil); err != nil {
				return fmt.Errorf("failed to save first message: %w", err)
			}
			return nil
		}
		// The client's id is the row's id, and rows share one id space: this
		// chat is new (or pending, with no message under this id yet), so a
		// row that already holds the id belongs to some other send, and the id
		// cannot be the key of this one.
		_, created, err := l.repo.SaveMessageToThreadOnce(ctx, chatID, threadID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), seed.userContent, &threadID, attachments, nil, userMessageID)
		if err != nil {
			return fmt.Errorf("failed to save first message: %w", err)
		}
		if !created {
			return ErrClientMessageReused
		}
	}
	return nil
}

type startParams struct {
	chat          *db.Chat
	workflowName  string
	spec          Spec
	initialData   map[string]interface{}
	userContent   string
	eventID       string
	trigger       *v2.TriggerInfo
	generateTitle bool
	// attemptAt is taken before this attempt's durable stamp (the launch
	// transaction, or ClaimWorkflowStart for a retry).
	attemptAt time.Time
}

// start brings the run in line with the committed intent: it issues the
// Temporal start and closes the "pending" window.
func (l *Launcher) start(ctx context.Context, p startParams) (*Result, error) {
	chat := p.chat
	chatID := chat.ID
	workflowID := chatID
	initialData := p.initialData

	// Build execution context for the workflow
	// This is the source of truth for thread, message, and execution state.
	// A branch's thread was forked by BranchChat but is still the root thread
	// (thread = workflow id), so it takes the same new-thread mode as a fresh
	// chat — which is what SendMessage's old pending path did too.
	execContext := &v2.ExecutionContext{
		WorkflowID:   workflowID,
		ChatID:       chatID,
		WorkflowName: p.workflowName,
		Thread:       workflowID,
		ThreadMode:   model.ThreadModeNew,
		UserJWT:      p.spec.UserJWT,
	}

	// Start workflow on shared task queue.
	//
	// USE_EXISTING, not TERMINATE_EXISTING: the workflow id is the chat id, so
	// a retried launch must attach to the run it already started rather than
	// kill it. SendMessage's restart paths keep TERMINATE_EXISTING because
	// they are continuations, not launches.
	workflowOptions := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                l.taskQueue,
		WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		Memo:                     map[string]interface{}{"trigger_event_id": p.eventID},
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
		WorkflowTaskTimeout:      workflow.DynamicWorkflowTaskTimeout,
	}
	if p.spec.RecordExecution != nil {
		// An acknowledged or completed admitted start must never create a
		// second chain when a post-accept database write is retried.
		workflowOptions.WorkflowIDReusePolicy = enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE
	}

	// Inject session daemon if set on chat. A chat that reaches here still
	// unpinned (a branch's first send, a trigger-recorded chat) is pinned
	// first; an existing pin is never overwritten.
	if l.pinner != nil {
		l.pinner.Pin(ctx, chat)
	}
	InjectSessionDaemonID(initialData, chat)

	// An unattended run has nobody to answer an ask_user or ask_question; the
	// runtime reads this off the root inputs and propagates it, monotonically,
	// to every sub-workflow and spawn.
	//
	// Injected AFTER validation, like session_daemon_id above, because it is a
	// fact about the RUN rather than a declared workflow input — no workflow
	// declares `unattended`. It is in workflow.RuntimeInjectedInputs for the
	// same reason session_daemon_id is: the runtime validates inputs again
	// inside the workflow, and BuildWorkflowInputs refuses to take any such
	// key from a client, so only a launch can make a run unattended.
	if p.spec.Unattended {
		initialData[v2.InputKeyUnattended] = true
	}
	// Who the integration writes speak as. Injected after validation like
	// unattended, and only for an unattended launch: the bot is an
	// automation's identity, so a person's own run can never carry it even if
	// a caller set the field. Absent means the owner.
	if p.spec.Unattended && p.spec.ActingIdentity.OrOwner() == core.ActingIdentityApp {
		initialData[v2.InputKeyActingIdentity] = string(core.ActingIdentityApp)
		if p.spec.Automation != "" {
			initialData[v2.InputKeyAutomation] = p.spec.Automation
		}
	}
	// This is the run the launch started; a reply's run never carries the mark,
	// which is how the runtime tells the two apart (v2.InputKeyLaunchRun).
	// Injected after validation for the same reason as unattended.
	initialData[v2.InputKeyLaunchRun] = true

	workflowInput := v2.WorkflowInput{
		ChatID:       chatID,
		WorkflowName: p.workflowName,
		Inputs:       initialData,
		ExecContext:  execContext,
		Trigger:      p.trigger,
		// The run asks the daemon whether the directory holds code, before
		// its first LLM call (runtime.runGreenfieldProbe) — not this
		// request, which must not wait on a machine that may be asleep. A
		// run with no machine has no directory to ask about.
		GreenfieldProbe: p.spec.GreenfieldProbe && !chat.NoMachine,
	}

	// The title start rides alongside the root start rather than after it.
	// Both follow the commit, so the chat row GenerateTitleWorkflow reads
	// already exists, and neither needs the other's result: serially they
	// were two Temporal round trips on every launch. Joined before returning
	// so it never outlives the request's context.
	titleStarted := l.startTitleGeneration(ctx, p)
	defer func() { <-titleStarted }()

	// The root start stays synchronous. Returning before Temporal accepted
	// it would hand the caller a committed chat that may never run.
	//
	// The start RPC carries a deadline of core.StartAttemptDeadline past
	// attemptAt, propagated to Temporal, so no start of this attempt is
	// accepted long after its durable stamp. Capacity recovery and the
	// stranded-start sweep wait core.StartAttemptBound past that stamp
	// before treating the start as no longer in flight.
	startCtx, cancelStart := context.WithDeadline(ctx, p.attemptAt.Add(core.StartAttemptDeadline))
	workflowRun, err := l.temporal.ExecuteWorkflow(startCtx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	cancelStart()
	if err != nil && p.spec.RecordExecution != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &already) {
			if recoverErr := l.recoverAdmission(ctx, p.eventID, workflowID, p.spec); recoverErr == nil {
				return nil, &AlreadyLaunchedError{ChatID: chatID, EventID: p.eventID}
			}
		}
	}
	if err != nil {
		logging.ErrorContext(ctx, "Failed to start workflow", "error", err, "chatID", chatID)
		l.recordFailedStart(ctx, workflowID)
		return nil, &InternalError{Reason: "failed to start workflow", Err: err}
	}

	runID := workflowRun.GetRunID()
	if err := l.recoverAdmission(ctx, p.eventID, workflowID, p.spec); err != nil {
		return nil, &InternalError{Reason: "failed to record admitted execution", Err: err}
	}
	l.runs.RecordRun(ctx, chatID, workflowID, runID)

	// Close the pending window now rather than waiting for the run's own
	// WorkflowStatus activity: "pending" must mean exactly "this chat has
	// never started", and SendMessage relies on that to reject a first send.
	// The run's own later write to active is a no-op on an active row. A lost
	// CAS means the run already got there first.
	if _, err := l.repo.CompareAndSwapWorkflowStatus(ctx, workflowID, db.Active(), db.Pending()); err != nil {
		logging.WarnContext(ctx, "Failed to mark launched workflow active", "error", err, "chatID", chatID)
	}

	// Fetch created chat
	startedChat, err := l.repo.GetChat(ctx, chatID)
	if err != nil {
		logging.ErrorContext(ctx, "Failed to fetch started chat", "error", err, "chatID", chatID)
		return nil, &InternalError{Reason: "failed to fetch created chat", Err: err}
	}

	return &Result{
		Chat:       startedChat,
		WorkflowID: workflowID,
		RunID:      runID,
		EventID:    p.eventID,
	}, nil
}

// failedStartMarkBudget bounds recording a failed start. The write runs on a
// context detached from the caller's, because the commonest failed start is
// the caller's own deadline expiring, and that is exactly when the record
// matters.
const failedStartMarkBudget = 5 * time.Second

// recordFailedStart stamps the root PENDING-with-a-failed-start, which is
// what lets a trigger's next fire tell this stranded launch from one whose
// start is still in flight (the overlap guard blocks only on the latter).
//
// Best-effort: if the write is lost too, the root stays PENDING with no stamp
// and reads as in flight, and the reconciler records the failure once the
// start has been missing from Temporal for its bound
// (reconciliation.strandedStartBound). That is the trade-off of a lost write:
// a trigger's later fires skip until then, rather than risk a second run
// beside one that may yet start.
//
// A failed call does not always mean Temporal has no run: one that timed out
// after the server accepted it did start. A retry of this launch claims the
// start again and re-issues it with USE_EXISTING, which attaches to that run;
// only a later fire that abandons this one in the window before that retry
// can leave the row failed while its run executes, and the run's own status
// writes repair the row once it executes.
func (l *Launcher) recordFailedStart(ctx context.Context, workflowID string) {
	markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failedStartMarkBudget)
	defer cancel()
	if _, err := l.repo.MarkWorkflowStartFailed(markCtx, workflowID); err != nil {
		logging.WarnContext(ctx, "Failed to record a failed workflow start; the reconciler will", "error", err, "workflowID", workflowID)
	}
}

// startTitleGeneration starts GenerateTitleWorkflow in the background when the
// launch asked for it, and returns a channel closed once that start has
// returned. Best-effort: a failed or panicking start is logged and never fails
// the launch.
func (l *Launcher) startTitleGeneration(ctx context.Context, p startParams) <-chan struct{} {
	done := make(chan struct{})
	if !p.generateTitle {
		close(done)
		return done
	}
	chatID := p.chat.ID
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				logging.ErrorContext(ctx, "Title generation start panicked", "panic", r, "chatID", chatID)
			}
		}()
		options := client.StartWorkflowOptions{
			ID:                       fmt.Sprintf("generate-title-%s", chatID),
			TaskQueue:                l.taskQueue,
			WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
		}
		input := map[string]interface{}{
			"chat_id":       chatID,
			"first_message": p.userContent,
		}
		if _, err := l.temporal.ExecuteWorkflow(ctx, options, "GenerateTitleWorkflow", input); err != nil {
			logging.ErrorContext(ctx, "Failed to start title generation workflow", "error", err, "chatID", chatID)
		}
	}()
	return done
}

// splitSeedMessages separates user and system messages from the seed messages.
// Returns: userContent (concatenated user messages), systemMessages slice, and
// whether any user content exists.
func splitSeedMessages(messages []SeedMessage) (userContent string, systemMessages []SeedMessage, hasUserContent bool) {
	var userParts []string
	for _, msg := range messages {
		switch msg.Role {
		case reliantv1.MessageRole_MESSAGE_ROLE_USER:
			if msg.Content != "" {
				userParts = append(userParts, msg.Content)
			}
		case reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM:
			if msg.Content != "" {
				systemMessages = append(systemMessages, msg)
			}
		}
	}
	userContent = strings.Join(userParts, "\n")
	hasUserContent = len(userParts) > 0
	return
}

// displayStyleProtoToInt32Ptr converts a proto DisplayStyle pointer to an *int32 for the database.
func displayStyleProtoToInt32Ptr(ds *reliantv1.DisplayStyle) *int32 {
	if ds == nil {
		return nil
	}
	v := int32(*ds)
	return &v
}

// TriggerInfoFromEvent is the launch event as the runtime carries it.
func TriggerInfoFromEvent(row *core.TriggerEvent) *v2.TriggerInfo {
	info := &v2.TriggerInfo{
		Kind:       string(row.Kind),
		EventID:    row.ID,
		OccurredAt: row.OccurredAt.UTC().Format(time.RFC3339),
		Payload:    row.Payload,
		Sender:     row.Sender,
		Origin:     row.Origin,
	}
	if row.TriggerID != nil {
		info.TriggerID = *row.TriggerID
	}
	return info
}

// LoadChatTrigger returns the launch event of an existing chat, for restarts
// that rebuild its WorkflowInput. A chat that predates trigger events has none,
// so it reads as an interactive start.
func LoadChatTrigger(ctx context.Context, repo interface {
	GetTriggerEventByChatID(ctx context.Context, chatID string) (*core.TriggerEvent, error)
}, chatID string) *v2.TriggerInfo {
	row, err := repo.GetTriggerEventByChatID(ctx, chatID)
	if err != nil || row == nil {
		return &v2.TriggerInfo{Kind: string(core.TriggerEventKindChatStart)}
	}
	return TriggerInfoFromEvent(row)
}

// recordStartSelection makes a start's params the chat's selection, so the
// follow-up sends of a chat that was started on a model keep running on it. It
// layers the start over what the chat already holds (a branch's copy of its
// source), and writes nothing for a start that carries no params.
func (l *Launcher) recordStartSelection(ctx context.Context, userID string, chat *db.Chat, workflowName string, params map[string]*structpb.Value) error {
	patch := l.SelectionPatch(ctx, userID, chat.ProjectID, workflowName, params)
	if len(patch) == 0 {
		return nil
	}
	if _, err := chatselection.NewInstrumented(l.repo).Merge(ctx, chat.ID, userID, workflowName, patch, 0); err != nil {
		return fmt.Errorf("failed to record the chat's selection: %w", err)
	}
	return nil
}
