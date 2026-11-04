package tools_daemon

import (
	"context"
	"encoding/json"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// DaemonStatus implements daemonquery.StatusSource. Returns the connection's
// lastActivity timestamp and daemonType if this gateway currently holds a
// stream for the daemon. ok=false means no connection — the caller's NATS
// status query will time out and the caller will treat the daemon as
// disconnected, which is correct.
//
// Reads from the connections map under the read lock; safe for high QPS as
// long as listeners don't fan out into it under the write lock (they don't).
func (s *Service) DaemonStatus(daemonID string) (lastActive time.Time, daemonType string, ok bool) {
	s.mu.RLock()
	conn, found := s.connections[daemonID]
	s.mu.RUnlock()
	if !found {
		return time.Time{}, "", false
	}
	return conn.lastActivity, conn.daemonType, true
}

// TouchDaemonsForUser bumps lastActivity on every daemon connection currently
// held for the given user. Used by the cross-process activity ping (the
// control-plane LLM proxy publishes a NATS message when it forwards a call so
// the daemon's status query reflects real usage, not just heartbeat cadence).
func (s *Service) TouchDaemonsForUser(userID string) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, daemonID := range s.userDaemons[userID] {
		if conn, ok := s.connections[daemonID]; ok {
			conn.lastActivity = now
		}
	}
}

// publishDaemonHeartbeat publishes an ephemeral daemon heartbeat event
// through the UserUpdateHub so the frontend knows the daemon is alive.
// This is NOT persisted to the database — it's a fire-and-forget notification.
// When the heartbeat carries workspace memory telemetry (cloud daemons in a
// cgroup-limited pod), it is included so the UI can react in real time.
func (s *Service) publishDaemonHeartbeat(_ context.Context, userID, daemonID string, ts time.Time, hb *reliantv1.DaemonHeartbeat) {
	if s.deps.UserUpdateHub == nil {
		return
	}
	payload := map[string]interface{}{
		"daemon_id":      daemonID,
		"last_heartbeat": ts.Unix(),
	}
	if hb != nil && hb.MemoryLimitBytes > 0 {
		payload["memory_used_bytes"] = hb.MemoryUsedBytes
		payload["memory_limit_bytes"] = hb.MemoryLimitBytes
		payload["memory_pressure"] = hb.MemoryPressure
		// The workspace's OOM kills, so the app can tell the user a process
		// was killed at the memory limit — including ones no tool call ran.
		if hb.OomKills > 0 {
			payload["oom_kills"] = hb.OomKills
			payload["last_oom_kill_at_ms"] = hb.LastOomKillAtMs
		}
	}
	if hb != nil {
		// Always included (may be empty) so the UI can clear its preview
		// affordance when the last detected listener goes away.
		ports := hb.DetectedPorts
		if ports == nil {
			ports = []uint32{}
		}
		payload["detected_ports"] = ports
	}
	data, _ := json.Marshal(payload)
	s.deps.UserUpdateHub.Publish(context.Background(), streaming.UpdateEvent[db.UserUpdate]{
		Key: userID,
		Payload: db.UserUpdate{
			UserID:     userID,
			UpdateType: db.UserUpdateDaemonHeartbeat,
			EntityType: db.EntityTypeSystem,
			EntityID:   daemonID,
			Data:       data,
			CreatedAt:  ts,
		},
	})
}

// diskReportRefresh is how old a stored disk report may get while nothing
// material changes: the reported-at time the UI shows stays honest without a
// write per 15s heartbeat.
const diskReportRefresh = 5 * time.Minute

