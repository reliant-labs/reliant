// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// localModelRouter records the calls DaemonRegistryService makes.
type localModelRouter struct {
	fakeDaemonRouter

	mu         sync.Mutex
	commands   []recordedLocalModelCommand
	refreshErr error
	inventory  *reliantv1.LocalModelInventory
}

type recordedLocalModelCommand struct {
	userID, daemonID, commandType string
	payload                       []byte
}

func (r *localModelRouter) SendDaemonCommandToDaemon(_ context.Context, userID, daemonID, commandType string, payload []byte, _ int32) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, recordedLocalModelCommand{userID, daemonID, commandType, payload})
	return nil, nil
}

func (r *localModelRouter) RefreshLocalModels(context.Context, string, string) (*reliantv1.LocalModelInventory, error) {
	if r.refreshErr != nil {
		return nil, r.refreshErr
	}
	return r.inventory, nil
}

func sampleInventory() *reliantv1.LocalModelInventory {
	return &reliantv1.LocalModelInventory{
		ProbedAt: "2026-10-04T00:00:00Z",
		Endpoints: []*reliantv1.LocalModelEndpoint{{
			Id: "ollama", Kind: "ollama", BaseUrl: "http://localhost:11434/v1", Source: "detected",
			Models: []*reliantv1.LocalModelInfo{{Name: "qwen3:latest", ContextWindow: 32768, SupportsChat: true, SupportsTools: true}},
		}},
	}
}

// seedDaemon creates a daemon row, optionally attached (online).
func seedDaemon(t *testing.T, repo *db.Repo, userID, daemonID string, online bool) {
	t.Helper()
	ctx := context.Background()
	host := "laptop"
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID, Hostname: &host}))
	if online {
		now := time.Now().UTC()
		require.NoError(t, repo.UpsertDaemonAttachment(ctx, &db.DaemonAttachment{
			DaemonID: daemonID, UserID: userID, Source: db.DaemonAttachmentSourceInbound,
			AttachedAt: now, LastStreamActivity: now,
		}))
	}
}

func userCtx(userID string) context.Context {
	return context.WithValue(context.Background(), auth.UserIDContextKey, userID)
}

type recordingUserHub struct {
	mu     sync.Mutex
	events []streaming.UpdateEvent[db.UserUpdate]
}

