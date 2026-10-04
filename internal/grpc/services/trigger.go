// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
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
	if s.syncer == nil {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("the schedule backend is unavailable; cannot create a trigger that would never fire"))
	}

	trigger, err := s.triggerFromDefinition(ctx, userID, def, nil)
	if err != nil {
		return nil, err
	}
	trigger.ID = uuid.NewString()
	// nil enabled means true: a create that omits the field wants a working
	// trigger, not a disabled one.
	trigger.Enabled = def.Enabled == nil || *def.Enabled

	if err := s.database.CreateTrigger(ctx, trigger); err != nil {
		return nil, triggerDBError("create trigger", err)
	}

	if err := s.syncer.Sync(ctx, trigger.ID); err != nil {
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
	}), nil
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

	out := make([]*reliantv1.Trigger, 0, len(stored))
	for _, t := range stored {
		out = append(out, s.renderWith(ctx, t, recent[t.ID]))
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
	if s.syncer == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("the schedule backend is unavailable"))
	}

	updated, err := s.triggerFromDefinition(ctx, existing.UserID, def, existing)
	if err != nil {
		return nil, err
	}
	updated.ID = existing.ID
	updated.CreatedAt = existing.CreatedAt
	// nil enabled on update means "leave it as it is": an update of the
	// schedule should not silently resume a trigger the owner paused.
	updated.Enabled = existing.Enabled
	if def.Enabled != nil {
		updated.Enabled = *def.Enabled
	}

	if err := s.database.UpdateTrigger(ctx, updated); err != nil {
		return nil, triggerDBError("update trigger", err)
	}

	// Unlike create, a failed converge here is not rolled back: the row is the
	// truth and is now correct, and SyncAll repairs Temporal at the next
	// startup. Reverting would throw away the user's edit to protect a
	// projection.
	if err := s.syncer.Sync(ctx, updated.ID); err != nil {
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
	if s.syncer != nil {
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
	if s.syncer == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("the schedule backend is unavailable"))
	}

	if err := s.database.SetTriggerEnabled(ctx, trigger.ID, req.Msg.Enabled); err != nil {
		return nil, triggerDBError("set trigger enabled", err)
	}
	trigger.Enabled = req.Msg.Enabled

	if err := s.syncer.Sync(ctx, trigger.ID); err != nil {
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
	if s.fires == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("the schedule backend is unavailable"))
	}

	fireID, err := s.fires.StartManualFire(ctx, trigger.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("start fire: %w", err))
	}
	return connect.NewResponse(&reliantv1.FireTriggerResponse{FireWorkflowId: fireID}), nil
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

	if def.DaemonId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("daemon_id is required: a trigger must name the daemon its runs execute on"))
	}

	if _, err := s.database.GetProjectWithUserCheck(ctx, def.ProjectId, userID); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("project not found"))
	}

	if err := s.validateTriggerDaemon(ctx, userID, def.ProjectId, def.DaemonId); err != nil {
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

	schedule := def.GetSchedule()
	if schedule == nil {
		// The source arm is what determines the kind, so an absent arm is not
		// a defaultable field — there is no kind to store.
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("a schedule source is required; a trigger with no source can never fire"))
	}
	if existing != nil && existing.Kind != core.TriggerKindSchedule {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a trigger's kind is not updatable"))
	}

	cfg := triggers.ScheduleConfigFromProto(schedule)
	// Validate before storing. The Temporal server would reject a bad cron
	// later as an opaque RPC error, by which point the row exists with no
	// working schedule behind it.
	if _, err := triggers.ParseScheduleConfig(cfg); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("encode schedule config: %w", err))
	}

	now := time.Now().UTC()
	trigger := &core.Trigger{
		UserID:     userID,
		ProjectID:  def.ProjectId,
		WorktreeID: def.WorktreeId,
		Name:       def.Name,
		Kind:       core.TriggerKindSchedule,
		Workflow:   def.Workflow,
		Presets:    def.Presets,
		Params:     triggers.ParamsFromProto(def.Params),
		Message:    def.Message,
		DaemonID:   def.DaemonId,
		Config:     raw,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if trigger.WorktreeID != nil && *trigger.WorktreeID == "" {
		trigger.WorktreeID = nil
	}
	return trigger, nil
}

// validateTriggerDaemon checks that the daemon exists, is the caller's, and can
// host the project.
//
// Another user's daemon is NotFound, identical to a missing one, so a caller
// cannot probe which daemon ids exist.
func (s *TriggerService) validateTriggerDaemon(ctx context.Context, userID, projectID, daemonID string) error {
	daemon, err := s.database.GetDaemon(ctx, daemonID)
	if err != nil || daemon == nil || daemon.UserID != userID {
		return connect.NewError(connect.CodeNotFound, errors.New("daemon not found"))
	}

	installs, err := s.database.ListProjectDaemonsForProject(ctx, projectID)
	if err != nil {
		return triggerDBError("list project daemons", err)
	}
	// A project with no project_daemons rows at all has no recorded install
	// anywhere (a local-path project the daemon discovered, or one predating
	// install tracking), so there is nothing to contradict and any owned
	// daemon is accepted. Once the project is installed somewhere, the trigger
	// must run where the checkout actually is.
	if len(installs) == 0 {
		return nil
	}
	for _, install := range installs {
		if install.DaemonID == daemonID && install.InstallState == core.ProjectInstallInstalled {
			return nil
		}
	}
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("project is not installed on that daemon"))
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
	return s.renderWith(ctx, t, recent[t.ID])
}

// renderWith renders a trigger whose recent firings the caller already loaded.
func (s *TriggerService) renderWith(ctx context.Context, t *core.Trigger, firings []*core.TriggerEventWithRun) *reliantv1.Trigger {
	var nextFireAt *time.Time
	if s.syncer != nil {
		next, err := s.syncer.NextFireAt(ctx, t.ID)
		if err != nil {
			logging.Warn("could not resolve a trigger's next fire time", "trigger_id", t.ID, "error", err)
		} else {
			nextFireAt = next
		}
	}

	proto, err := triggers.ToProto(t, nextFireAt, firings)
	if err != nil {
		// Params that will not round-trip through structpb. Report the trigger
		// without them rather than failing the whole call.
		logging.Error("could not fully render a trigger", "trigger_id", t.ID, "error", err)
		return &reliantv1.Trigger{Id: t.ID, Name: t.Name, ProjectId: t.ProjectID, Enabled: t.Enabled}
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
