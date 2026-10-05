// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/workflow"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// defaultTriggerEventLimit bounds ListTriggerEvents when the caller asks for
// everything. The history of a schedule is unbounded, so an unlimited default
// would grow into a response nobody can render.
const defaultTriggerEventLimit = 50

// maxTriggerEventLimit caps what a caller can ask for.
const maxTriggerEventLimit = 500

// triggerSyncer is the schedule backend this handler converges onto. Declared
// here, at the consumer, so the handler can be tested without Temporal.
type triggerSyncer interface {
	Sync(ctx context.Context, triggerID string) error
	Delete(ctx context.Context, triggerID string) error
	NextFireAt(ctx context.Context, triggerID string) (*time.Time, error)
}

// triggerFireStarter starts a manual fire. Separate from triggerSyncer because
// it needs a workflow client rather than a schedule client, and because a
// deployment could in principle converge schedules without allowing run-now.
type triggerFireStarter interface {
	StartManualFire(ctx context.Context, triggerID string) (string, error)
}

// TriggerService implements the gRPC TriggerService.
type TriggerService struct {
	reliantv1connect.UnimplementedTriggerServiceHandler
	database db.Repository
	syncer   triggerSyncer
	fires    triggerFireStarter
	grants   *automationGrants
	inbound  InboundOptions
}

// triggerConnections is the slice of the connection store an integration
// trigger's write path reads.
type triggerConnections interface {
	GetConnection(ctx context.Context, userID, id string) (*core.Connection, error)
	DefaultConnection(ctx context.Context, userID, integrationID string) (*core.Connection, error)
}

// connections returns the connection store, when the repository has one.
func (s *TriggerService) connections() triggerConnections {
	if repo, ok := s.database.(interface{ Connections() core.ConnectionStore }); ok {
		return repo.Connections()
	}
	return nil
}

// WithControlPlaneClient enables delegated automation credentials: while a
// user has an enabled trigger for a daemon, a daemon-bound daemon:resume token
// is held so an unattended fire can wake exactly that daemon. Without it
// (self-hosted) nothing is minted.
func (s *TriggerService) WithControlPlaneClient(client controlplane.Client) *TriggerService {
	s.grants = &automationGrants{client: client, keys: s.database, triggers: s.database}
	return s
}

// NewTriggerService creates a new TriggerService.
//
// syncer and fires may be nil in a deployment with no Temporal client, in
// which case write paths fail loudly rather than storing triggers that can
// never fire.
func NewTriggerService(database db.Repository, syncer triggerSyncer, fires triggerFireStarter) *TriggerService {
	return &TriggerService{database: database, syncer: syncer, fires: fires}
}

// WithPolledIntegrations makes the schedule backend converge a poll schedule
// for integration triggers whose integration is polled. Must be called
// before the service handles a request.
func (s *TriggerService) WithPolledIntegrations(polled func(integration string) bool) *TriggerService {
	if backend, ok := s.syncer.(*triggers.Backend); ok && backend != nil {
		backend.WithPolledIntegrations(polled)
	}
	return s
}

// syncs reports whether a trigger is converged onto a Temporal Schedule:
// every schedule trigger, and integration triggers (whose syncer decides
// whether their integration is polled). A webhook or workflow-event trigger
// never is, so its writes need no schedule backend.
func syncs(kind core.TriggerKind) bool {
	return kind == core.TriggerKindSchedule || kind == core.TriggerKindIntegration
}

// NewTriggerServiceFor builds the service over a Temporal client, which may be
// nil in a deployment without Temporal.
//
// This exists so the nil case is handled in ONE place. A nil *triggers.Backend
// assigned to the triggerSyncer interface is a non-nil interface holding a nil
// pointer, so `s.syncer == nil` would be false and the first write would
// panic — a trap the call site cannot see. Here the interface fields are only
// ever assigned a real backend.
func NewTriggerServiceFor(database db.Repository, temporalClient client.Client, taskQueue string) *TriggerService {
	var svc *TriggerService
	if temporalClient == nil {
		svc = &TriggerService{database: database}
	} else {
		backend := triggers.NewBackend(
			temporalClient.ScheduleClient(),
			temporalClient,
			database,
			taskQueue,
		)
		svc = &TriggerService{database: database, syncer: backend, fires: backend}
	}
	// Delegated automation credentials exist only against a control plane.
	if baseURL := controlplane.BaseURLFromEnv(); baseURL != "" {
		svc.WithControlPlaneClient(controlplane.NewClient(baseURL))
	}
	return svc
}

