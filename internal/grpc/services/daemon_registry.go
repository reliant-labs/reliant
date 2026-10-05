// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonstate"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// daemonAttachmentStaleThreshold is the freshness window for daemon_attachment
// rows when determining whether a daemon is currently routable. 6 missed 15s
// heartbeats, matching the gateway's staleConnectionThreshold and
// daemonliveness.DefaultStaleThreshold — 30s (the old value) left zero margin
// over the heartbeat interval, so a single delayed lease renewal made the UI
// status dot flip to DISCONNECTED for a live daemon. Clean disconnects delete
// the row immediately; this window only bounds crashed-gateway detection.
const daemonAttachmentStaleThreshold = 90 * time.Second

// DaemonRegistryService handles daemon registry queries (list/get/resolve/resume).
// Daemon credentials are `rlat_` access tokens managed through TokenService.
type DaemonRegistryService struct {
	reliantv1connect.UnimplementedDaemonRegistryServiceHandler
	database db.Repository
	router   toolexec.DaemonRouter
}

func NewDaemonRegistryService(database db.Repository, router toolexec.DaemonRouter) *DaemonRegistryService {
	return &DaemonRegistryService{database: database, router: router}
}

func (s *DaemonRegistryService) ListDaemons(
	ctx context.Context,
	req *connect.Request[reliantv1.ListDaemonsRequest],
) (*connect.Response[reliantv1.ListDaemonsResponse], error) {
	_ = req

	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	// Trigger daemon connection check and wait briefly for it to register
	// before querying the DB. No-op in cloud mode where the daemon is external.
	if s.router != nil {
		_, _ = s.router.IsDaemonOnline(ctx, userID)
	}

	daemons, err := s.database.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list daemons: %w", err))
	}

	attached := s.attachedDaemonSet(ctx, userID)
	inventories := s.storedLocalModels(ctx, userID)

	resp := &reliantv1.ListDaemonsResponse{Daemons: make([]*reliantv1.DaemonInfo, 0, len(daemons))}
	for _, d := range daemons {
		info := daemonToProto(d, attached[d.ID])
		info.LocalModels = localModelsFromJSON(inventories[d.ID])
		resp.Daemons = append(resp.Daemons, info)
	}

	return connect.NewResponse(resp), nil
}

// attachedDaemonSet returns the user's fresh daemon_attachment rows keyed by
// daemon ID. A daemon is attached (routable) iff its entry is non-nil; the
// row also carries heartbeat-reported memory telemetry. Errors are logged
// and treated as "no daemons attached" so a transient DB hiccup doesn't
// break list/get responses.
func (s *DaemonRegistryService) attachedDaemonSet(ctx context.Context, userID string) map[string]*db.DaemonAttachment {
	attachments, err := s.database.ListFreshDaemonAttachmentsForUser(ctx, userID, daemonAttachmentStaleThreshold)
	if err != nil {
		logging.Warn("[DaemonRegistry] Failed to list attached daemons", "error", err, "userID", userID)
		return map[string]*db.DaemonAttachment{}
	}
	attached := make(map[string]*db.DaemonAttachment, len(attachments))
	for _, att := range attachments {
		attached[att.DaemonID] = att
	}
	return attached
}

func (s *DaemonRegistryService) GetDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.GetDaemonRequest],
) (*connect.Response[reliantv1.GetDaemonResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}
	if req.Msg.GetDaemonId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("daemon_id is required"))
	}

	daemon, err := s.database.GetDaemon(ctx, req.Msg.GetDaemonId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("daemon not found: %w", err))
	}
	if daemon.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("daemon not found"))
	}

	attached := s.attachedDaemonSet(ctx, userID)
	info := daemonToProto(daemon, attached[daemon.ID])
	info.LocalModels = localModelsFromJSON(s.storedLocalModels(ctx, userID)[daemon.ID])
	return connect.NewResponse(&reliantv1.GetDaemonResponse{Daemon: info}), nil
}

