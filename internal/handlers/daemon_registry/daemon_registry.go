// Copyright (c) 2025 Reliant Labs
package daemon_registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/pkg/daemonwire"
)

// AttachmentStaleThreshold is the freshness window for daemon_attachment
// rows when determining whether a daemon is currently routable. 6 missed 15s
// heartbeats, matching the gateway's staleConnectionThreshold and
// daemonliveness.DefaultStaleThreshold — 30s (the old value) left zero margin
// over the heartbeat interval, so a single delayed lease renewal made the UI
// status dot flip to DISCONNECTED for a live daemon. Clean disconnects delete
// the row immediately; this window only bounds crashed-gateway detection.
const AttachmentStaleThreshold = 90 * time.Second

func (s *Service) ListDaemons(
	ctx context.Context,
	req *connect.Request[reliantv1.ListDaemonsRequest],
) (*connect.Response[reliantv1.ListDaemonsResponse], error) {
	_ = req

	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	// Trigger daemon connection check and wait briefly for it to register
	// before querying the DB. No-op in cloud mode where the daemon is external.
	if s.deps.Router != nil {
		_, _ = s.deps.Router.IsDaemonOnline(ctx, userID, nil)
	}

	daemons, err := s.deps.Repo.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list daemons"))
	}

	attached, err := s.attachedDaemonSet(ctx, userID)
	if err != nil {
		return nil, err
	}
	inventories := s.storedLocalModels(ctx, userID)
	disks := s.storedDiskReports(ctx, userID)

	resp := &reliantv1.ListDaemonsResponse{Daemons: make([]*reliantv1.DaemonInfo, 0, len(daemons))}
	for _, d := range daemons {
		info := daemonToProto(d, attached[d.ID])
		info.LocalModels = localModelsFromJSON(inventories[d.ID])
		info.Disk = diskwatch.Decode(disks[d.ID])
		resp.Daemons = append(resp.Daemons, info)
	}

	return connect.NewResponse(resp), nil
}

// attachedDaemonSet returns the user's fresh daemon_attachment rows keyed by
// daemon ID. A daemon is attached (routable) iff its entry is non-nil; the
// row also carries heartbeat-reported memory telemetry.
//
// A failed read is an error (Unavailable, or the read's own deadline or
// cancellation), not an empty set. Treating it as
// "nothing attached" turned a DB hiccup into a confident answer that every one
// of the user's machines had disconnected — the client cannot tell that from
// the truth, so it shows "Your machine disconnected" for machines that are
// serving. An error lets the client keep the last status it knew and say that
// it cannot reach Reliant, which is what actually happened.
func (s *Service) attachedDaemonSet(ctx context.Context, userID string) (map[string]*db.DaemonAttachment, error) {
	attachments, err := s.deps.Repo.ListFreshDaemonAttachmentsForUser(ctx, userID, AttachmentStaleThreshold)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Unavailable("reading daemon liveness"))
	}
	attached := make(map[string]*db.DaemonAttachment, len(attachments))
	for _, att := range attachments {
		attached[att.DaemonID] = att
	}
	return attached, nil
}

func (s *Service) GetDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.GetDaemonRequest],
) (*connect.Response[reliantv1.GetDaemonResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}
	if req.Msg.GetDaemonId() == "" {
		return nil, svcerr.InvalidArgument("daemon_id is required")
	}

	daemon, err := s.deps.Repo.GetDaemon(ctx, req.Msg.GetDaemonId())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get daemon"))
	}
	if daemon.UserID != userID {
		return nil, svcerr.NotFound("daemon")
	}

	attached, err := s.attachedDaemonSet(ctx, userID)
	if err != nil {
		return nil, err
	}
	info := daemonToProto(daemon, attached[daemon.ID])
	info.LocalModels = localModelsFromJSON(s.storedLocalModels(ctx, userID)[daemon.ID])
	info.Disk = diskwatch.Decode(s.storedDiskReports(ctx, userID)[daemon.ID])
	return connect.NewResponse(&reliantv1.GetDaemonResponse{Daemon: info}), nil
}