// CreateTrigger stores the trigger, then converges its schedule.
//
// The row is written first because it is the truth; Temporal is a projection
// of it. But a create whose Sync fails is rolled BACK, because the alternative
// is reporting success for a trigger that will never fire — and the owner has
// no way to tell that apart from one that is working. Update does not roll
// back: the previous definition is already live, and a failed converge there
// is drift that SyncAll repairs.
func (s *TriggerService) CreateTrigger(
	ctx context.Context,
	req *connect.Request[reliantv1.CreateTriggerRequest],
) (*connect.Response[reliantv1.CreateTriggerResponse], error) {
	userID := auth.MustGetUserID(ctx)

	def := req.Msg.Trigger
	if def == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("trigger is required"))
	}

	trigger, err := s.triggerFromDefinition(ctx, userID, def, nil)
	if err != nil {
		return nil, err
	}
	if trigger.Kind == core.TriggerKindSchedule && s.syncer == nil {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("the schedule backend is unavailable; cannot create a trigger that would never fire"))
	}
	trigger.ID = uuid.NewString()
	// nil enabled means true: a create that omits the field wants a working
	// trigger, not a disabled one.
	trigger.Enabled = def.Enabled == nil || *def.Enabled
	// Before the row: a trigger whose owner's access cannot be read is
	// refused, not stored unable to fire.
	if err := s.refreshTriggerAccess(ctx, trigger); err != nil {
		return nil, err
	}

	if err := s.database.CreateTrigger(ctx, trigger); err != nil {
		return nil, triggerDBError("create trigger", err)
	}

	// A webhook trigger has no schedule: it fires when a delivery arrives.
	// What it has instead are credentials, written now that the row (whose
	// id they are bound to) exists. A failure removes the row: a webhook
	// trigger with no token could never be called.
	var cred *reliantv1.WebhookCredential
	if trigger.Kind == core.TriggerKindWebhook {
		cred, err = s.writeWebhookCredentials(ctx, trigger, def, true)
		if err != nil {
			if delErr := s.database.DeleteTrigger(ctx, trigger.ID); delErr != nil {
				logging.Error("failed to remove a webhook trigger whose credentials could not be written",
					"trigger_id", trigger.ID, "error", err, "delete_error", delErr)
			}
			return nil, err
		}
	}

	if err := s.sync(ctx, trigger.Kind, trigger.ID); err != nil {
		// Remove the schedule Sync may have half-created, then the row we
		// just wrote. Leaving either would mean an API that reported failure
		// for a trigger that still fires, or success for one with no schedule.
		if schedErr := s.syncer.Delete(ctx, trigger.ID); schedErr != nil {
			logging.Error("failed to remove the schedule of a trigger that could not be created",
				"trigger_id", trigger.ID, "sync_error", err, "delete_error", schedErr)
		}
		if delErr := s.database.DeleteTrigger(ctx, trigger.ID); delErr != nil {
			logging.Error("failed to remove a trigger whose schedule could not be created",
				"trigger_id", trigger.ID, "sync_error", err, "delete_error", delErr)
		}
		return nil, triggerSyncError(err)
	}
	if trigger.Enabled {
		s.grants.ensure(ctx, userID, trigger.DaemonID)
	}

	return connect.NewResponse(&reliantv1.CreateTriggerResponse{
		Trigger: s.render(ctx, trigger),
		Webhook: cred,
	}), nil
}

// sync converges the trigger's schedule when its kind has one. A schedule
// trigger without a backend was refused before it was stored; an
// integration trigger without one is a pushed-only deployment, where there
// is nothing to converge.
func (s *TriggerService) sync(ctx context.Context, kind core.TriggerKind, id string) error {
	if !syncs(kind) || s.syncer == nil {
		return nil
	}
	return s.syncer.Sync(ctx, id)
}

// GetTrigger returns one trigger with its read-only projections.
func (s *TriggerService) GetTrigger(
	ctx context.Context,
	req *connect.Request[reliantv1.GetTriggerRequest],
) (*connect.Response[reliantv1.GetTriggerResponse], error) {
	trigger, err := s.ownedTrigger(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.GetTriggerResponse{
		Trigger: s.render(ctx, trigger),
	}), nil
}

