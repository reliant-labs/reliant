// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
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

// DaemonProber is the slice of toolexec.DaemonRouter the greenfield probe uses.
// It reaches the user's filesystem, which neither the api-server nor the worker
// can see themselves. Optional: a nil prober skips the probe.
type DaemonProber interface {
	SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// Launcher turns an (Event, Spec) into a running session. See the package doc.
type Launcher struct {
	repo      db.Repository
	temporal  TemporalStarter
	runs      RunRecorder
	threads   *threads.Service
	taskQueue string
	prober    DaemonProber
}

// NewLauncher builds a launcher over the chat store, Temporal and the task
// queue runs execute on.
//
// prober may be nil, which skips the greenfield probe — the api-server has a
// daemon router, a scheduled fire on the worker does not, and neither is worth
// a second constructor.
func NewLauncher(
	repo db.Repository,
	temporal TemporalStarter,
	runRecorder RunRecorder,
	taskQueue string,
	prober DaemonProber,
) *Launcher {
	var threadsSvc *threads.Service
	if repo != nil {
		threadsSvc = threads.NewService(repo)
	}
	return &Launcher{
		repo:      repo,
		temporal:  temporal,
		runs:      runRecorder,
		threads:   threadsSvc,
		taskQueue: taskQueue,
		prober:    prober,
	}
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
	userContent, systemMessages, hasUserContent := splitSeedMessages(spec.Messages)
	seed := seedContent{userContent: userContent, systemMessages: systemMessages, hasUserContent: hasUserContent}

	// Require at least one user message or attachments
	if !hasUserContent && len(spec.Attachments) == 0 {
		return nil, &ValidationError{Reason: "at least one user message or attachment is required"}
	}

	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
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
	if ev.DedupeKey == "" && ev.Kind == core.TriggerEventKindChatStart {
		ev.DedupeKey = chatID
	}

	// A retry of an earlier launch: finish it rather than create a second chat.
	if ev.DedupeKey != "" {
		if _, err := l.repo.GetTriggerEventByDedupe(ctx, ev.Kind, ev.DedupeKey); err == nil {
			return l.finishExisting(ctx, ev, spec, seed)
		} else if !errors.Is(err, core.ErrTriggerEventNotFound) {
			return nil, &InternalError{Reason: "failed to check for an earlier launch", Err: err}
		}
	}
	if spec.NewChatID != "" {
		if _, err := l.repo.GetChat(ctx, chatID); err == nil {
			return nil, &AlreadyLaunchedError{ChatID: chatID}
		}
	}

	// Verify user owns the project and get project details
	project, err := l.repo.GetProjectWithUserCheck(ctx, spec.ProjectID, userID)
	if err != nil {
		return nil, &NotFoundError{Reason: "project not found", Err: err}
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
		ID:              chatID,
		UserID:          userID,
		Title:           title,
		ProjectID:       spec.ProjectID,
		WorkflowName:    &workflowName,
		State:           db.ChatStateIdle,
		WorkflowID:      &workflowID,
		SelectedPresets: spec.Presets,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActive:      now,
	}

	// Validate workflow tree BEFORE creating chat to avoid runtime graph failures and orphaned chats.
	// Uses runtime-equivalent loader semantics: builtin:// and usable workflow drafts only.
	if err := l.ValidateCreateChatWorkflowTree(ctx, userID, workflowName, project.ID); err != nil {
		return nil, err
	}

	if err := ValidateWorkflowParamStructure(spec.Params); err != nil {
		return nil, &ValidationError{Reason: err.Error()}
	}

	// Every chat must name a resolvable worktree, or the UI's worktree-grouped
	// chat list silently hides it. Callers that omit one (e.g. the CLI) bind to
	// the project's main worktree. Resolved after the request validators so a
	// malformed request still reports its own error rather than a project-state
	// precondition, but before GetEffectiveWorkingPath below, which reads it.
	worktreeID, err := l.ResolveChatWorktreeID(ctx, spec.ProjectID, spec.WorktreeID)
	if err != nil {
		return nil, err
	}
	chat.WorktreeID = worktreeID
	if spec.DaemonID != "" {
		chat.ActiveDaemonID = &spec.DaemonID
	}

	// DEBUG: Log raw proto tools value before any processing
	if toolsProto, ok := spec.Params["tools"]; ok {
		logging.Info("[Launch] Raw proto tools param",
			"chatID", chatID,
			"asInterface", toolsProto.AsInterface(),
			"protoString", toolsProto.String(),
		)
	} else {
		logging.Info("[Launch] No tools param in workflowParams", "chatID", chatID, "paramKeys", func() []string {
			keys := make([]string, 0, len(spec.Params))
			for k := range spec.Params {
				keys = append(keys, k)
			}
			return keys
		}())
	}

	// Build and validate workflow inputs BEFORE creating chat
	initialData, err := l.buildInputs(ctx, userID, chat, workflowName, spec.Presets, spec.Params)
	if err != nil {
		return nil, err
	}

	// A brand-new chat is a first turn by construction, so no message count is
	// needed here. When the project directory holds no code the stack is still
	// open, and the model gets that observation plus the criteria for
	// proposing forge ahead of the user's first message. No-op when the
	// project already holds code or the daemon is unreachable.
	systemMessages := seed.systemMessages
	if spec.GreenfieldProbe {
		if guidance := l.GreenfieldGuidanceForChat(ctx, userID, chat); guidance != nil {
			systemMessages = append([]SeedMessage{*guidance}, systemMessages...)
		}
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
		logging.Error("Failed to marshal chat_created data", "error", marshalErr, "chatID", chatID)
	}

	eventRow := newEventRow(ev, userID, chatID, workflowName, worktreeID)

	// The chat row, its root workflow+thread, the initial messages, the
	// chat_created announcement and the trigger event must not be observed
	// apart: a client that sees chat_created must be able to load a chat that
	// already has a thread, and a chat without its event row could be started
	// twice. Group them in one transaction so any failure leaves nothing
	// behind (no orphan chat, no thread-less chat, no announcement for a
	// chat that doesn't exist).
	if err := l.repo.RunTx(ctx, func(txCtx context.Context) error {
		if err := l.repo.CreateChat(txCtx, chat); err != nil {
			return fmt.Errorf("failed to create chat: %w", err)
		}

		if _, _, _, err := l.threads.CreateWorkflowWithThread(txCtx, threads.CreateWorkflowWithThreadOpts{
			Workflow: rootWorkflow,
			ThreadID: workflowID,
			ChatID:   chatID,
		}); err != nil {
			return fmt.Errorf("failed to create workflow and thread: %w", err)
		}

		if err := l.saveSeedMessages(txCtx, chatID, workflowID, systemMessages, seed, spec.Attachments); err != nil {
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

		created, err := l.repo.CreateTriggerEvent(txCtx, eventRow)
		if err != nil {
			return fmt.Errorf("failed to record trigger event: %w", err)
		}
		if !created {
			return errEventExists
		}
		return nil
	}); err != nil {
		if errors.Is(err, errEventExists) {
			// Lost a race with a concurrent launch of the same event.
			return l.finishExisting(ctx, ev, spec, seed)
		}
		logging.Error("Failed to create chat", "error", err, "chatID", chatID)
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
	})
}

// launchPending starts an existing PENDING chat: a branched chat's first send.
func (l *Launcher) launchPending(ctx context.Context, ev Event, spec Spec, seed seedContent) (*Result, error) {
	userID := spec.OwnerUserID

	chat, err := l.repo.GetChat(ctx, spec.ChatID)
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
	if root.Status != db.Pending() {
		return nil, ErrNotPending
	}

	project, err := l.repo.GetProjectWithUserCheck(ctx, chat.ProjectID, userID)
	if err != nil {
		return nil, &NotFoundError{Reason: "project not found", Err: err}
	}

	// A pending chat has produced nothing, so its workflow can still change;
	// this is the one place the system allows it.
	workflowName := ""
	if chat.WorkflowName != nil {
		workflowName = *chat.WorkflowName
	}
	switching := spec.Workflow != "" && spec.Workflow != workflowName
	if switching {
		workflowName = spec.Workflow
	}

	if err := l.ValidateCreateChatWorkflowTree(ctx, userID, workflowName, project.ID); err != nil {
		return nil, err
	}
	if err := ValidateWorkflowParamStructure(spec.Params); err != nil {
		return nil, &ValidationError{Reason: err.Error()}
	}

	// Pin before buildInputs, which reads ActiveDaemonID to inject
	// session_daemon_id. UpdateChat does not write the column, so it is
	// persisted separately inside the transaction below.
	pinDaemon := spec.DaemonID != "" && (chat.ActiveDaemonID == nil || *chat.ActiveDaemonID != spec.DaemonID)
	if pinDaemon {
		chat.ActiveDaemonID = &spec.DaemonID
	}

	// Merge presets: chat presets are base, request presets override
	presets := make(map[string]string)
	for k, v := range chat.SelectedPresets {
		if v != "" {
			presets[k] = v
		}
	}
	for k, v := range spec.Presets {
		if v != "" {
			presets[k] = v
		}
	}

	initialData, err := l.buildInputs(ctx, userID, chat, workflowName, presets, spec.Params)
	if err != nil {
		return nil, err
	}

	if ev.DedupeKey == "" && ev.Kind == core.TriggerEventKindChatStart {
		ev.DedupeKey = chat.ID
	}
	eventRow := newEventRow(ev, userID, chat.ID, workflowName, chat.WorktreeID)

	if err := l.repo.RunTx(ctx, func(txCtx context.Context) error {
		created, err := l.repo.CreateTriggerEvent(txCtx, eventRow)
		if err != nil {
			return fmt.Errorf("failed to record trigger event: %w", err)
		}
		if !created {
			// A previous attempt committed everything below and then failed to
			// start Temporal. Its messages are already on the thread; saving
			// them again would double them.
			existing, err := l.repo.GetTriggerEventByDedupe(txCtx, ev.Kind, ev.DedupeKey)
			if err != nil {
				return fmt.Errorf("failed to load earlier trigger event: %w", err)
			}
			eventRow.ID = existing.ID
			return nil
		}

		if switching {
			if err := l.repo.UpdateWorkflowName(txCtx, workflowID, workflowName); err != nil {
				return fmt.Errorf("failed to update workflow name: %w", err)
			}
		}
		if pinDaemon {
			if err := l.repo.UpdateChatActiveDaemon(txCtx, chat.ID, chat.ActiveDaemonID); err != nil {
				return fmt.Errorf("failed to pin chat to daemon: %w", err)
			}
		}
		if switching || len(spec.Presets) > 0 {
			chat.WorkflowName = &workflowName
			chat.SelectedPresets = presets
			chat.UpdatedAt = time.Now().UTC()
			if err := l.repo.UpdateChat(txCtx, chat); err != nil {
				return fmt.Errorf("failed to update chat: %w", err)
			}
		}

		// The branch's thread was forked by BranchChat; the seed messages
		// append to it. No greenfield probe: a branch is not a first turn of
		// new work.
		return l.saveSeedMessages(txCtx, chat.ID, workflowID, seed.systemMessages, seed, spec.Attachments)
	}); err != nil {
		logging.Error("Failed to start pending chat", "error", err, "chatID", chat.ID)
		return nil, &InternalError{Reason: "failed to start chat", Err: err}
	}

	return l.start(ctx, startParams{
		chat:          chat,
		workflowName:  workflowName,
		spec:          spec,
		initialData:   initialData,
		userContent:   seed.userContent,
		eventID:       eventRow.ID,
		trigger:       TriggerInfoFromEvent(eventRow),
		generateTitle: spec.GenerateTitle && chat.Title == "",
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
	return nil, &AlreadyLaunchedError{ChatID: chat.ID, EventID: existing.ID}
}

// newEventRow builds the trigger_events row a launch records. For an ad hoc
// chat start it fills in the resolved workflow and worktree, which the
// handler cannot know when the request left them to defaults.
func newEventRow(ev Event, userID, chatID, workflowName string, worktreeID *string) *core.TriggerEvent {
	payload := make(map[string]any, len(ev.Payload)+2)
	for key, value := range ev.Payload {
		payload[key] = value
	}
	if ev.Kind == core.TriggerEventKindChatStart {
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
	// Use worktree path if chat is in a worktree, otherwise project path
	workingPath := l.GetEffectiveWorkingPath(ctx, chat)
	initialData := l.BuildWorkflowInputs(ctx, userID, workingPath, chat.ProjectID, workflowName, presets, params)

	// Validate resolved inputs (catches empty model after defaults resolution)
	if validationErrors := l.ValidateWorkflowInputs(ctx, userID, workflowName, chat.ProjectID, initialData); len(validationErrors) > 0 {
		errMsgs := make([]string, len(validationErrors))
		for i, e := range validationErrors {
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
) error {
	for _, sysMsg := range systemMessages {
		if _, err := l.repo.SaveMessageToThread(ctx, chatID, threadID, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), sysMsg.Content, &threadID, nil, displayStyleProtoToInt32Ptr(sysMsg.DisplayStyle)); err != nil {
			return fmt.Errorf("failed to save system message: %w", err)
		}
	}
	if seed.hasUserContent || len(attachments) > 0 {
		if _, err := l.repo.SaveMessageToThread(ctx, chatID, threadID, int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), seed.userContent, &threadID, attachments, nil); err != nil {
			return fmt.Errorf("failed to save first message: %w", err)
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
		WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
	}

	// Inject session daemon if set on chat
	InjectSessionDaemonID(initialData, chat)

	// An unattended run has nobody to answer an ask_user or ask_question; the
	// runtime reads this off the root inputs and propagates it, monotonically,
	// to every sub-workflow and spawn.
	//
	// Injected AFTER validation, like session_daemon_id above, because it is a
	// fact about the RUN rather than a declared workflow input — no workflow
	// declares `unattended`, so validating it against the schema rejects it as
	// an unknown input. Unlike session_daemon_id it is NOT in
	// workflow.RuntimeInjectedInputs, which only matters for the validation
	// filter this now runs after.
	if p.spec.Unattended {
		initialData[v2.InputKeyUnattended] = true
	}

	workflowInput := v2.WorkflowInput{
		ChatID:       chatID,
		WorkflowName: p.workflowName,
		Inputs:       initialData,
		ExecContext:  execContext,
		Trigger:      p.trigger,
	}

	workflowRun, err := l.temporal.ExecuteWorkflow(ctx, workflowOptions, v2.DynamicWorkflow, workflowInput)
	if err != nil {
		logging.Error("Failed to start workflow", "error", err, "chatID", chatID)
		return nil, &InternalError{Reason: "failed to start workflow", Err: err}
	}

	runID := workflowRun.GetRunID()
	l.runs.RecordRun(ctx, chatID, workflowID, runID)

	// Close the pending window now rather than waiting for the run's own
	// WorkflowStatus activity: "pending" must mean exactly "this chat has
	// never started", and SendMessage relies on that to reject a first send.
	// The run's own later write to active is a no-op on an active row. A lost
	// CAS means the run already got there first.
	if _, err := l.repo.CompareAndSwapWorkflowStatus(ctx, workflowID, db.Active(), db.Pending()); err != nil {
		logging.Warn("Failed to mark launched workflow active", "error", err, "chatID", chatID)
	}

	// Start GenerateTitle workflow
	if p.generateTitle {
		generateTitleOptions := client.StartWorkflowOptions{
			ID:                       fmt.Sprintf("generate-title-%s", chatID),
			TaskQueue:                l.taskQueue,
			WorkflowExecutionTimeout: workflow.WorkflowExecutionTimeout,
		}
		generateTitleInput := map[string]interface{}{
			"chat_id":       chatID,
			"first_message": p.userContent,
		}
		_, titleErr := l.temporal.ExecuteWorkflow(ctx, generateTitleOptions, "GenerateTitleWorkflow", generateTitleInput)
		if titleErr != nil {
			logging.Error("Failed to start title generation workflow", "error", titleErr, "chatID", chatID)
			// Don't fail the request for title generation failure
		}
	}

	// Fetch created chat
	startedChat, err := l.repo.GetChat(ctx, chatID)
	if err != nil {
		logging.Error("Failed to fetch started chat", "error", err, "chatID", chatID)
		return nil, &InternalError{Reason: "failed to fetch created chat", Err: err}
	}

	return &Result{
		Chat:       startedChat,
		WorkflowID: workflowID,
		RunID:      runID,
		EventID:    p.eventID,
	}, nil
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