// storedDiskReports returns the user's stored disk reports (heartbeat
// telemetry the gateway keeps on the daemon row) keyed by daemon id. A read
// failure is logged and treated as "none reported" so it cannot break the
// daemon list.
func (s *Service) storedDiskReports(ctx context.Context, userID string) map[string]string {
	reports, err := s.deps.Repo.ListDaemonStorageState(ctx, userID)
	if err != nil {
		logging.WarnContext(ctx, "[DaemonRegistry] Failed to list stored disk reports", "error", err, "userID", userID)
		return map[string]string{}
	}
	return reports
}

// ClearMachineCaches relays a confirmed cache clean-up to the machine. The
// daemon decides what is safe (diskwatch.ClearCaches) and starts it in the
// background; this returns what started and what it refused.
func (s *Service) ClearMachineCaches(
	ctx context.Context,
	req *connect.Request[reliantv1.ClearMachineCachesRequest],
) (*connect.Response[reliantv1.ClearMachineCachesResponse], error) {
	daemonID := req.Msg.GetDaemonId()
	if len(req.Msg.GetKinds()) == 0 {
		return nil, svcerr.InvalidArgument("kinds is required")
	}
	userID, router, err := s.ownedOnlineDaemon(ctx, daemonID)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(diskwatch.ClearCachesRequest{Kinds: req.Msg.GetKinds()})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("marshal request"))
	}
	raw, err := router.SendDaemonCommandToDaemon(ctx, userID, daemonID, diskwatch.ClearCachesCommand, payload, 15000)
	if err != nil {
		return nil, localModelRelayError("daemon command "+diskwatch.ClearCachesCommand, err)
	}
	var out diskwatch.ClearCachesResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal(fmt.Sprintf("decode %s response", diskwatch.ClearCachesCommand)))
	}
	resp := &reliantv1.ClearMachineCachesResponse{Started: out.Started}
	for _, sk := range out.Skipped {
		resp.Skipped = append(resp.Skipped, &reliantv1.ClearMachineCachesSkip{Kind: sk.Kind, Reason: sk.Reason})
	}
	if len(resp.Started) == 0 {
		activity.Skip(ctx) // the machine refused every kind: nothing is being cleared
	}
	return connect.NewResponse(resp), nil
}