// ListTriggers returns the caller's triggers.
func (s *TriggerService) ListTriggers(
	ctx context.Context,
	req *connect.Request[reliantv1.ListTriggersRequest],
) (*connect.Response[reliantv1.ListTriggersResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// The filter carries the caller's id, so this can never return another
	// user's triggers regardless of what project id was asked for.
	filters := core.TriggerFilters{UserID: userID, ProjectID: req.Msg.ProjectId}
	stored, err := s.database.ListTriggers(ctx, filters)
	if err != nil {
		return nil, triggerDBError("list triggers", err)
	}

	// One query for every trigger's recent firings: health and last_event are
	// computed from it, so the list costs a constant number of queries.
	ids := make([]string, len(stored))
	for i, t := range stored {
		ids[i] = t.ID
	}
	recent, err := s.database.RecentTriggerFirings(ctx, userID, ids, triggers.HealthWindow)
	if err != nil {
		logging.Warn("could not resolve triggers' recent firings", "error", err)
		recent = nil
	}
	// And one for every polled trigger's source state, which health folds in.
	regs, err := s.database.ListTriggerRegistrations(ctx, userID, ids)
	if err != nil {
		logging.Warn("could not resolve triggers' source state", "error", err)
		regs = nil
	}

	workflows := triggers.NewCachedWorkflows(triggers.LaunchWorkflows{Repo: s.database})
	out := make([]*reliantv1.Trigger, 0, len(stored))
	for _, t := range stored {
		out = append(out, s.renderWithWorkflows(ctx, t, recent[t.ID], regs[t.ID], workflows))
	}
	return connect.NewResponse(&reliantv1.ListTriggersResponse{Triggers: out}), nil
}

// UpdateTrigger replaces the definition and reconverges the schedule. The id,
// owner and kind are not updatable.
func (s *TriggerService) UpdateTrigger(
	ctx context.Context,
	req *connect.Request[reliantv1.UpdateTriggerRequest],
) (*connect.Response[reliantv1.UpdateTriggerResponse], error) {
	existing, err := s.ownedTrigger(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}

	def := req.Msg.Trigger
	if def == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("trigger is required"))
	}

	updated, err := s.triggerFromDefinition(ctx, existing.UserID, def, existing)
	if err != nil {
		return nil, err
	}
	if updated.Kind == core.TriggerKindSchedule && s.syncer == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("the schedule backend is unavailable"))
	}
	updated.ID = existing.ID
	updated.CreatedAt = existing.CreatedAt
	// nil enabled on update means "leave it as it is": an update of the
	// schedule should not silently resume a trigger the owner paused.
	updated.Enabled = existing.Enabled
	if def.Enabled != nil {
		updated.Enabled = *def.Enabled
	}
	if err := s.refreshTriggerAccess(ctx, updated); err != nil {
		return nil, err
	}

	if err := s.database.UpdateTrigger(ctx, updated); err != nil {
		return nil, triggerDBError("update trigger", err)
	}

	if updated.Kind == core.TriggerKindWebhook {
		// The token is untouched by an edit; only RotateWebhookToken
		// replaces it. The HMAC secret follows the definition.
		if _, err := s.writeWebhookCredentials(ctx, updated, def, false); err != nil {
			return nil, err
		}
	}
	// Unlike create, a failed converge here is not rolled back: the row is
	// the truth and is now correct, and SyncAll repairs Temporal at the next
	// startup. Reverting would throw away the user's edit to protect a
	// projection.
	if err := s.sync(ctx, updated.Kind, updated.ID); err != nil {
		return nil, triggerSyncError(err)
	}

	if updated.Enabled {
		s.grants.ensure(ctx, updated.UserID, updated.DaemonID)
	}
	if existing.DaemonID != updated.DaemonID || !updated.Enabled {
		s.grants.releaseIfUnused(ctx, existing.UserID, existing.DaemonID)
	}
	return connect.NewResponse(&reliantv1.UpdateTriggerResponse{
		Trigger: s.render(ctx, updated),
	}), nil
}