// storedLocalModels returns the user's stored inventories keyed by daemon id.
// A read failure is logged and treated as "none published" so it cannot break
// the daemon list.
func (s *DaemonRegistryService) storedLocalModels(ctx context.Context, userID string) map[string]string {
	inventories, err := s.database.ListDaemonLocalModels(ctx, userID)
	if err != nil {
		logging.Warn("[DaemonRegistry] Failed to list stored local models", "error", err, "userID", userID)
		return map[string]string{}
	}
	return inventories
}

// localModelsFromJSON decodes a stored inventory (protojson). An absent or
// undecodable value yields nil: a bad row must not break the daemon list.
func localModelsFromJSON(stored string) *reliantv1.LocalModelInventory {
	if stored == "" {
		return nil
	}
	var inv reliantv1.LocalModelInventory
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(stored), &inv); err != nil {
		logging.Warn("[DaemonRegistry] Undecodable stored local model inventory", "error", err)
		return nil
	}
	return &inv
}

// ownedOnlineDaemon verifies the caller owns the daemon and that it is
// attached; offline daemons fail with Unavailable.
func (s *DaemonRegistryService) ownedOnlineDaemon(ctx context.Context, daemonID string) (userID string, err error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return "", connect.NewError(connect.CodeUnauthenticated, nil)
	}
	if daemonID == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("daemon_id is required"))
	}
	daemon, err := s.database.GetDaemon(ctx, daemonID)
	if err != nil || daemon == nil || daemon.UserID != userID {
		return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("daemon not found"))
	}
	if s.attachedDaemonSet(ctx, userID)[daemon.ID] == nil {
		return "", connect.NewError(connect.CodeUnavailable, fmt.Errorf("daemon %s is offline", daemonID))
	}
	if s.router == nil {
		return "", connect.NewError(connect.CodeUnavailable, fmt.Errorf("no daemon router configured"))
	}
	return userID, nil
}

// localModelRelayError maps router failures to connect codes.
func localModelRelayError(err error) error {
	if errors.Is(err, toolexec.ErrLocalModelDaemonUnavailable) {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	var already *connect.Error
	if errors.As(err, &already) {
		return err
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (s *DaemonRegistryService) RefreshLocalModels(
	ctx context.Context,
	req *connect.Request[reliantv1.RefreshLocalModelsRequest],
) (*connect.Response[reliantv1.RefreshLocalModelsResponse], error) {
	userID, err := s.ownedOnlineDaemon(ctx, req.Msg.GetDaemonId())
	if err != nil {
		return nil, err
	}
	inv, err := s.router.RefreshLocalModels(ctx, userID, req.Msg.GetDaemonId())
	if err != nil {
		return nil, localModelRelayError(err)
	}
	return connect.NewResponse(&reliantv1.RefreshLocalModelsResponse{LocalModels: inv}), nil
}

func (s *DaemonRegistryService) SetLocalModelEndpoints(
	ctx context.Context,
	req *connect.Request[reliantv1.SetLocalModelEndpointsRequest],
) (*connect.Response[reliantv1.SetLocalModelEndpointsResponse], error) {
	daemonID := req.Msg.GetDaemonId()
	userID, err := s.ownedOnlineDaemon(ctx, daemonID)
	if err != nil {
		return nil, err
	}
	baseURLs := req.Msg.GetBaseUrls()
	if baseURLs == nil {
		baseURLs = []string{}
	}
	payload, err := json.Marshal(map[string][]string{"base_urls": baseURLs})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if _, err := s.router.SendDaemonCommandToDaemon(ctx, userID, daemonID, toolexec.CommandLocalModelsSetEndpoints, payload, 15000); err != nil {
		return nil, localModelRelayError(fmt.Errorf("writing local model endpoints on daemon %s: %w", daemonID, err))
	}
	inv, err := s.router.RefreshLocalModels(ctx, userID, daemonID)
	if err != nil {
		return nil, localModelRelayError(err)
	}
	return connect.NewResponse(&reliantv1.SetLocalModelEndpointsResponse{LocalModels: inv}), nil
}

func (s *DaemonRegistryService) ResolveDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.ResolveDaemonRequest],
) (*connect.Response[reliantv1.ResolveDaemonResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	daemons, err := s.database.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing daemons: %w", err))
	}

	attached := s.attachedDaemonSet(ctx, userID)

	var best *db.Daemon
	for _, d := range daemons {
		if req.Msg.DaemonId != "" && d.ID != req.Msg.DaemonId {
			continue
		}
		if req.Msg.DaemonName != "" {
			name := ""
			if d.Hostname != nil {
				name = *d.Hostname
			}
			if name != req.Msg.DaemonName {
				continue
			}
		}
		// Prefer attached (routable) daemons, but accept any match.
		if best == nil || attached[d.ID] != nil {
			best = d
		}
		if attached[d.ID] != nil {
			break
		}
	}

	if best == nil {
		return connect.NewResponse(&reliantv1.ResolveDaemonResponse{Found: false}), nil
	}

	return connect.NewResponse(&reliantv1.ResolveDaemonResponse{
		Daemon: daemonToProto(best, attached[best.ID]),
		Found:  true,
	}), nil
}

func (s *DaemonRegistryService) ResumeDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.ResumeDaemonRequest],
) (*connect.Response[reliantv1.ResumeDaemonResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	if req.Msg.DaemonId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("daemon_id is required"))
	}

	// Verify ownership.
	daemon, err := s.database.GetDaemon(ctx, req.Msg.DaemonId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("daemon not found: %w", err))
	}
	if daemon.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("daemon not found"))
	}

	// If the daemon currently has a fresh attachment row, it's already routable.
	if s.attachedDaemonSet(ctx, userID)[daemon.ID] != nil {
		return connect.NewResponse(&reliantv1.ResumeDaemonResponse{Resumed: true}), nil
	}

	// For OSS, we can only report that the daemon is not routable.
	// The control plane (commercial) can override this to actually wake cloud daemons.
	return connect.NewResponse(&reliantv1.ResumeDaemonResponse{
		Resumed:      false,
		ErrorMessage: fmt.Sprintf("daemon %s has no active attachment; automatic resume not available in OSS mode", req.Msg.DaemonId),
	}), nil
}