// storedLocalModels returns the user's stored inventories keyed by daemon id.
// A read failure is logged and treated as "none published" so it cannot break
// the daemon list.
func (s *Service) storedLocalModels(ctx context.Context, userID string) map[string]string {
	inventories, err := s.deps.Repo.ListDaemonLocalModels(ctx, userID)
	if err != nil {
		logging.WarnContext(ctx, "[DaemonRegistry] Failed to list stored local models", "error", err, "userID", userID)
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
// attached; offline daemons fail with Unavailable. It returns the router to
// relay through, which is non-nil whenever err is nil.
func (s *Service) ownedOnlineDaemon(ctx context.Context, daemonID string) (userID string, router Router, err error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return "", nil, svcerr.ErrUnauthenticated
	}
	if daemonID == "" {
		return "", nil, svcerr.InvalidArgument("daemon_id is required")
	}
	daemon, err := s.deps.Repo.GetDaemon(ctx, daemonID)
	if err != nil {
		return "", nil, svcerr.WrapOr(err, svcerr.Internal("failed to get daemon"))
	}
	if daemon == nil || daemon.UserID != userID {
		return "", nil, svcerr.NotFound("daemon")
	}
	attached, err := s.attachedDaemonSet(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	if attached[daemon.ID] == nil {
		return "", nil, svcerr.Unavailable(fmt.Sprintf("daemon %s is offline", daemonID))
	}
	if s.deps.Router == nil {
		return "", nil, svcerr.Unavailable("no daemon router configured")
	}
	return userID, s.deps.Router, nil
}

// localModelRelayError is a failed relay to a daemon as the svcerr kind the rpc
// returns, saying what was being done. A daemon the relay could not reach
// (toolexec.LocalModelDaemonUnavailableError) is Unavailable in the relay's
// own words, which name the daemon and why; a machine whose state explains the
// failure gets the code that state calls for (daemonerr.MachineState); an
// error that already says what it is (a deadline, a cancellation, the
// router's Unavailable) keeps its code. Anything else is Internal: the
// daemon's own words reach the user, the router's and the transport's stay on
// the error as its logged cause.
func localModelRelayError(doing string, err error) error {
	var unavailable *toolexec.LocalModelDaemonUnavailableError
	if errors.As(err, &unavailable) {
		return svcerr.WithCause(svcerr.Unavailable(unavailable.Error()), err)
	}
	if serr, ok := daemonerr.MachineState(doing, err); ok {
		return serr
	}
	msg := doing + " failed"
	if text, ok := toolexec.DaemonReportedText(err); ok {
		msg += ": " + text
	}
	return svcerr.WrapOr(err, svcerr.Internal(msg))
}

func (s *Service) RefreshLocalModels(
	ctx context.Context,
	req *connect.Request[reliantv1.RefreshLocalModelsRequest],
) (*connect.Response[reliantv1.RefreshLocalModelsResponse], error) {
	userID, router, err := s.ownedOnlineDaemon(ctx, req.Msg.GetDaemonId())
	if err != nil {
		return nil, err
	}
	inv, err := router.RefreshLocalModels(ctx, userID, req.Msg.GetDaemonId())
	if err != nil {
		return nil, localModelRelayError("refresh local models", err)
	}
	return connect.NewResponse(&reliantv1.RefreshLocalModelsResponse{LocalModels: inv}), nil
}

func (s *Service) SetLocalModelEndpoints(
	ctx context.Context,
	req *connect.Request[reliantv1.SetLocalModelEndpointsRequest],
) (*connect.Response[reliantv1.SetLocalModelEndpointsResponse], error) {
	daemonID := req.Msg.GetDaemonId()
	userID, router, err := s.ownedOnlineDaemon(ctx, daemonID)
	if err != nil {
		return nil, err
	}
	baseURLs := req.Msg.GetBaseUrls()
	if baseURLs == nil {
		baseURLs = []string{}
	}
	payload, err := json.Marshal(map[string][]string{"base_urls": baseURLs})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("marshal request"))
	}
	if _, err := router.SendDaemonCommandToDaemon(ctx, userID, daemonID, toolexec.CommandLocalModelsSetEndpoints, payload, 15000); err != nil {
		return nil, localModelRelayError("write local model endpoints", err)
	}
	inv, err := router.RefreshLocalModels(ctx, userID, daemonID)
	if err != nil {
		return nil, localModelRelayError("refresh local models", err)
	}
	return connect.NewResponse(&reliantv1.SetLocalModelEndpointsResponse{LocalModels: inv}), nil
}