// DeleteTrigger removes the trigger and its schedule.
func (s *TriggerService) DeleteTrigger(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteTriggerRequest],
) (*connect.Response[reliantv1.DeleteTriggerResponse], error) {
	trigger, err := s.ownedTrigger(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}

	// Drop the schedule FIRST. A schedule outliving its row keeps firing for a
	// trigger nobody can see or stop; a row outliving its schedule merely
	// stops firing, and the next Sync fixes it.
	if s.syncer != nil && syncs(trigger.Kind) {
		if err := s.syncer.Delete(ctx, trigger.ID); err != nil {
			return nil, triggerSyncError(err)
		}
	}
	if err := s.database.DeleteTrigger(ctx, trigger.ID); err != nil {
		return nil, triggerDBError("delete trigger", err)
	}
	s.grants.releaseIfUnused(ctx, trigger.UserID, trigger.DaemonID)
	return connect.NewResponse(&reliantv1.DeleteTriggerResponse{}), nil
}

// SetTriggerEnabled pauses or resumes a trigger.
func (s *TriggerService) SetTriggerEnabled(
	ctx context.Context,
	req *connect.Request[reliantv1.SetTriggerEnabledRequest],
) (*connect.Response[reliantv1.SetTriggerEnabledResponse], error) {
	trigger, err := s.ownedTrigger(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if trigger.Kind == core.TriggerKindSchedule && s.syncer == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("the schedule backend is unavailable"))
	}
	if req.Msg.Enabled {
		enabling := *trigger
		enabling.Enabled = true
		if err := s.refreshTriggerAccess(ctx, &enabling); err != nil {
			return nil, err
		}
	}

	if err := s.database.SetTriggerEnabled(ctx, trigger.ID, req.Msg.Enabled); err != nil {
		return nil, triggerDBError("set trigger enabled", err)
	}
	trigger.Enabled = req.Msg.Enabled

	// A pushed trigger needs nothing converged — the receivers read
	// `enabled` off the row for every event — but a polled one pauses its
	// poll schedule.
	if err := s.sync(ctx, trigger.Kind, trigger.ID); err != nil {
		return nil, triggerSyncError(err)
	}
	if trigger.Enabled {
		s.grants.ensure(ctx, trigger.UserID, trigger.DaemonID)
	} else {
		s.grants.releaseIfUnused(ctx, trigger.UserID, trigger.DaemonID)
	}
	return connect.NewResponse(&reliantv1.SetTriggerEnabledResponse{
		Trigger: s.render(ctx, trigger),
	}), nil
}

// FireTrigger runs a trigger now. The firing is asynchronous: this returns the
// fire workflow id, and the outcome shows up in ListTriggerEvents.
func (s *TriggerService) FireTrigger(
	ctx context.Context,
	req *connect.Request[reliantv1.FireTriggerRequest],
) (*connect.Response[reliantv1.FireTriggerResponse], error) {
	trigger, err := s.ownedTrigger(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	if trigger.Kind.IsEventDriven() {
		fireID, err := s.fireInbound(ctx, trigger)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(&reliantv1.FireTriggerResponse{FireWorkflowId: fireID}), nil
	}
	if s.fires == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("the schedule backend is unavailable"))
	}

	fireID, err := s.fires.StartManualFire(ctx, trigger.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("start fire: %w", err))
	}
	return connect.NewResponse(&reliantv1.FireTriggerResponse{FireWorkflowId: fireID}), nil
}

// GetLaunchEvent returns the event that launched a chat. Ownership is the
// chat's: another user's chat is NotFound, never PermissionDenied, and a chat
// that exists but has no launch event yields an empty response.
//
// The payload is returned as recorded. What the launcher records is the start
// (workflow, presets, params), the seed fingerprint and the source's own
// fields; the caller's JWT is never part of it (it travels in the in-memory
// Spec), so owner-only access is the whole boundary.
func (s *TriggerService) GetLaunchEvent(
	ctx context.Context,
	req *connect.Request[reliantv1.GetLaunchEventRequest],
) (*connect.Response[reliantv1.GetLaunchEventResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if req.Msg.ChatId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("chat_id is required"))
	}
	chat, err := s.database.GetChat(ctx, req.Msg.ChatId)
	if err != nil || chat == nil || chat.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("chat not found"))
	}
	ev, err := s.database.GetTriggerEventByChatID(ctx, chat.ID)
	if err != nil {
		if errors.Is(err, core.ErrTriggerEventNotFound) {
			return connect.NewResponse(&reliantv1.GetLaunchEventResponse{}), nil
		}
		return nil, triggerDBError("get launch event", err)
	}
	if ev == nil || ev.UserID != userID {
		return connect.NewResponse(&reliantv1.GetLaunchEventResponse{}), nil
	}
	out, err := triggers.EventToProto(ev)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("render launch event"))
	}
	return connect.NewResponse(&reliantv1.GetLaunchEventResponse{Event: out}), nil
}