// lifecyclePhaseToProto maps the stored public lifecycle vocabulary onto the
// proto enum. An unrecognized value yields UNSPECIFIED rather than a guess:
// the client treats UNSPECIFIED as "no extra detail" and falls back to status,
// which is always correct if less specific.
func lifecyclePhaseToProto(phase *string) reliantv1.DaemonLifecyclePhase {
	if phase == nil {
		return reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_UNSPECIFIED
	}
	switch daemonstate.LifecyclePhase(*phase) {
	case daemonstate.LifecyclePhaseProvisioning:
		return reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_PROVISIONING
	case daemonstate.LifecyclePhaseCloning:
		return reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_CLONING
	case daemonstate.LifecyclePhaseReady:
		return reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_READY
	case daemonstate.LifecyclePhaseSuspending:
		return reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_SUSPENDING
	case daemonstate.LifecyclePhaseSuspended:
		return reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_SUSPENDED
	case daemonstate.LifecyclePhaseFailed:
		return reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_FAILED
	default:
		return reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_UNSPECIFIED
	}
}

// composeDaemonStatus produces THE status for a daemon from the two things
// that know anything about it: the attachment lease this service owns, and the
// lifecycle phase the control plane mirrors in.
//
// Attachment wins. A daemon with a fresh attachment is ACTIVE whatever the
// phase says, because a stream is attached right now and work can be routed to
// it — that is observed, and the phase is at best a slightly older mirror of a
// Workspace CR. Getting this backwards is what produced the original
// complaint: a connected, serving daemon reading DISCONNECTED because a stored
// column had not caught up.
//
// With no attachment the phase is the only information available, and it is
// what separates the three cases the old two-value enum collapsed into one:
// still coming up (PENDING), deliberately parked (SUSPENDED), and broken
// (FAILED). No phase means no lifecycle was ever reported — the permanent
// state of every self-hosted daemon — so DISCONNECTED, exactly as before.
func composeDaemonStatus(phase reliantv1.DaemonLifecyclePhase, attached bool) reliantv1.DaemonStatus {
	if attached {
		return reliantv1.DaemonStatus_DAEMON_STATUS_ACTIVE
	}
	switch phase {
	case reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_PROVISIONING,
		reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_CLONING:
		return reliantv1.DaemonStatus_DAEMON_STATUS_PENDING
	case reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_SUSPENDING,
		reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_SUSPENDED:
		return reliantv1.DaemonStatus_DAEMON_STATUS_SUSPENDED
	case reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_FAILED:
		return reliantv1.DaemonStatus_DAEMON_STATUS_FAILED
	default:
		// READY-but-unattached is DISCONNECTED, not ACTIVE: the operator says
		// the pod is up, but nothing is attached to route work to. That is
		// precisely the window the attachment lease exists to report.
		return reliantv1.DaemonStatus_DAEMON_STATUS_DISCONNECTED
	}
}