// recordDiskReport stores a heartbeat's disk report on the daemon row
// (daemons.storage_state). Unlike the memory telemetry, which lives and dies
// with the attachment row, a disk report outlives the connection: a disk that
// was full is still full when the machine resumes, and the storage inbox item
// keeps saying so with the time it was reported.
//
// It writes only when the report changed materially (diskwatch.Signature) or
// the stored one is diskReportRefresh old, and tells the user's web clients to
// refetch the daemon list when the part they must see at once changed — a
// volume coming under pressure, a breakdown measured, a command failing with
// ENOSPC, a cache clean-up starting or ending.
//
// Each write carries the pressure episode forward from the report stored
// before it (diskwatch.Stamp). That episode is what the storage inbox item is
// keyed by, so it must come from the row, not this connection: a cloud
// machine that suspends and resumes with the same full disk is one episode.
func (s *Service) recordDiskReport(ctx context.Context, conn *daemonConnection, rep *reliantv1.DiskReport) {
	now := time.Now().UTC()
	sig := diskwatch.Signature(rep)
	if sig == conn.lastDiskSig && now.Sub(conn.lastDiskWrite) < diskReportRefresh {
		return
	}
	if !conn.lastDiskLoaded {
		raw, err := s.deps.Repo.GetDaemonStorageState(ctx, conn.daemonID)
		if err != nil {
			// Not fatal: the worst case is a running episode restarting,
			// which re-raises the storage item once.
			logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to read stored disk report", "error", err, "daemonID", conn.daemonID)
		}
		conn.lastDiskStored, conn.lastDiskLoaded = diskwatch.Decode(raw), true
	}
	stamped := diskwatch.Stamp(rep, conn.lastDiskStored, now)
	stored, err := diskwatch.Encode(stamped)
	if err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to encode disk report", "error", err, "daemonID", conn.daemonID)
		return
	}
	if err := s.deps.Repo.SetDaemonStorageState(ctx, conn.daemonID, stored); err != nil {
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Failed to record disk report", "error", err, "daemonID", conn.daemonID)
		return
	}
	urgent := diskwatch.UrgentSignature(rep)
	urgentChanged := urgent != conn.lastDiskUrgent
	conn.lastDiskSig, conn.lastDiskUrgent, conn.lastDiskWrite, conn.lastDiskStored = sig, urgent, now, stamped
	if urgentChanged {
		s.publishDaemonListChanged(conn.userID, conn.daemonID)
	}
}

// daemonListRefetchType is the ephemeral REFETCH payload type published when
// something ListDaemons reports for a user changed: a daemon attached,
// detached, or moved lifecycle phase. Web clients refetch the daemon list on
// it instead of polling ListDaemons every few seconds.
const daemonListRefetchType = "daemons"

// publishDaemonListChanged tells the user's web clients to refetch the daemon
// list. Ephemeral like the heartbeat (never persisted, never replayed): a
// client that was disconnected when it fired refetches on reconnect instead,
// and keeps a slow fallback poll for anything that slips past both.
func (s *Service) publishDaemonListChanged(userID, daemonID string) {
	if s.deps.UserUpdateHub == nil || userID == "" {
		return
	}
	data, _ := json.Marshal(map[string]string{"type": daemonListRefetchType, "daemon_id": daemonID})
	s.deps.UserUpdateHub.Publish(context.Background(), streaming.UpdateEvent[db.UserUpdate]{
		Key: userID,
		Payload: db.UserUpdate{
			UserID:     userID,
			UpdateType: db.UserUpdateRefetch,
			EntityType: db.EntityTypeSystem,
			EntityID:   daemonID,
			Data:       data,
			CreatedAt:  time.Now().UTC(),
		},
	})
}

// PublishDaemonListChanged is publishDaemonListChanged for state this service
// learns about second-hand — the daemonstate derivation applying a
// control-plane lifecycle event. Those events may omit the owner, so an empty
// userID is resolved from the registry row; a daemon with no row has nothing
// a client could list, and is skipped.
func (s *Service) PublishDaemonListChanged(ctx context.Context, userID, daemonID string) {
	if userID == "" {
		if s.deps.Repo == nil || daemonID == "" {
			return
		}
		d, err := s.deps.Repo.GetDaemon(ctx, daemonID)
		if err != nil || d == nil {
			logging.DebugContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" No registry row to announce a daemon change for", "daemonID", daemonID, "error", err)
			return
		}
		userID = d.UserID
	}
	s.publishDaemonListChanged(userID, daemonID)
}