// ListTriggerEvents returns a trigger's firings, newest first.
func (s *TriggerService) ListTriggerEvents(
	ctx context.Context,
	req *connect.Request[reliantv1.ListTriggerEventsRequest],
) (*connect.Response[reliantv1.ListTriggerEventsResponse], error) {
	// Resolve ownership through the TRIGGER, not the events: the event rows
	// carry a user id, but checking the trigger is what makes "a trigger id
	// you do not own is NotFound" true.
	if _, err := s.ownedTrigger(ctx, req.Msg.TriggerId); err != nil {
		return nil, err
	}

	limit := int(req.Msg.Limit)
	switch {
	case limit <= 0:
		limit = defaultTriggerEventLimit
	case limit > maxTriggerEventLimit:
		limit = maxTriggerEventLimit
	}

	filters := core.TriggerEventFilters{
		UserID:    auth.MustGetUserID(ctx),
		TriggerID: req.Msg.TriggerId,
		Limit:     limit,
	}
	for _, o := range req.Msg.Outcomes {
		outcome, ok := triggerOutcomeFromProto(o)
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("outcomes: unspecified outcome"))
		}
		filters.Outcomes = append(filters.Outcomes, outcome)
	}
	if req.Msg.PageToken != nil && *req.Msg.PageToken != "" {
		cursor, err := decodeTriggerEventCursor(*req.Msg.PageToken)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid page_token"))
		}
		filters.After = cursor
	}

	stored, hasMore, err := s.database.ListTriggerEvents(ctx, filters)
	if err != nil {
		return nil, triggerDBError("list trigger events", err)
	}

	out := make([]*reliantv1.TriggerEvent, 0, len(stored))
	for _, ev := range stored {
		proto, err := triggers.EventWithRunToProto(ev)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		out = append(out, proto)
	}
	resp := &reliantv1.ListTriggerEventsResponse{Events: out}
	if hasMore && len(stored) > 0 {
		last := stored[len(stored)-1].Event
		resp.NextPageToken = encodeTriggerEventCursor(core.TriggerEventCursor{OccurredAt: last.OccurredAt, ID: last.ID})
	}
	return connect.NewResponse(resp), nil
}

// The page token is the last firing's (occurred_at, id), base64url-encoded and
// opaque to callers. It is the list's sort key, so a firing recorded
// mid-pagination can neither repeat nor be skipped.
func encodeTriggerEventCursor(c core.TriggerEventCursor) string {
	raw := strconv.FormatInt(c.OccurredAt.UnixMicro(), 10) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeTriggerEventCursor(token string) (*core.TriggerEventCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	micros, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return nil, errors.New("malformed cursor")
	}
	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return nil, err
	}
	return &core.TriggerEventCursor{OccurredAt: time.UnixMicro(n).UTC(), ID: id}, nil
}

func triggerOutcomeFromProto(o reliantv1.TriggerEventOutcome) (core.TriggerEventOutcome, bool) {
	switch o {
	case reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_LAUNCHED:
		return core.TriggerEventLaunched, true
	case reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_SKIPPED:
		return core.TriggerEventSkipped, true
	case reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_FAILED:
		return core.TriggerEventFailed, true
	}
	return "", false
}

// ownedTrigger loads a trigger and verifies the caller owns it.
//
// Another user's trigger is NotFound, not PermissionDenied: telling a caller
// that an id exists but is not theirs leaks the existence of other users'
// triggers for nothing.
func (s *TriggerService) ownedTrigger(ctx context.Context, id string) (*core.Trigger, error) {
	if id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("id is required"))
	}
	userID := auth.MustGetUserID(ctx)

	trigger, err := s.database.GetTrigger(ctx, id)
	if err != nil {
		if errors.Is(err, core.ErrTriggerNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("trigger not found"))
		}
		return nil, triggerDBError("get trigger", err)
	}
	if trigger.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("trigger not found"))
	}
	return trigger, nil
}