// daemonToProto converts a db.Daemon into the proto DaemonInfo.
//
// This is the one place the registry's two halves are joined: liveness, which
// this service owns (daemon_attachment freshness, plus the heartbeat-reported
// memory and port telemetry riding on the same row), and managed-machine
// lifecycle, which the control plane mirrors in over
// daemon.v1.state.<id>.lifecycle. The UI used to perform this join itself
// against two different services with two different status enums; see
// docs/design/one-daemon-list.md.
func daemonToProto(d *db.Daemon, att *db.DaemonAttachment) *reliantv1.DaemonInfo {
	if d == nil {
		return &reliantv1.DaemonInfo{}
	}

	phase := lifecyclePhaseToProto(d.LifecyclePhase)

	info := &reliantv1.DaemonInfo{
		DaemonId:          d.ID,
		UserId:            d.UserID,
		Status:            composeDaemonStatus(phase, att != nil),
		Projects:          projectPathsToDiscoveredProjects(d.ProjectPaths),
		LifecyclePhase:    phase,
		LastStatusMessage: d.LastStatusMessage,
		OomKillCount:      d.OOMKillCount,
		CreatedAt:         timestamppb.New(d.CreatedAt),
	}
	// Sourced from the attachment lease, so both are absent while nothing is
	// attached. That is deliberate: the lease row is deleted on disconnect and
	// this service keeps no history, so there is no honest "last connected" to
	// report for a detached daemon. Callers needing recency for one of those
	// fall back to CreatedAt.
	if att != nil {
		info.ConnectedAt = timestamppb.New(att.AttachedAt)
		info.LastHeartbeat = timestamppb.New(att.LastStreamActivity)
	}
	if d.Size != nil {
		info.Size = *d.Size
	}
	if d.LastStatusChangedAt != nil {
		info.LastStatusChangedAt = timestamppb.New(*d.LastStatusChangedAt)
	}
	if d.LastOOMKilledAt != nil {
		info.LastOomKilledAt = timestamppb.New(*d.LastOOMKilledAt)
	}
	if d.Hostname != nil {
		info.Hostname = *d.Hostname
	}
	if d.Platform != nil {
		info.Platform = *d.Platform
	}
	if d.DaemonType != nil {
		info.DaemonType = *d.DaemonType
	}
	if att != nil && att.MemoryLimitBytes > 0 {
		info.MemoryUsedBytes = uint64(att.MemoryUsedBytes)
		info.MemoryLimitBytes = uint64(att.MemoryLimitBytes)
		info.MemoryPressure = att.MemoryPressure
	}
	if att != nil {
		info.DetectedPorts = att.DetectedPorts
	}
	return info
}

func projectPathsToDiscoveredProjects(projectPathsJSON *string) []*reliantv1.DiscoveredProject {
	if projectPathsJSON == nil || *projectPathsJSON == "" {
		return nil
	}
	var paths []string
	if err := json.Unmarshal([]byte(*projectPathsJSON), &paths); err != nil {
		return nil
	}
	projects := make([]*reliantv1.DiscoveredProject, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		projects = append(projects, &reliantv1.DiscoveredProject{Path: path})
	}
	return projects
}
