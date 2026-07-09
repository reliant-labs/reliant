// Copyright (c) 2025 Reliant Labs

package daemonliveness_test

import (
	"context"
	"testing"
	"time"

	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"

	"github.com/reliant-labs/reliant/internal/daemonliveness"
	"github.com/reliant-labs/reliant/internal/daemonquery"
)

// startTestNATS runs an in-process NATS server on a random port and returns a
// client connection. Mirrors the helper in internal/daemonquery's tests.
func startTestNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1 // random port
	srv := natstest.RunServer(&opts)
	t.Cleanup(func() { srv.Shutdown() })

	if !srv.ReadyForConnections(2 * time.Second) {
		t.Fatal("test nats server failed to come up")
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect to test nats: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// countSource implements daemonquery.UserLivenessSource with a fixed count.
type countSource map[string]int

func (c countSource) ConnectedDaemonCountForUser(userID string) int { return c[userID] }

// TestReachableByUser_NATSLiveWinsOverStaleDB proves the per-user variant is
// NATS-first: a gateway holding a live stream answers live=true even when the
// DB row is stale (e.g. heartbeat writes lagging). Before the per-user
// subject existed, this scenario read as offline and failed chat preflight.
func TestReachableByUser_NATSLiveWinsOverStaleDB(t *testing.T) {
	nc := startTestNATS(t)
	responder := daemonquery.NewUserResponder(nc, countSource{"u-1": 1})
	responder.OnDaemonConnected("u-1", "d-1")
	t.Cleanup(responder.CloseAll)
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	stale := time.Now().UTC().Add(-2 * time.Minute) // past DefaultStaleThreshold
	repo := &fakeRepo{userLastSeen: map[string]time.Time{"u-1": stale}}

	got, err := daemonliveness.ReachableByUser(context.Background(), nc, repo, "u-1")
	if err != nil {
		t.Fatalf("ReachableByUser: %v", err)
	}
	if !got.Live {
		t.Error("Live = false, want true (NATS answer must win over stale DB row)")
	}
	if got.CacheStale {
		t.Error("CacheStale = true, want false (answer came from NATS, not the DB fallback)")
	}
}

// TestReachableByUser_ErrUnavailableIsDefinitiveNotLive: no gateway anywhere
// is subscribed for the user → not live, even when the DB row is fresh. The
// DB is consulted only to decorate LastSeen.
func TestReachableByUser_ErrUnavailableIsDefinitiveNotLive(t *testing.T) {
	nc := startTestNATS(t) // no responder subscribed

	fresh := time.Now().UTC().Add(-5 * time.Second) // within DefaultStaleThreshold
	repo := &fakeRepo{userLastSeen: map[string]time.Time{"u-1": fresh}}

	got, err := daemonliveness.ReachableByUser(context.Background(), nc, repo, "u-1")
	if err != nil {
		t.Fatalf("ReachableByUser: %v", err)
	}
	if got.Live {
		t.Error("Live = true, want false (no responder is the canonical not-connected signal)")
	}
	if !got.LastSeen.Equal(fresh) {
		t.Errorf("LastSeen = %v, want %v (decorated from the DB best-effort)", got.LastSeen, fresh)
	}
	if got.CacheStale {
		t.Error("CacheStale = true, want false (ErrUnavailable is canonical, not a cache fallback)")
	}
}

// TestReachableByUser_UnsubscribedReplicaDoesNotMaskAnother covers the
// multi-replica subtlety end-to-end: replica A lost its last stream for the
// user and unsubscribed; replica B still holds one. ReachableByUser must get
// B's live=true, never a false answer from A.
func TestReachableByUser_UnsubscribedReplicaDoesNotMaskAnother(t *testing.T) {
	nc := startTestNATS(t)

	replicaA := daemonquery.NewUserResponder(nc, countSource{"u-1": 0})
	replicaA.OnDaemonConnected("u-1", "d-1")
	t.Cleanup(replicaA.CloseAll)

	replicaB := daemonquery.NewUserResponder(nc, countSource{"u-1": 1})
	replicaB.OnDaemonConnected("u-1", "d-2")
	t.Cleanup(replicaB.CloseAll)

	replicaA.OnDaemonDisconnected("u-1", "d-1") // last daemon on A → unsubscribe
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	repo := &fakeRepo{userLastSeen: map[string]time.Time{}}
	for i := 0; i < 5; i++ {
		got, err := daemonliveness.ReachableByUser(context.Background(), nc, repo, "u-1")
		if err != nil {
			t.Fatalf("ReachableByUser (iteration %d): %v", i, err)
		}
		if !got.Live {
			t.Fatalf("Live = false on iteration %d, want true (replica B still holds a stream)", i)
		}
	}
}

// TestReachableByUser_TransportErrorFallsBackToDB: a real transport failure
// (closed connection) must degrade to the DB path with CacheStale semantics —
// exactly like Reachable's per-daemon fallback.
func TestReachableByUser_TransportErrorFallsBackToDB(t *testing.T) {
	nc := startTestNATS(t)
	nc.Close() // force nats.ErrConnectionClosed — a non-ErrUnavailable error

	now := time.Now().UTC()
	repo := &fakeRepo{userLastSeen: map[string]time.Time{
		"user-fresh": now.Add(-5 * time.Second),
		"user-stale": now.Add(-2 * time.Minute),
	}}

	t.Run("fresh_row_reads_live", func(t *testing.T) {
		got, err := daemonliveness.ReachableByUser(context.Background(), nc, repo, "user-fresh")
		if err != nil {
			t.Fatalf("ReachableByUser: %v", err)
		}
		if !got.Live {
			t.Error("Live = false, want true from the DB fallback")
		}
	})

	t.Run("stale_row_reads_not_live_and_cache_stale", func(t *testing.T) {
		got, err := daemonliveness.ReachableByUser(context.Background(), nc, repo, "user-stale")
		if err != nil {
			t.Fatalf("ReachableByUser: %v", err)
		}
		if got.Live {
			t.Error("Live = true, want false for a stale row")
		}
		if !got.CacheStale {
			t.Error("CacheStale = false, want true (DB fallback past the freshness window)")
		}
	})
}