// triggerFromDefinition validates a wire definition into a storable row.
// existing is the current row on update and nil on create.
func (s *TriggerService) triggerFromDefinition(
	ctx context.Context,
	userID string,
	def *reliantv1.TriggerDefinition,
	existing *core.Trigger,
) (*core.Trigger, error) {
	if def.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name is required"))
	}
	// A trigger with no prompt would launch a run with nothing to do, and the
	// agent would have no way to ask what was wanted — nobody is watching.
	if def.Message == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("message is required"))
	}
	if def.ProjectId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("project_id is required"))
	}

	// The machine is an explicit choice: a daemon, or no_machine. An empty
	// daemon_id alone is still the mistake it always was, so a client that
	// omits the field cannot silently create a run with no machine.
	switch {
	case def.NoMachine && def.DaemonId != "":
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("no_machine and daemon_id are mutually exclusive: runs either have a machine or they do not"))
	case !def.NoMachine && def.DaemonId == "":
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("daemon_id is required: a trigger must name the daemon its runs execute on, or set no_machine"))
	}

	if _, err := s.database.GetProjectWithUserCheck(ctx, def.ProjectId, userID); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("project not found"))
	}

	if def.NoMachine {
		if err := s.validateNoMachineWorkflow(ctx, userID, def); err != nil {
			return nil, err
		}
	} else if err := s.validateTriggerDaemon(ctx, userID, def.ProjectId, def.DaemonId); err != nil {
		return nil, err
	}

	if def.WorktreeId != nil && *def.WorktreeId != "" {
		worktree, err := s.database.GetWorktree(ctx, *def.WorktreeId)
		if err != nil || worktree == nil {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("worktree not found"))
		}
		// The worktree has to belong to the project the runs execute in;
		// otherwise the trigger would launch runs against a checkout of a
		// different project.
		if worktree.ProjectID != def.ProjectId {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				errors.New("worktree does not belong to the project"))
		}
	}

	// An activation's source, filter and inputs come from the workflow's
	// declaration; an ad hoc trigger writes its source inline.
	var (
		src  *triggers.Source
		decl *triggers.Declaration
		err  error
	)
	if name := def.GetWorkflowTrigger(); name != "" {
		decl, err = s.resolveActivation(ctx, userID, def, name)
		if err != nil {
			return nil, err
		}
		src = decl.Source
	} else {
		// The source arm is what determines the kind, so an absent arm is
		// not a defaultable field — there is no kind to store. Validated
		// before storing: the Temporal server would reject a bad cron later
		// as an opaque RPC error, by which point the row exists with no
		// working schedule behind it.
		src, err = triggers.SourceFromDefinition(def)
		if err != nil {
			var cfgErr *triggers.ConfigError
			if errors.As(err, &cfgErr) {
				return nil, connect.NewError(connect.CodeInvalidArgument, cfgErr)
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	if existing != nil {
		wasActivation := existing.WorkflowTrigger != nil
		if wasActivation != (decl != nil) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(
				"a trigger cannot change between activating a workflow's declared trigger and writing its source inline; delete it and create the other"))
		}
		if existing.Kind != src.Kind {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a trigger's kind is not updatable"))
		}
	}
	if src.Kind == core.TriggerKindSchedule && strings.TrimSpace(def.Filter) != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("filter: a schedule fires on time, not on an event, so there is nothing to filter"))
	}

	now := time.Now().UTC()
	trigger := &core.Trigger{
		UserID:           userID,
		ProjectID:        def.ProjectId,
		WorktreeID:       def.WorktreeId,
		Name:             def.Name,
		Kind:             src.Kind,
		Workflow:         def.Workflow,
		Presets:          def.Presets,
		Params:           triggers.ParamsFromProto(def.Params),
		Message:          def.Message,
		DaemonID:         def.DaemonId,
		NoMachine:        def.NoMachine,
		NotifyOnComplete: def.NotifyOnComplete,
		Config:           src.Config,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if trigger.WorktreeID != nil && *trigger.WorktreeID == "" {
		trigger.WorktreeID = nil
	}
	if src.Kind.IsEventDriven() {
		if err := s.applyInboundDefinition(ctx, userID, def, src, trigger, existing); err != nil {
			return nil, err
		}
	}
	if decl != nil {
		name := decl.Name
		trigger.WorkflowTrigger = &name
		// The row's filter is the declaration's, as of now: a projection the
		// fire path re-reads from the workflow.
		trigger.Filter = decl.Filter
	}
	return trigger, nil
}

