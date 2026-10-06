// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// listChangedRepo is the slice of the repository the daemon-list announcement
// paths touch: teardown deletes the attachment, and an owner-less lifecycle
// change looks the owner up.
type listChangedRepo struct {
	db.Repository
	daemons map[string]*db.Daemon
}

func (listChangedRepo) DeleteDaemonAttachment(context.Context, string) error { return nil }

func (r listChangedRepo) GetDaemon(_ context.Context, id string) (*db.Daemon, error) {
	if d, ok := r.daemons[id]; ok {
		return d, nil
	}
	return nil, errors.New("not found")
}

// daemonListRefetches returns the daemon ids of every "daemons" refetch the
// hub carried, keyed by the user they were addressed to.
func daemonListRefetches(t *testing.T, hub *recordingUserHub) map[string][]string {
	t.Helper()
	hub.mu.Lock()
	defer hub.mu.Unlock()
	out := map[string][]string{}
	for _, e := range hub.events {
		if e.Payload.UpdateType != db.UserUpdateRefetch {
			continue
		}
		var payload map[string]string
		require.NoError(t, json.Unmarshal(e.Payload.Data, &payload))
		if payload["type"] != daemonListRefetchType {
			continue
		}
		out[e.Key] = append(out[e.Key], payload["daemon_id"])
	}
	return out
}

// A daemon dropping off is a change to what ListDaemons reports, so its
// owner's web clients are told to refetch — that signal is what lets them stop
// polling ListDaemons every few seconds to notice.
func TestTeardownConnection_AnnouncesDaemonListChange(t *testing.T) {
	svc := NewToolsDaemonService(listChangedRepo{})
	t.Cleanup(svc.Close)
	hub := &recordingUserHub{}
	svc.SetUserUpdateHub(hub)

	conn := newTestConn("user-1", "daemon-1", newParkedStream())
	svc.mu.Lock()
	svc.connections["daemon-1"] = conn
	svc.userDaemons["user-1"] = []string{"daemon-1"}
	svc.mu.Unlock()

	svc.teardownConnection(conn, "test")
	assert.Equal(t, map[string][]string{"user-1": {"daemon-1"}}, daemonListRefetches(t, hub))

	// Tearing down a connection that was already replaced changes nothing the
	// registry reports, and must not announce a second time.
	svc.teardownConnection(conn, "test")
	assert.Equal(t, map[string][]string{"user-1": {"daemon-1"}}, daemonListRefetches(t, hub))
}

// Control-plane lifecycle events may arrive without an owner. The announcement
// is addressed by user, so the owner is resolved from the registry row; a
// daemon with no row has nothing for a client to list and is skipped.
func TestPublishDaemonListChanged_ResolvesOwner(t *testing.T) {
	svc := NewToolsDaemonService(listChangedRepo{daemons: map[string]*db.Daemon{
		"daemon-1": {ID: "daemon-1", UserID: "user-1"},
	}})
	t.Cleanup(svc.Close)
	hub := &recordingUserHub{}
	svc.SetUserUpdateHub(hub)
	ctx := context.Background()

	svc.PublishDaemonListChanged(ctx, "", "daemon-1")
	svc.PublishDaemonListChanged(ctx, "user-2", "daemon-2")
	svc.PublishDaemonListChanged(ctx, "", "daemon-unknown")

	assert.Equal(t, map[string][]string{
		"user-1": {"daemon-1"},
		"user-2": {"daemon-2"},
	}, daemonListRefetches(t, hub))
}