// ============================================
// DaemonConnectionManager interface implementation
// ============================================

// ListConnectedDaemons returns info about all daemons connected for a user.
// Implements toolexec.ConnectedDaemonLister.
func (s *Service) ListConnectedDaemons(userID string) []toolexec.DaemonInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	daemonIDs := s.userDaemons[userID]
	result := make([]toolexec.DaemonInfo, 0, len(daemonIDs))
	for _, dID := range daemonIDs {
		c := s.connections[dID]
		if c == nil {
			continue
		}
		result = append(result, toolexec.DaemonInfo{
			DaemonID:   c.daemonID,
			Name:       c.name,
			Labels:     c.labels,
			Type:       c.daemonType,
			Status:     "connected",
			LastActive: c.lastActivity,
		})
	}
	return result
}

// IsDaemonOnline checks if a daemon is currently connected for the user
func (s *Service) IsDaemonOnline(_ context.Context, userID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.userDaemons[userID]) > 0
}

// HasConnectedDaemonsForUser reports whether this gateway currently holds any
// daemon stream for the user. Implements toolexec.LocalConnectionChecker so the
// NATS router can skip the DB freshness check on the hot path.
func (s *Service) HasConnectedDaemonsForUser(userID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.userDaemons[userID]) > 0
}

// ConnectedDaemonCountForUser reports how many daemon streams this gateway
// currently holds for the user. Implements daemonquery.UserLivenessSource so
// the per-user any-live NATS responder answers from the in-memory connection
// map — authoritative for the streams this gateway owns.
func (s *Service) ConnectedDaemonCountForUser(userID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.userDaemons[userID])
}

// GetActiveConnections returns count of active connections (for monitoring)
func (s *Service) GetActiveConnections() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.connections)
}

// HeldDaemonStream is a point-in-time snapshot of one daemon stream this
// gateway holds. Deliberately not toolexec.DaemonInfo: the flow-health
// assertion needs connectedAt (a stream too young to have a registry row yet
// is not evidence about anything) and needs no identity fields at all — the
// endpoint it feeds is status-only and must not be able to leak a name or a
// hostname even by accident.
type HeldDaemonStream struct {
	DaemonID     string
	ConnectedAt  time.Time
	LastActivity time.Time
}

// HeldDaemonStreams snapshots every daemon stream this gateway currently
// holds, inbound and outbound alike.
//
// This is the gateway's OWN answer to "which daemons do I believe I am
// serving right now", and it is the scope /flow-health asserts over. Without
// it the endpoint can only read daemon_attachment, which cannot distinguish a
// daemon that SHOULD be connected from a row nobody ever cleaned up: rows are
// deleted on graceful teardown only, so a crashed or rescheduled gateway
// strands its rows forever (dev, 2026-08-24: a 29-day-old and a 51-day-old
// orphan pinned /flow-health at 503 permanently). A stream present in this
// map is the one piece of evidence that someone is still expected to be
// there.
//
// Read under s.mu — the same lock that guards conn.lastActivity, which the
// receive loop and runSender both write.
func (s *Service) HeldDaemonStreams() []HeldDaemonStream {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]HeldDaemonStream, 0, len(s.connections))
	for _, c := range s.connections {
		out = append(out, HeldDaemonStream{
			DaemonID:     c.daemonID,
			ConnectedAt:  c.connectedAt,
			LastActivity: c.lastActivity,
		})
	}
	return out
}

// GetConnectedUsers returns list of connected user IDs (for monitoring)
func (s *Service) GetConnectedUsers() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	users := make([]string, 0, len(s.userDaemons))
	for userID := range s.userDaemons {
		users = append(users, userID)
	}
	return users
}