// resolveActivation reads the declaration a definition activates and checks
// what the activation itself may set. The returned declaration's source is
// then validated and stored like an inline one.
func (s *TriggerService) resolveActivation(ctx context.Context, userID string, def *reliantv1.TriggerDefinition, name string) (*triggers.Declaration, error) {
	if strings.TrimSpace(def.Workflow) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("workflow is required: a declared trigger is activated from the workflow that declares it"))
	}
	if strings.TrimSpace(def.Filter) != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("filter: an activation's filter is its declaration's; edit the workflow's triggers: block to change it"))
	}
	wf, err := launch.ResolveRunWorkflow(ctx, s.database, userID, def.Workflow, def.ProjectId)
	if err != nil {
		var lookup *launch.WorkflowLookupError
		if errors.As(err, &lookup) {
			return nil, triggerDBError("resolve workflow", err)
		}
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("workflow %q does not resolve to a runnable workflow: %w", def.Workflow, err))
	}
	if triggers.FindDeclaredTrigger(wf, name) == nil {
		declared := triggers.DeclaredTriggerNames(wf)
		msg := fmt.Sprintf("workflow %q does not declare a trigger named %q", def.Workflow, name)
		if len(declared) > 0 {
			msg += " (it declares: " + strings.Join(declared, ", ") + ")"
		}
		return nil, connect.NewError(connect.CodeNotFound, errors.New(msg))
	}
	// Activation has no previous kind to hold the declaration to.
	decl, err := triggers.DeclarationIn(wf, &core.Trigger{Workflow: def.Workflow, WorkflowTrigger: &name})
	if err != nil {
		var declErr *triggers.DeclarationError
		if errors.As(err, &declErr) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, declErr)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// A param the declaration's inputs also set would be silently replaced
	// by the event's value at every fire; refuse it rather than store a
	// setting that does nothing.
	for input := range decl.Inputs {
		if _, ok := def.Params[input]; ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
				"params.%s: the declared trigger %q sets this input from each event; remove it from params", input, name))
		}
	}
	return decl, nil
}

// validateTriggerDaemon checks that the daemon exists, is the caller's, and can
// host the project.
//
// Another user's daemon is NotFound, identical to a missing one, so a caller
// cannot probe which daemon ids exist.
func (s *TriggerService) validateTriggerDaemon(ctx context.Context, userID, projectID, daemonID string) error {
	return validateOwnedProjectDaemon(ctx, s.database, userID, projectID, daemonID)
}

// validateNoMachineWorkflow refuses a no-machine trigger whose workflow will not
// work without a machine, so the author learns it while writing the trigger
// rather than from a run nobody is watching. Stricter than a no-machine chat
// launch: an agent GIVEN machine tools is refused too, because for an
// unattended run "it quietly had fewer tools than you configured" is a silent
// downgrade. The trigger's params are what the run will start with, so a
// builtin agent with tools: ["tag:web"] passes. See research/DAEMONLESS_RUNS.md.
func (s *TriggerService) validateNoMachineWorkflow(ctx context.Context, userID string, def *reliantv1.TriggerDefinition) error {
	workflowName := def.Workflow
	if workflowName == "" {
		workflowName = workflow.DefaultWorkflow
	}
	wf, err := launch.ResolveRunWorkflow(ctx, s.database, userID, workflowName, def.ProjectId)
	if err != nil {
		var lookup *launch.WorkflowLookupError
		if errors.As(err, &lookup) {
			return triggerDBError("resolve workflow", err)
		}
		// An unresolvable workflow is reported where it is resolved for the
		// launch itself; nothing about machines can be said of it here.
		return nil
	}
	loader := func(ref string) (*reliantv1.Workflow, error) {
		return launch.ResolveRunWorkflow(ctx, s.database, userID, ref, def.ProjectId)
	}
	req := v2.MachineRequirements(wf, triggers.ParamsFromProto(def.Params), loader, tools.PreflightConfig())
	if req.None() {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"this workflow needs a machine: %s. Pick a machine for this automation, or give it only tools "+
			"that run without one (for example tools: [\"tag:web\"])", req.Summary()))
}

