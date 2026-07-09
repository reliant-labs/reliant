// Copyright (c) 2025 Reliant Labs

// Package daemonliveness is the single canonical "is this daemon live?"
// answer. It replaces the constellation of in-memory maps, DB checks, and
// NATS pings that previously each had their own staleness, writer, and
// reader (see internal/.dev/simplification-proposal.md for context).
//
// Two entry points:
//
//   - Reachable: keyed by daemonID. NATS pull-RPC first (canonical: subject
//     routing reaches whichever gateway holds the bidi stream). DB fallback
//     on transport error, with CacheStale=true if the row is past
//     staleThreshold.
//
//   - ReachableByUser: keyed by userID. NATS pull-RPC first on the per-user
//     any-live subject (each gateway subscribes while it holds at least one
//     daemon stream for the user; no responders = no daemon connected
//     anywhere). Same DB-fallback semantics as Reachable.
package daemonliveness

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/daemonquery"
)

// DefaultStaleThreshold is 6 missed 15s heartbeats, matching the gateway's
// staleConnectionThreshold (tools_daemon.go). It was previously 30s (2
// missed heartbeats), which left zero margin: one delayed heartbeat write or
// GC pause aged the row past the window and a live daemon read as offline —
// failing chat preflight and flipping the UI status dot. Staleness is only
// the CRASH-detection path: a clean disconnect deletes the daemon_attachment
// row immediately, so a generous window here does not delay normal
// disconnect detection. Callers that need a different window can pass their
// own value to repo.GetUserLiveness / GetDaemonLiveness.
const DefaultStaleThreshold = 90 * time.Second

// DefaultNATSTimeout is the hot-path budget for the pull-RPC. The router and
// preflight checks fall back to the DB if NATS doesn't answer in time, so
// keep this small.
const DefaultNATSTimeout = 150 * time.Millisecond

// Status is the authoritative liveness answer.
type Status struct {
	Live       bool
	LastSeen   time.Time // zero when Live == false (or when DB fallback can't supply one)
	DaemonType string
	// CacheStale is true when the answer came from the DB after the NATS
	// pull-RPC failed AND the DB row is past the staleness threshold. Callers
	// can choose to treat stale rows as not-live, but most should treat the
	// boolean as authoritative and use CacheStale only for observability.
	CacheStale bool
}

// Repository is the derived-state read for the fallback path.
type Repository interface {
	// GetUserLiveness returns Live=true if ANY daemon for this user has
	// last_stream_activity within staleThreshold. LastSeen is the freshest
	// such timestamp (or zero if the implementation can't cheaply compute it).
	GetUserLiveness(ctx context.Context, userID string, staleThreshold time.Duration) (Status, error)

	// GetDaemonLiveness returns Live=true if the daemon's last_stream_activity
	// is within staleThreshold. LastSeen is that timestamp.
	GetDaemonLiveness(ctx context.Context, daemonID string, staleThreshold time.Duration) (Status, error)
}