func (h *recordingUserHub) Publish(_ context.Context, e streaming.UpdateEvent[db.UserUpdate]) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
}
func (h *recordingUserHub) Subscribe(context.Context, string) streaming.UpdateSubscription[db.UserUpdate] {
	return nil
}
func (h *recordingUserHub) Close() error { return nil }
func (h *recordingUserHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

// The gateway stores an inventory it receives, skips byte-identical
// republishes, rewrites when it changes, and tells the user's web clients to
// refetch each time it really changed.
func TestInventory_StoredSkippedWhenIdenticalAndPublished(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	seedDaemon(t, repo, "user-1", "daemon-1", true)

	svc := NewToolsDaemonService(repo)
	t.Cleanup(svc.Close)
	hub := &recordingUserHub{}
	svc.SetUserUpdateHub(hub)
	conn := newTestConn("user-1", "daemon-1", newParkedStream())
	ctx := context.Background()

	svc.handleLocalModelInventory(ctx, conn, sampleInventory())
	stored, err := repo.ListDaemonLocalModels(ctx, "user-1")
	require.NoError(t, err)
	require.Contains(t, stored, "daemon-1")
	var roundTrip reliantv1.LocalModelInventory
	require.NoError(t, protojson.Unmarshal([]byte(stored["daemon-1"]), &roundTrip))
	assert.Equal(t, "qwen3:latest", roundTrip.GetEndpoints()[0].GetModels()[0].GetName())
	assert.Equal(t, 1, hub.count(), "first inventory notifies web clients")

	// Prove the second identical publish does NOT write: tamper with the
	// stored value; an actual write would restore it.
	require.NoError(t, repo.SetDaemonLocalModels(ctx, "daemon-1", `{"tampered":true}`))
	svc.handleLocalModelInventory(ctx, conn, sampleInventory())
	after, _ := repo.ListDaemonLocalModels(ctx, "user-1")
	assert.Equal(t, `{"tampered":true}`, after["daemon-1"], "identical inventory must not be rewritten")
	assert.Equal(t, 1, hub.count(), "identical inventory must not notify again")

	changed := sampleInventory()
	changed.Endpoints[0].Models = append(changed.Endpoints[0].Models, &reliantv1.LocalModelInfo{Name: "llama3:8b", SupportsChat: true})
	svc.handleLocalModelInventory(ctx, conn, changed)
	after, _ = repo.ListDaemonLocalModels(ctx, "user-1")
	assert.Contains(t, after["daemon-1"], "llama3:8b")
	assert.Equal(t, 2, hub.count(), "a changed inventory notifies again")

	var update db.UserUpdate
	hub.mu.Lock()
	update = hub.events[0].Payload
	hub.mu.Unlock()
	assert.Equal(t, db.UserUpdateRefetch, update.UpdateType)
	var payload map[string]string
	require.NoError(t, json.Unmarshal(update.Data, &payload))
	assert.Equal(t, localModelsRefetchType, payload["type"])
	assert.Equal(t, "daemon-1", payload["daemon_id"])
}

func TestInventory_WakesRefreshWaiter(t *testing.T) {
	svc := NewToolsDaemonService(nil)
	t.Cleanup(svc.Close)
	stream := newParkedStream()
	conn := newTestConn("user-1", "daemon-1", stream)
	svc.mu.Lock()
	svc.connections["daemon-1"] = conn
	svc.mu.Unlock()
	svc.database = nopLocalModelsRepo{}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got := make(chan *reliantv1.LocalModelInventory, 1)
	go func() {
		inv, err := svc.RefreshLocalModels(ctx, "user-1", "daemon-1")
		if err == nil {
			got <- inv
		}
	}()

	// The refresh message reaches the daemon...
	select {
	case msg := <-conn.sendCh:
		require.NotNil(t, msg.GetLocalModelRefresh())
	case <-time.After(2 * time.Second):
		t.Fatal("refresh was not sent to the daemon")
	}
	// ...and the daemon's next inventory answers it.
	svc.handleLocalModelInventory(ctx, conn, sampleInventory())
	select {
	case inv := <-got:
		assert.Equal(t, "ollama", inv.GetEndpoints()[0].GetId())
	case <-time.After(2 * time.Second):
		t.Fatal("refresh waiter was not woken by the inventory")
	}
}

// nopLocalModelsRepo satisfies the one method handleLocalModelInventory calls.
type nopLocalModelsRepo struct{ db.Repository }

func (nopLocalModelsRepo) SetDaemonLocalModels(context.Context, string, string) error { return nil }

// Chunks are handed to the request's consumer in order, a done chunk retires
// the registration, and teardown fails in-flight relays with an error chunk.
func TestRelay_DispatchOrderingAndTeardown(t *testing.T) {
	svc := NewToolsDaemonService(nil)
	t.Cleanup(svc.Close)
	conn := newTestConn("user-1", "daemon-1", newParkedStream())
	svc.mu.Lock()
	svc.connections["daemon-1"] = conn
	svc.mu.Unlock()

	var got []*reliantv1.LocalModelHTTPChunk
	closeRelay, err := svc.OpenLocalModelRelay("user-1", "daemon-1",
		&reliantv1.LocalModelHTTPRequest{RequestId: "r1", EndpointId: "ollama", Method: "GET", Path: "/models"},
		func(c *reliantv1.LocalModelHTTPChunk) { got = append(got, c) })
	require.NoError(t, err)
	defer closeRelay()

	sent := <-conn.sendCh
	assert.Equal(t, "r1", sent.GetLocalModelHttpRequest().GetRequestId())

	conn.dispatchLocalModelChunk(&reliantv1.LocalModelHTTPChunk{RequestId: "r1", Sequence: 0, Status: 200})
	conn.dispatchLocalModelChunk(&reliantv1.LocalModelHTTPChunk{RequestId: "r1", Sequence: 1, Data: []byte("a")})
	conn.dispatchLocalModelChunk(&reliantv1.LocalModelHTTPChunk{RequestId: "r1", Sequence: 2, Done: true})
	conn.dispatchLocalModelChunk(&reliantv1.LocalModelHTTPChunk{RequestId: "r1", Sequence: 3, Data: []byte("late")})
	conn.dispatchLocalModelChunk(&reliantv1.LocalModelHTTPChunk{RequestId: "unknown", Sequence: 0})
	require.Len(t, got, 3, "chunks after done and for unknown requests are dropped")
	assert.Equal(t, uint64(0), got[0].GetSequence())
	assert.Equal(t, uint64(2), got[2].GetSequence())

	// Teardown while a second request is in flight.
	var failed *reliantv1.LocalModelHTTPChunk
	_, err = svc.OpenLocalModelRelay("user-1", "daemon-1",
		&reliantv1.LocalModelHTTPRequest{RequestId: "r2", EndpointId: "ollama"},
		func(c *reliantv1.LocalModelHTTPChunk) { failed = c })
	require.NoError(t, err)
	conn.failLocalModelRelays("daemon disconnected")
	require.NotNil(t, failed)
	assert.True(t, failed.GetDone())
	assert.Equal(t, "daemon disconnected", failed.GetError())
}

func TestRelay_OnlyOwnersDaemonIsReachable(t *testing.T) {
	svc := NewToolsDaemonService(nil)
	t.Cleanup(svc.Close)
	conn := newTestConn("user-1", "daemon-1", newParkedStream())
	svc.mu.Lock()
	svc.connections["daemon-1"] = conn
	svc.mu.Unlock()

	_, err := svc.OpenLocalModelRelay("user-2", "daemon-1", &reliantv1.LocalModelHTTPRequest{RequestId: "r"}, func(*reliantv1.LocalModelHTTPChunk) {})
	require.Error(t, err)
	_, err = svc.OpenLocalModelRelay("user-1", "daemon-missing", &reliantv1.LocalModelHTTPRequest{RequestId: "r"}, func(*reliantv1.LocalModelHTTPChunk) {})
	require.Error(t, err)
}

func TestDaemonRegistry_ListAndGetCarryStoredLocalModels(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	seedDaemon(t, repo, "user-1", "daemon-1", false) // offline: inventory outlives disconnect
	seedDaemon(t, repo, "user-1", "daemon-2", true)  // never published
	encoded, err := protojson.Marshal(sampleInventory())
	require.NoError(t, err)
	require.NoError(t, repo.SetDaemonLocalModels(context.Background(), "daemon-1", string(encoded)))

	svc := NewDaemonRegistryService(repo, &localModelRouter{})
	list, err := svc.ListDaemons(userCtx("user-1"), connect.NewRequest(&reliantv1.ListDaemonsRequest{}))
	require.NoError(t, err)
	byID := map[string]*reliantv1.DaemonInfo{}
	for _, d := range list.Msg.GetDaemons() {
		byID[d.GetDaemonId()] = d
	}
	require.Contains(t, byID, "daemon-1")
	assert.Equal(t, reliantv1.DaemonStatus_DAEMON_STATUS_DISCONNECTED, byID["daemon-1"].GetStatus())
	assert.Equal(t, "qwen3:latest", byID["daemon-1"].GetLocalModels().GetEndpoints()[0].GetModels()[0].GetName())
	assert.Nil(t, byID["daemon-2"].GetLocalModels(), "a daemon that never published has no inventory")

	got, err := svc.GetDaemon(userCtx("user-1"), connect.NewRequest(&reliantv1.GetDaemonRequest{DaemonId: "daemon-1"}))
	require.NoError(t, err)
	assert.Equal(t, "ollama", got.Msg.GetDaemon().GetLocalModels().GetEndpoints()[0].GetId())
}

func TestDaemonRegistry_RefreshLocalModels(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	seedDaemon(t, repo, "user-1", "online", true)
	seedDaemon(t, repo, "user-1", "offline", false)
	seedDaemon(t, repo, "user-2", "theirs", true)

	router := &localModelRouter{inventory: sampleInventory()}
	svc := NewDaemonRegistryService(repo, router)

	resp, err := svc.RefreshLocalModels(userCtx("user-1"), connect.NewRequest(&reliantv1.RefreshLocalModelsRequest{DaemonId: "online"}))
	require.NoError(t, err)
	assert.Equal(t, "ollama", resp.Msg.GetLocalModels().GetEndpoints()[0].GetId())

	_, err = svc.RefreshLocalModels(userCtx("user-1"), connect.NewRequest(&reliantv1.RefreshLocalModelsRequest{DaemonId: "offline"}))
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err), "offline daemon => Unavailable")

	_, err = svc.RefreshLocalModels(userCtx("user-1"), connect.NewRequest(&reliantv1.RefreshLocalModelsRequest{DaemonId: "theirs"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err), "another user's daemon is indistinguishable from absent")

	_, err = svc.RefreshLocalModels(context.Background(), connect.NewRequest(&reliantv1.RefreshLocalModelsRequest{DaemonId: "online"}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	_, err = svc.RefreshLocalModels(userCtx("user-1"), connect.NewRequest(&reliantv1.RefreshLocalModelsRequest{}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	router.refreshErr = &toolexec.LocalModelDaemonUnavailableError{DaemonID: "online", Reason: "no inventory within 10s"}
	_, err = svc.RefreshLocalModels(userCtx("user-1"), connect.NewRequest(&reliantv1.RefreshLocalModelsRequest{DaemonId: "online"}))
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "online")
}

func TestDaemonRegistry_SetLocalModelEndpoints(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	seedDaemon(t, repo, "user-1", "online", true)
	seedDaemon(t, repo, "user-1", "offline", false)
	seedDaemon(t, repo, "user-2", "theirs", true)

	router := &localModelRouter{inventory: sampleInventory()}
	svc := NewDaemonRegistryService(repo, router)

	resp, err := svc.SetLocalModelEndpoints(userCtx("user-1"), connect.NewRequest(&reliantv1.SetLocalModelEndpointsRequest{
		DaemonId: "online", BaseUrls: []string{"http://gpu-box.lan:8000/v1"},
	}))
	require.NoError(t, err)
	assert.Equal(t, "ollama", resp.Msg.GetLocalModels().GetEndpoints()[0].GetId(), "returns the re-probed inventory")

	require.Len(t, router.commands, 1)
	cmd := router.commands[0]
	assert.Equal(t, "user-1", cmd.userID)
	assert.Equal(t, "online", cmd.daemonID, "the command is pinned to the named daemon")
	assert.Equal(t, "localmodels.set_endpoints", cmd.commandType)
	assert.JSONEq(t, `{"base_urls":["http://gpu-box.lan:8000/v1"]}`, string(cmd.payload))

	// Empty clears: the payload carries an empty array, not null.
	_, err = svc.SetLocalModelEndpoints(userCtx("user-1"), connect.NewRequest(&reliantv1.SetLocalModelEndpointsRequest{DaemonId: "online"}))
	require.NoError(t, err)
	assert.JSONEq(t, `{"base_urls":[]}`, string(router.commands[1].payload))

	before := len(router.commands)
	for _, id := range []string{"offline", "theirs"} {
		_, err = svc.SetLocalModelEndpoints(userCtx("user-1"), connect.NewRequest(&reliantv1.SetLocalModelEndpointsRequest{DaemonId: id}))
		require.Error(t, err, id)
	}
	assert.Equal(t, before, len(router.commands), "no command may reach an offline or foreign daemon")

	var ce *connect.Error
	_, err = svc.SetLocalModelEndpoints(userCtx("user-1"), connect.NewRequest(&reliantv1.SetLocalModelEndpointsRequest{DaemonId: "offline"}))
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, connect.CodeUnavailable, ce.Code())
}