// render builds the wire trigger, resolving the read-only projections.
//
// Neither projection is allowed to fail the response: next_fire_at comes from
// Temporal and last_event from a second query, and a trigger the caller cannot
// see at all is a worse outcome than one whose next fire time is momentarily
// absent.
func (s *TriggerService) render(ctx context.Context, t *core.Trigger) *reliantv1.Trigger {
	// The write paths hand over the row they just stored, which has no joined
	// names; re-read it so every response carries them.
	if fresh, err := s.database.GetTrigger(ctx, t.ID); err == nil {
		t = fresh
	}
	recent, err := s.database.RecentTriggerFirings(ctx, t.UserID, []string{t.ID}, triggers.HealthWindow)
	if err != nil {
		logging.Warn("could not resolve a trigger's recent firings", "trigger_id", t.ID, "error", err)
	}
	regs, err := s.database.ListTriggerRegistrations(ctx, t.UserID, []string{t.ID})
	if err != nil {
		logging.Warn("could not resolve a trigger's source state", "trigger_id", t.ID, "error", err)
	}
	return s.renderWithWorkflows(ctx, t, recent[t.ID], regs[t.ID], triggers.LaunchWorkflows{Repo: s.database})
}

// renderWithWorkflows renders a trigger, resolving an activation's
// declaration through workflows so that a broken one reports BROKEN health.
// It is computed on read, from the workflow as it is now: a stored verdict
// would go stale the moment someone fixed the YAML.
func (s *TriggerService) renderWithWorkflows(ctx context.Context, t *core.Trigger, firings []*core.TriggerEventWithRun, reg *core.TriggerRegistration, workflows triggers.WorkflowResolver) *reliantv1.Trigger {
	proto := s.renderFirings(ctx, t, firings, reg)
	if t.WorkflowTrigger == nil {
		return proto
	}
	if _, err := triggers.ResolveDeclaration(ctx, workflows, t); err != nil {
		var declErr *triggers.DeclarationError
		if errors.As(err, &declErr) {
			proto.Health = triggers.BrokenHealth(declErr)
		} else {
			// The workflow could not be read right now; that says nothing
			// about the trigger, so its firings still describe it.
			logging.Warn("could not resolve a trigger's declaration", "trigger_id", t.ID, "error", err)
		}
	}
	return proto
}

// renderFirings renders a trigger from its row, recent firings and (for a
// polled trigger) its source state.
func (s *TriggerService) renderFirings(ctx context.Context, t *core.Trigger, firings []*core.TriggerEventWithRun, reg *core.TriggerRegistration) *reliantv1.Trigger {
	var nextFireAt *time.Time
	if s.syncer != nil && syncs(t.Kind) {
		next, err := s.syncer.NextFireAt(ctx, t.ID)
		if err != nil {
			logging.Warn("could not resolve a trigger's next fire time", "trigger_id", t.ID, "error", err)
		} else {
			nextFireAt = next
		}
	}

	proto, err := triggers.ToProto(t, nextFireAt, firings, reg)
	if err != nil {
		// Params that will not round-trip through structpb. Report the trigger
		// without them rather than failing the whole call.
		logging.Error("could not fully render a trigger", "trigger_id", t.ID, "error", err)
		return &reliantv1.Trigger{Id: t.ID, Name: t.Name, ProjectId: t.ProjectID, Enabled: t.Enabled}
	}
	switch t.Kind {
	case core.TriggerKindWebhook:
		webhookURL := triggers.WebhookURL(s.inbound.PublicURL, t.ID)
		proto.WebhookUrl = &webhookURL
	case core.TriggerKindIntegration:
		// A provider whose webhook each user sets on their own resources
		// (Twilio's per-number "A message comes in" URL) needs the user to
		// know it; an operator-registered app webhook does not.
		if uc, ok := s.inbound.Catalog.(userConfiguredCatalog); ok {
			if cfg, err := triggers.IntegrationConfigFor(t); err == nil && uc.UserConfiguredURL(cfg.Integration) {
				eventsURL := triggers.IntegrationEventsURL(s.inbound.PublicURL, cfg.Integration)
				proto.WebhookUrl = &eventsURL
			}
		}
	}
	return proto
}

func triggerDBError(op string, err error) error {
	logging.Error("trigger database error", "op", op, "error", err)
	return connect.NewError(connect.CodeInternal, errors.New("database error"))
}

// triggerSyncError maps a schedule-backend failure. A config the backend
// rejects is the caller's problem; anything else is the backend being
// unreachable, which is retryable.
func triggerSyncError(err error) error {
	var cfgErr *triggers.ConfigError
	if errors.As(err, &cfgErr) {
		return connect.NewError(connect.CodeInvalidArgument, cfgErr)
	}
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("schedule backend: %w", err))
}