// Reachable is the canonical "is this daemon live?" query, keyed by daemonID.
//
// Resolution order:
//  1. NATS pull-RPC daemon.v1.query.<id>.status — subject routing reaches
//     whichever gateway holds the bidi stream. ~5ms on the happy path.
//  2. On NATS error (transport failure; ErrUnavailable means no gateway is
//     subscribed → daemon is not connected): fall back to the DB derived
//     state. CacheStale=true if the DB row is past staleThreshold.
//
// An ErrUnavailable from the NATS path is treated as definitive "not live"
// rather than a transport failure — see daemonquery.Query for the rationale.
func Reachable(ctx context.Context, nc *nats.Conn, repo Repository, daemonID string) (Status, error) {
	if daemonID == "" {
		return Status{}, fmt.Errorf("daemonliveness: empty daemonID")
	}

	// 1. NATS pull-RPC — canonical truth.
	if nc != nil {
		s, err := daemonquery.Query(ctx, nc, daemonID, DefaultNATSTimeout)
		switch {
		case err == nil:
			return Status{
				Live:       s.Connected,
				LastSeen:   time.UnixMilli(s.LastActiveMs),
				DaemonType: s.DaemonType,
			}, nil
		case errors.Is(err, daemonquery.ErrUnavailable):
			// No gateway is subscribed for this daemon. That IS the answer:
			// not live. We still consult the DB to populate LastSeen for
			// observability — but the canonical Live=false wins.
			dbStatus, dbErr := repo.GetDaemonLiveness(ctx, daemonID, DefaultStaleThreshold)
			if dbErr != nil {
				// DB error during decoration is fine to swallow; the NATS
				// answer is canonical.
				return Status{Live: false}, nil
			}
			dbStatus.Live = false // override: NATS said no.
			return dbStatus, nil
		default:
			// Real transport failure (connection closed, decode error, etc.).
			// Fall through to DB fallback so the system degrades gracefully.
		}
	}

	// 2. DB fallback. CacheStale reflects whether the row is past the freshness window.
	dbStatus, err := repo.GetDaemonLiveness(ctx, daemonID, DefaultStaleThreshold)
	if err != nil {
		return Status{}, fmt.Errorf("daemonliveness: db fallback: %w", err)
	}
	if !dbStatus.Live {
		dbStatus.CacheStale = true
	}
	return dbStatus, nil
}

// ReachableByUser answers "is any daemon for this user live?" — used by the
// routing layer (which only knows userID until it resolves a specific daemon).
//
// Resolution order mirrors Reachable:
//  1. NATS pull-RPC daemon.v1.query.user.<userID>.any-live — each gateway
//     subscribes while it holds at least one daemon stream for the user
//     (first connect subscribes, last disconnect unsubscribes), so subject
//     routing reaches a replica that can answer authoritatively. Note the
//     minimal any-live payload carries no timestamp, so LastSeen is zero on
//     this path; callers here only consume Live.
//  2. ErrUnavailable means no gateway anywhere holds a stream for this user.
//     That IS the answer: not live. LastSeen is decorated from the DB
//     best-effort for observability, but the canonical Live=false wins.
//  3. Real transport failures fall back to the DB derived state, with
//     CacheStale=true if the row is past staleThreshold.
func ReachableByUser(ctx context.Context, nc *nats.Conn, repo Repository, userID string) (Status, error) {
	if userID == "" {
		return Status{}, fmt.Errorf("daemonliveness: empty userID")
	}
	if repo == nil {
		return Status{}, fmt.Errorf("daemonliveness: nil repository")
	}

	// 1. NATS pull-RPC — canonical truth.
	if nc != nil {
		ul, err := daemonquery.QueryUserAnyLive(ctx, nc, userID, DefaultNATSTimeout)
		switch {
		case err == nil:
			return Status{Live: ul.Live}, nil
		case errors.Is(err, daemonquery.ErrUnavailable):
			// No gateway is subscribed for this user. That IS the answer:
			// not live. We still consult the DB to populate LastSeen for
			// observability — but the canonical Live=false wins.
			dbStatus, dbErr := repo.GetUserLiveness(ctx, userID, DefaultStaleThreshold)
			if dbErr != nil {
				// DB error during decoration is fine to swallow; the NATS
				// answer is canonical.
				return Status{Live: false}, nil
			}
			dbStatus.Live = false // override: NATS said no.
			return dbStatus, nil
		default:
			// Real transport failure (connection closed, decode error, etc.).
			// Fall through to DB fallback so the system degrades gracefully.
		}
	}

	// 2. DB fallback. CacheStale reflects whether the row is past the freshness window.
	dbStatus, err := repo.GetUserLiveness(ctx, userID, DefaultStaleThreshold)
	if err != nil {
		return Status{}, fmt.Errorf("daemonliveness: db fallback: %w", err)
	}
	if !dbStatus.Live {
		dbStatus.CacheStale = true
	}
	return dbStatus, nil
}