func (s *Service) ResolveDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.ResolveDaemonRequest],
) (*connect.Response[reliantv1.ResolveDaemonResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	daemons, err := s.deps.Repo.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("listing daemons"))
	}

	attached, err := s.attachedDaemonSet(ctx, userID)
	if err != nil {
		return nil, err
	}

	var best *db.Daemon
	for _, d := range daemons {
		if req.Msg.DaemonId != "" && d.ID != req.Msg.DaemonId {
			continue
		}
		if req.Msg.DaemonName != "" && !daemonAnswersToName(d, req.Msg.DaemonName) {
			continue
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

func (s *Service) ResumeDaemon(
	ctx context.Context,
	req *connect.Request[reliantv1.ResumeDaemonRequest],
) (*connect.Response[reliantv1.ResumeDaemonResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	if req.Msg.DaemonId == "" {
		return nil, svcerr.InvalidArgument("daemon_id is required")
	}

	// Verify ownership.
	daemon, err := s.deps.Repo.GetDaemon(ctx, req.Msg.DaemonId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get daemon"))
	}
	if daemon.UserID != userID {
		return nil, svcerr.NotFound("daemon")
	}

	// If the daemon currently has a fresh attachment row, it's already routable.
	attached, err := s.attachedDaemonSet(ctx, userID)
	if err != nil {
		return nil, err
	}
	if attached[daemon.ID] != nil {
		return connect.NewResponse(&reliantv1.ResumeDaemonResponse{Resumed: true}), nil
	}

	// This service cannot wake a machine: resuming a suspended managed daemon
	// is control-plane's (controlplane.v1.DaemonService/ResumeDaemon, which
	// the router's EnsureAwake and the web client call directly). Nothing
	// overrides this RPC any more — control-plane deleted its
	// reliant.v1.DaemonRegistryService adapter (docs/design/one-daemon-list.md).
	return connect.NewResponse(&reliantv1.ResumeDaemonResponse{
		Resumed:      false,
		ErrorMessage: fmt.Sprintf("daemon %s has no active attachment; automatic resume not available in OSS mode", req.Msg.DaemonId),
	}), nil
}

// daemonAnswersToName reports whether a daemon is the one a caller means by
// name: the name its owner gave it, or — for a daemon that has none, or a
// caller that knows it by where it runs — its hostname.
func daemonAnswersToName(d *db.Daemon, name string) bool {
	if d.Name != "" && d.Name == name {
		return true
	}
	return d.Hostname != nil && *d.Hostname == name
}

// lifecyclePhaseToProto maps the stored public lifecycle vocabulary onto the
// proto enum. An unrecognized value yields UNSPECIFIED rather than a guess:
// the client treats UNSPECIFIED as "no extra detail" and falls back to status,
// which is always correct if less specific.
func lifecyclePhaseToProto(phase *string) reliantv1.DaemonLifecyclePhase {
	if phase == nil {
		return reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_UNSPECIFIED
	}
	switch daemonwire.LifecyclePhase(*phase) {
	case daemonwire.LifecyclePhaseProvisioning:
		return reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_PROVISIONING
	case daemonwire.LifecyclePhaseCloning:
		return reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_CLONING
	case daemonwire.LifecyclePhaseReady:
		return reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_READY
	case daemonwire.LifecyclePhaseSuspending:
		return reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_SUSPENDING
	case daemonwire.LifecyclePhaseSuspended:
		return reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_SUSPENDED
	case daemonwire.LifecyclePhaseFailed:
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
// (FAILED). No phase means no lifecycle was ever reported. That is the
// permanent state of every self-hosted daemon, so those stay DISCONNECTED; for
// a managed daemon it means the mirror is missing or unrecognised and the
// machine may well be suspended, so the honest answer is UNSPECIFIED
// ("unknown") rather than a DISCONNECTED guess that hides Resume.
func composeDaemonStatus(phase reliantv1.DaemonLifecyclePhase, attached, selfHosted bool) reliantv1.DaemonStatus {
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
	case reliantv1.DaemonLifecyclePhase_DAEMON_LIFECYCLE_PHASE_UNSPECIFIED:
		if selfHosted {
			return reliantv1.DaemonStatus_DAEMON_STATUS_DISCONNECTED
		}
		return reliantv1.DaemonStatus_DAEMON_STATUS_UNSPECIFIED
	default:
		// READY-but-unattached is DISCONNECTED, not ACTIVE: the operator says
		// the pod is up, but nothing is attached to route work to. That is
		// precisely the window the attachment lease exists to report.
		return reliantv1.DaemonStatus_DAEMON_STATUS_DISCONNECTED
	}
}

// isSelfHostedDaemonType reports whether a daemon is user-run, i.e. has no
// control-plane lifecycle to mirror. "external" is control-plane's word for it.
func isSelfHostedDaemonType(daemonType *string) bool {
	if daemonType == nil {
		return false
	}
	return *daemonType == "self_hosted" || *daemonType == "external"
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
		Name:              d.Name,
		Status:            composeDaemonStatus(phase, att != nil, isSelfHostedDaemonType(d.DaemonType)),
		Projects:          projectPathsToDiscoveredProjects(d.ProjectPaths),
		LifecyclePhase:    phase,
		LastStatusMessage: d.LastStatusMessage,
		OomKillCount:      d.OOMKillCount,
		CreatedAt:         timestamppb.New(d.CreatedAt),
		Labels:            d.Labels,
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
		info.Version = att.Build.Version
		info.Commit = att.Build.Commit
		info.CommitTimeUnix = att.Build.CommitTimeUnix
		info.ForgeVersion = att.Build.ForgeVersion
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
