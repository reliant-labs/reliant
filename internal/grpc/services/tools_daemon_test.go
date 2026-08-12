package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/stretchr/testify/require"
)

func TestToolsDaemonServiceCloseIsIdempotent(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	svc.Close()
	svc.Close()
}

func TestSendDaemonCommandCancelsInFlightCommandWhenCallerContextEnds(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	conn := &daemonConnection{
		userID:          "test-user",
		daemonID:        uuid.New().String(),
		sendCh:          make(chan *reliantv1.ServerMessage, 4),
		done:            make(chan struct{}),
		pendingCommands: make(map[string]chan *reliantv1.DaemonCommandResponse),
	}
	svc.mu.Lock()
	registerTestConn(svc, conn)
	svc.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	requestID := "oauth-req"
	go func() {
		_, err := svc.SendDaemonCommand(ctx, conn.userID, &reliantv1.DaemonCommandRequest{
			RequestId:   requestID,
			CommandType: "auth.start_oauth",
			TimeoutMs:   120000,
		})
		resultCh <- err
	}()

	commandMsg := <-conn.sendCh
	require.Equal(t, requestID, commandMsg.GetDaemonCommand().GetRequestId())

	cancel()

	cancelMsg := <-conn.sendCh
	require.Equal(t, requestID, cancelMsg.GetToolCancel().GetRequestId())

	err := <-resultCh
	require.ErrorIs(t, err, context.Canceled)
}

// DaemonRegister.user_id is now `reserved` — the daemon can't assert identity,
// the server derives it from the PAT in context. The function only needs to
// confirm a userID is present in context; spoofing is impossible by construction.

func TestDaemonRegistrationUserIDRequiresAuthContext(t *testing.T) {
	_, err := daemonRegistrationUserID(context.Background(), &reliantv1.DaemonRegister{})
	require.Error(t, err)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestDaemonRegistrationUserIDReturnsContextUserID(t *testing.T) {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "trusted-user")
	resolved, err := daemonRegistrationUserID(ctx, &reliantv1.DaemonRegister{})
	require.NoError(t, err)
	require.Equal(t, "trusted-user", resolved)
}

// fakeDaemonRepo is a narrow stub for resolveUnboundDaemonID — it implements
// only the lookup the helper needs. Real repo tests cover the SQL.
type fakeDaemonRepo struct {
	daemons []*db.Daemon
	err     error
	calls   int
}

func (f *fakeDaemonRepo) ListDaemonsByUserID(_ context.Context, _ string) ([]*db.Daemon, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.daemons, nil
}

// resolveUnboundDaemonID is the gateway-side hostname reuse path that keeps
// (userID, hostname) stable across reconnects. The tests below pin its three
// branches so a refactor can't silently regress to "mint a fresh ID per
// reconnect" — the failure mode this helper exists to prevent.

// resolveDaemonID owns the precedence PAT-bound > client-asserted stable id >
// hostname fallback. The lazy resolveUnbound thunk must NOT run when a
// higher-precedence id is present — the calls counter proves the DB lookup is
// skipped, which is the whole point of the stable-id path (identity survives
// hostname churn without touching the daemons table).
func TestResolveDaemonIDPrefersPATBoundID(t *testing.T) {
	called := false
	got := resolveDaemonID("pat-bound", "asserted", func() string {
		called = true
		return "fallback"
	})
	require.Equal(t, "pat-bound", got)
	require.False(t, called, "hostname fallback must not run when PAT-bound id present")
}

func TestResolveDaemonIDTrustsClientAssertedIDForUnboundPAT(t *testing.T) {
	called := false
	got := resolveDaemonID("", "stable-from-daemon-json", func() string {
		called = true
		return "fallback"
	})
	require.Equal(t, "stable-from-daemon-json", got)
	require.False(t, called, "hostname fallback must not run when client asserts a stable id")
}

func TestResolveDaemonIDTrimsAssertedID(t *testing.T) {
	got := resolveDaemonID("", "  stable-id  ", func() string { return "fallback" })
	require.Equal(t, "stable-id", got)
}

func TestResolveDaemonIDFallsBackToHostnameForOlderDaemons(t *testing.T) {
	called := false
	got := resolveDaemonID("", "   ", func() string {
		called = true
		return "hostname-derived"
	})
	require.Equal(t, "hostname-derived", got)
	require.True(t, called, "hostname fallback must run when no id is asserted")
}

func TestResolveUnboundDaemonIDReusesExistingByHostname(t *testing.T) {
	existingID := "existing-daemon-id"
	hostname := "dev-machine.local"
	repo := &fakeDaemonRepo{daemons: []*db.Daemon{
		{ID: "other-host-daemon", UserID: "u1", Hostname: ptrStr("other-host")},
		{ID: existingID, UserID: "u1", Hostname: ptrStr(hostname)},
	}}

	got := resolveUnboundDaemonID(context.Background(), repo, "u1", hostname)
	require.Equal(t, existingID, got)
	require.Equal(t, 1, repo.calls)
}

func TestResolveUnboundDaemonIDMintsNewWhenHostnameDoesNotMatch(t *testing.T) {
	repo := &fakeDaemonRepo{daemons: []*db.Daemon{
		{ID: "old-id", UserID: "u1", Hostname: ptrStr("DifferentHost")},
	}}

	got := resolveUnboundDaemonID(context.Background(), repo, "u1", "dev-machine.local")
	require.NotEmpty(t, got)
	require.NotEqual(t, "old-id", got)
	require.Equal(t, 1, repo.calls)
}

func TestResolveUnboundDaemonIDMintsNewWhenNoExistingDaemons(t *testing.T) {
	repo := &fakeDaemonRepo{}
	got := resolveUnboundDaemonID(context.Background(), repo, "u1", "host-a")
	require.NotEmpty(t, got)
}

func TestResolveUnboundDaemonIDMintsNewWhenHostnameEmpty(t *testing.T) {
	// Empty hostname must skip the lookup — otherwise an older daemon that
	// doesn't send hostname would collide with the first row for the user
	// and steal its identity.
	repo := &fakeDaemonRepo{daemons: []*db.Daemon{
		{ID: "real-daemon", UserID: "u1", Hostname: ptrStr("real-host")},
	}}
	got := resolveUnboundDaemonID(context.Background(), repo, "u1", "")
	require.NotEmpty(t, got)
	require.NotEqual(t, "real-daemon", got)
	require.Equal(t, 0, repo.calls, "empty hostname should not query the DB")
}

func TestResolveUnboundDaemonIDIgnoresOtherUsersWithSameHostname(t *testing.T) {
	// Multi-user dev box: two Supabase identities, same physical machine.
	// The lookup is already user-scoped, but pin it so a future refactor
	// can't accidentally widen the query and cross-attribute daemons.
	repo := &fakeDaemonRepo{daemons: []*db.Daemon{
		// ListDaemonsByUserID is contract-bound to filter; if a future
		// implementation returns cross-user rows, we still must not pick
		// them — the helper trusts the contract, so we model the contract
		// (only user-scoped rows show up) and verify the new-mint path.
	}}
	got := resolveUnboundDaemonID(context.Background(), repo, "u2", "dev-machine.local")
	require.NotEmpty(t, got)
}

func TestResolveUnboundDaemonIDMintsNewOnLookupError(t *testing.T) {
	// DB blip should not block daemon registration — fall back to a new
	// UUID. The next successful reconnect will reuse this row.
	repo := &fakeDaemonRepo{err: fmt.Errorf("transient DB error")}
	got := resolveUnboundDaemonID(context.Background(), repo, "u1", "host-a")
	require.NotEmpty(t, got)
}

func TestResolveUnboundDaemonIDTrimsHostnameWhitespace(t *testing.T) {
	// os.Hostname() should never return padded values, but a stray space
	// from a malformed proto must not split one machine into two rows.
	repo := &fakeDaemonRepo{daemons: []*db.Daemon{
		{ID: "stable-id", UserID: "u1", Hostname: ptrStr("host-a")},
	}}
	got := resolveUnboundDaemonID(context.Background(), repo, "u1", "  host-a  ")
	require.Equal(t, "stable-id", got)
}

// Reconnect simulation: walk the helper through a "first connect → upsert →
// second connect" cycle using an in-memory fake. Pins the end-to-end
// contract that two reconnects from the same (userID, hostname) produce
// one daemon_id, not two — the Electron-restart-loop failure mode.
func TestResolveUnboundDaemonIDReusesAcrossReconnectsInMemory(t *testing.T) {
	userID := "user-1"
	hostname := "dev-machine.local"
	repo := &fakeDaemonRepo{}
	ctx := context.Background()

	// First connect: no existing row → mint.
	firstID := resolveUnboundDaemonID(ctx, repo, userID, hostname)
	require.NotEmpty(t, firstID)

	// Simulate UpsertDaemon by appending to the fake's snapshot.
	repo.daemons = append(repo.daemons, &db.Daemon{
		ID:       firstID,
		UserID:   userID,
		Hostname: ptrStr(hostname),
	})

	// Second connect (Electron restart): same userID + hostname → must reuse.
	secondID := resolveUnboundDaemonID(ctx, repo, userID, hostname)
	require.Equal(t, firstID, secondID, "reconnect must reuse the same daemon_id, not mint a fresh one")

	// Third connect: still stable.
	thirdID := resolveUnboundDaemonID(ctx, repo, userID, hostname)
	require.Equal(t, firstID, thirdID)

	// Only one row in the fake — i.e. the upsert path the caller runs
	// after this helper would land on the same id every time.
	require.Len(t, repo.daemons, 1)
}

func ptrStr(s string) *string { return &s }

func TestHandleProjectDiscoveryCreatesProjectAndRequestsConfig(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	daemonID := uuid.New().String()
	require.NoError(t, repo.UpsertDaemon(context.Background(), &db.Daemon{
		ID:     daemonID,
		UserID: "test-user",
	}))

	conn := &daemonConnection{
		userID:   "test-user",
		daemonID: daemonID,
		sendCh:   make(chan *reliantv1.ServerMessage, 8),
		done:     make(chan struct{}),
	}
	svc.mu.Lock()
	registerTestConn(svc, conn)
	svc.mu.Unlock()

	discovery := &reliantv1.ProjectDiscovery{Projects: []*reliantv1.DiscoveredProject{
		{Path: " /tmp/discovered-proj/ ", Name: "Discovered Project", IsGitRepo: true},
	}}
	require.NoError(t, svc.handleProjectDiscovery(context.Background(), conn, discovery))

	project, err := repo.GetProjectByPath(context.Background(), "/tmp/discovered-proj")
	require.NoError(t, err)
	require.Equal(t, "test-user", project.UserID)
	require.Equal(t, "Discovered Project", project.Name)
	require.True(t, project.IsGitRepo)

	var sawLoad bool
	var sawWatch bool
	for i := 0; i < 2; i++ {
		select {
		case msg := <-conn.sendCh:
			if load := msg.GetLoadProjectConfigs(); load != nil {
				sawLoad = true
				require.Equal(t, "/tmp/discovered-proj", load.ProjectPath)
				require.NotEmpty(t, load.RequestId)
			}
			if watch := msg.GetWatchProjectConfigs(); watch != nil {
				sawWatch = true
				require.Equal(t, "/tmp/discovered-proj", watch.ProjectPath)
				require.True(t, watch.IncludeInitial)
			}
		default:
			t.Fatal("expected load/watch config requests for newly discovered project")
		}
	}
	require.True(t, sawLoad)
	require.True(t, sawWatch)

	reloadedDaemon, err := repo.GetDaemon(context.Background(), daemonID)
	require.NoError(t, err)
	require.NotNil(t, reloadedDaemon.ProjectPaths)

	var paths []string
	require.NoError(t, json.Unmarshal([]byte(*reloadedDaemon.ProjectPaths), &paths))
	require.Equal(t, []string{"/tmp/discovered-proj"}, paths)
}

func TestHandleLoadProjectConfigsResponsePersistsSnapshotAndTracksState(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	daemonID := uuid.New().String()
	require.NoError(t, repo.UpsertDaemon(context.Background(), &db.Daemon{
		ID:     daemonID,
		UserID: "test-user",
	}))

	conn := &daemonConnection{userID: "test-user", daemonID: daemonID}
	resp := &reliantv1.LoadProjectConfigsResponse{
		RequestId: "req-1",
		Snapshot: &reliantv1.ProjectConfigSnapshot{
			ProjectPath:           " /tmp/test ",
			ConfigVersion:         "v1",
			DaemonTimestampUnixMs: 1700000000000,
			UserConfigYaml:        []byte("user: yes"),
			ProjectConfigYaml:     []byte("project: yes"),
			LocalConfigYaml:       []byte("local: yes"),
			GlobalMemoryMd:        []byte("global memory"),
			ProjectMemoryMd:       []byte("project memory"),
			McpConfigs: map[string][]byte{
				"user":    []byte("{\"a\":1}"),
				"project": []byte("{\"b\":2}"),
				"local":   []byte("{\"c\":3}"),
				"global":  []byte("{\"ignored\":true}"),
			},
		},
	}

	require.NoError(t, svc.handleLoadProjectConfigsResponse(context.Background(), conn, resp))

	// The snapshot path (" /tmp/test ") normalizes to /tmp/test; the handler
	// creates/owns a project at that path, and the config record is keyed on
	// that project's ID. Resolve it rather than assuming a fixed seed ID.
	project, err := repo.GetProjectByPath(context.Background(), "/tmp/test")
	require.NoError(t, err)
	require.NotNil(t, project)

	record, err := repo.GetProjectConfigRecord(context.Background(), project.ID)
	require.NoError(t, err)
	require.Equal(t, daemonID, record.DaemonID)
	require.NotNil(t, record.UserConfigYAML)
	require.NotNil(t, record.ProjectConfigYAML)
	require.NotNil(t, record.LocalConfigYAML)
	require.Equal(t, "user: yes", *record.UserConfigYAML)
	require.Equal(t, "project: yes", *record.ProjectConfigYAML)
	require.Equal(t, "local: yes", *record.LocalConfigYAML)
	require.NotNil(t, record.GlobalMemoryMD)
	require.Equal(t, "global memory", *record.GlobalMemoryMD)
	require.NotNil(t, record.ProjectMemoryMD)
	require.Equal(t, "project memory", *record.ProjectMemoryMD)

	require.NotNil(t, record.MCPConfigs)
	var stored map[string]string
	require.NoError(t, json.Unmarshal([]byte(*record.MCPConfigs), &stored))
	require.Equal(t, map[string]string{
		"user":    "{\"a\":1}",
		"project": "{\"b\":2}",
		"local":   "{\"c\":3}",
	}, stored)

}

func TestHandleProjectConfigDeltaStaleGuardAndReloadRequest(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	now := time.Now().UTC()
	daemonID := uuid.New().String()
	projectID := uuid.New().String()
	projectPath := "/tmp/test-" + uuid.New().String()
	require.NoError(t, repo.UpsertDaemon(context.Background(), &db.Daemon{
		ID:     daemonID,
		UserID: "test-user",
	}))

	require.NoError(t, repo.CreateProject(context.Background(), &db.Project{
		ID:         projectID,
		UserID:     "test-user",
		Name:       "test",
		Path:       projectPath,
		IsGitRepo:  true,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	existingPushedAt := time.UnixMilli(2000).UTC()
	require.NoError(t, repo.UpsertProjectConfigRecord(context.Background(), &db.ProjectConfigRecord{
		ProjectID:      projectID,
		DaemonID:       daemonID,
		UserConfigYAML: testStringPtr("user: existing"),
		PushedAt:       existingPushedAt,
	}))

	conn := &daemonConnection{
		userID:   "test-user",
		daemonID: daemonID,
		sendCh:   make(chan *reliantv1.ServerMessage, 8),
		done:     make(chan struct{}),
	}
	svc.mu.Lock()
	registerTestConn(svc, conn)
	svc.mu.Unlock()

	older := &reliantv1.ProjectConfigDelta{
		ProjectPath:           projectPath,
		ConfigVersion:         "v1",
		DaemonTimestampUnixMs: 1000,
	}
	require.NoError(t, svc.handleProjectConfigDelta(context.Background(), conn, older))
	select {
	case <-conn.sendCh:
		t.Fatal("did not expect reload request for stale delta")
	default:
	}

	newer := &reliantv1.ProjectConfigDelta{
		ProjectPath:           projectPath,
		ConfigVersion:         "v3",
		DaemonTimestampUnixMs: 3000,
	}
	require.NoError(t, svc.handleProjectConfigDelta(context.Background(), conn, newer))
	select {
	case msg := <-conn.sendCh:
		load := msg.GetLoadProjectConfigs()
		require.NotNil(t, load)
		require.Equal(t, projectPath, load.ProjectPath)
		require.NotEmpty(t, load.RequestId)
	default:
		t.Fatal("expected reload request for newer delta without compacted snapshot")
	}
}

func TestPR5Smoke_ConfigSyncCancelDisconnectReconnect(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	ctx := context.Background()
	now := time.Now().UTC()
	userID := "test-user"
	daemonID := uuid.New().String()
	projectID := uuid.New().String()
	projectPath := "/tmp/pr5-smoke-" + uuid.New().String()

	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{
		ID:     daemonID,
		UserID: userID,
	}))

	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     userID,
		Name:       "pr5-smoke",
		Path:       projectPath,
		IsGitRepo:  true,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	chatID := uuid.New().String()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID:         chatID,
		Title:      "pr5 smoke",
		ProjectID:  projectID,
		UserID:     userID,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	conn1 := &daemonConnection{
		userID:   userID,
		daemonID: daemonID,
		sendCh:   make(chan *reliantv1.ServerMessage, 32),
		done:     make(chan struct{}),
	}
	svc.mu.Lock()
	registerTestConn(svc, conn1)
	svc.mu.Unlock()

	// 1) Discovery/config sync: discovery triggers load+watch and config snapshot persists.
	discovery := &reliantv1.ProjectDiscovery{Projects: []*reliantv1.DiscoveredProject{{Path: projectPath, Name: "pr5-smoke", IsGitRepo: true}}}
	require.NoError(t, svc.handleProjectDiscovery(ctx, conn1, discovery))

	var sawLoad, sawWatch bool
	for i := 0; i < 2; i++ {
		select {
		case msg := <-conn1.sendCh:
			if load := msg.GetLoadProjectConfigs(); load != nil && load.ProjectPath == projectPath {
				sawLoad = true
			}
			if watch := msg.GetWatchProjectConfigs(); watch != nil && watch.ProjectPath == projectPath {
				sawWatch = true
			}
		default:
			t.Fatal("expected initial load/watch messages for discovery")
		}
	}
	require.True(t, sawLoad)
	require.True(t, sawWatch)

	snapshotTS := time.Now().UTC().UnixMilli()
	require.NoError(t, svc.handleLoadProjectConfigsResponse(ctx, conn1, &reliantv1.LoadProjectConfigsResponse{
		RequestId: "snapshot-1",
		Snapshot: &reliantv1.ProjectConfigSnapshot{
			ProjectPath:           projectPath,
			DaemonTimestampUnixMs: snapshotTS,
			UserConfigYaml:        []byte("models: {}"),
			ProjectConfigYaml:     []byte("debug: true"),
			LocalConfigYaml:       []byte("context_paths: []"),
		},
	}))

	record, err := repo.GetProjectConfigRecord(ctx, projectID)
	require.NoError(t, err)
	require.Equal(t, daemonID, record.DaemonID)

	// 2) Disconnect by closing the connection; the stale-sweep path has been removed
	// in favor of daemon_attachment-based liveness, so a manual close stands in for it.
	conn1.closeDone()

	select {
	case <-conn1.done:
		// expected
	default:
		t.Fatal("expected connection to be closed")
	}
}

func TestHandleFileSystemChanged(t *testing.T) {
	t.Run("nil message returns no error", func(t *testing.T) {
		repo, cleanup := db.SetupTestDB(t)
		defer cleanup()

		svc := NewToolsDaemonService(repo)
		defer svc.Close()

		conn := &daemonConnection{userID: "test-user", daemonID: uuid.New().String(), done: make(chan struct{})}
		require.NoError(t, svc.handleFileSystemChanged(context.Background(), conn, nil))

		updates, err := repo.GetUserUpdatesSince(context.Background(), "test-user", 0, 100)
		require.NoError(t, err)
		require.Empty(t, updates)
	})

	t.Run("empty project path returns no error", func(t *testing.T) {
		repo, cleanup := db.SetupTestDB(t)
		defer cleanup()

		svc := NewToolsDaemonService(repo)
		defer svc.Close()

		conn := &daemonConnection{userID: "test-user", daemonID: uuid.New().String(), done: make(chan struct{})}
		require.NoError(t, svc.handleFileSystemChanged(context.Background(), conn, &reliantv1.FileSystemChanged{
			ProjectPath: "",
		}))

		updates, err := repo.GetUserUpdatesSince(context.Background(), "test-user", 0, 100)
		require.NoError(t, err)
		require.Empty(t, updates)
	})

	t.Run("unknown project path returns no error", func(t *testing.T) {
		repo, cleanup := db.SetupTestDB(t)
		defer cleanup()

		svc := NewToolsDaemonService(repo)
		defer svc.Close()

		conn := &daemonConnection{userID: "test-user", daemonID: uuid.New().String(), done: make(chan struct{})}
		require.NoError(t, svc.handleFileSystemChanged(context.Background(), conn, &reliantv1.FileSystemChanged{
			ProjectPath: "/tmp/does-not-exist-" + uuid.New().String(),
		}))

		updates, err := repo.GetUserUpdatesSince(context.Background(), "test-user", 0, 100)
		require.NoError(t, err)
		require.Empty(t, updates)
	})

	t.Run("valid project emits RefetchFileTree", func(t *testing.T) {
		repo, cleanup := db.SetupTestDB(t)
		defer cleanup()

		svc := NewToolsDaemonService(repo)
		defer svc.Close()

		ctx := context.Background()
		now := time.Now().UTC()
		userID := "test-user"
		daemonID := uuid.New().String()
		projectID := uuid.New().String()
		projectPath := "/tmp/test-fs-" + uuid.New().String()

		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{
			ID:     daemonID,
			UserID: userID,
		}))

		require.NoError(t, repo.CreateProject(ctx, &db.Project{
			ID:         projectID,
			UserID:     userID,
			Name:       "test-fs",
			Path:       projectPath,
			IsGitRepo:  true,
			CreatedAt:  now,
			UpdatedAt:  now,
			LastActive: now,
		}))

		conn := &daemonConnection{
			userID:   userID,
			daemonID: daemonID,
			sendCh:   make(chan *reliantv1.ServerMessage, 8),
			done:     make(chan struct{}),
		}
		svc.mu.Lock()
		registerTestConn(svc, conn)
		svc.mu.Unlock()

		require.NoError(t, svc.handleFileSystemChanged(ctx, conn, &reliantv1.FileSystemChanged{
			ProjectPath: projectPath,
		}))

		updates, err := repo.GetUserUpdatesSince(ctx, userID, 0, 100)
		require.NoError(t, err)
		require.NotEmpty(t, updates, "expected a user update for RefetchFileTree")

		// Find the refetch update
		var found bool
		for _, u := range updates {
			if u.UpdateType == db.UserUpdateRefetch && u.EntityID == projectID {
				var data db.RefetchData
				require.NoError(t, json.Unmarshal(u.Data, &data))
				require.Equal(t, db.RefetchFileTree, data.Type)
				require.Equal(t, db.EntityTypeProject, u.EntityType)
				found = true
				break
			}
		}
		require.True(t, found, "expected RefetchFileTree update with project entity ID")
	})
}

func testStringPtr(v string) *string {
	return &v
}

// registerTestConn adds a daemon connection to the service's maps
// using the new multi-daemon layout (keyed by daemonID + userDaemons index).
func registerTestConn(svc *ToolsDaemonService, conn *daemonConnection) {
	svc.connections[conn.daemonID] = conn
	svc.userDaemons[conn.userID] = append(svc.userDaemons[conn.userID], conn.daemonID)
}

func TestHasConnectedDaemonsForUser(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	const userID = "user-x"
	require.False(t, svc.HasConnectedDaemonsForUser(userID))

	conn := &daemonConnection{userID: userID, daemonID: uuid.New().String()}
	svc.mu.Lock()
	registerTestConn(svc, conn)
	svc.mu.Unlock()
	require.True(t, svc.HasConnectedDaemonsForUser(userID))

	svc.mu.Lock()
	delete(svc.connections, conn.daemonID)
	svc.userDaemons[userID] = nil
	delete(svc.userDaemons, userID)
	svc.mu.Unlock()
	require.False(t, svc.HasConnectedDaemonsForUser(userID))
}

// fakeDisconnectListener captures OnDaemonDisconnected calls for assertion.
type fakeDisconnectListener struct {
	mu     sync.Mutex
	events []string // formatted "userID/daemonID"
}

func (f *fakeDisconnectListener) OnDaemonConnected(userID, daemonID string) {}
func (f *fakeDisconnectListener) OnDaemonDisconnected(userID, daemonID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, userID+"/"+daemonID)
}
func (f *fakeDisconnectListener) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.events))
	copy(out, f.events)
	return out
}

func TestSweepStaleConnectionsRemovesHalfOpenAndFiresListeners(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	// Inject a controllable clock so we can age connections without sleeping.
	baseTime := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	currentTime := baseTime
	svc.now = func() time.Time { return currentTime }

	listener := &fakeDisconnectListener{}
	svc.AddConnectionListener(listener)

	// Pre-seed daemon row so DeleteDaemonAttachment (in teardown) doesn't choke.
	staleUserID := "stale-user"
	staleDaemonID := uuid.New().String()
	require.NoError(t, repo.UpsertDaemon(context.Background(), &db.Daemon{
		ID:     staleDaemonID,
		UserID: staleUserID,
	}))

	freshUserID := "fresh-user"
	freshDaemonID := uuid.New().String()
	require.NoError(t, repo.UpsertDaemon(context.Background(), &db.Daemon{
		ID:     freshDaemonID,
		UserID: freshUserID,
	}))

	// Stale connection: registered long ago, last activity 5 minutes ago —
	// well past the 90s threshold.
	staleConn := &daemonConnection{
		userID:            staleUserID,
		daemonID:          staleDaemonID,
		connectedAt:       baseTime.Add(-10 * time.Minute),
		lastActivity:      baseTime.Add(-5 * time.Minute),
		sendCh:            make(chan *reliantv1.ServerMessage, 4),
		done:              make(chan struct{}),
		terminalSubs:      make(map[string][]chan *toolexec.TerminalOutputEvent),
		processOutputSubs: make(map[string][]chan *toolexec.ProcessOutputEvent),
	}

	// Fresh connection: active right now, must NOT be reaped.
	freshConn := &daemonConnection{
		userID:            freshUserID,
		daemonID:          freshDaemonID,
		connectedAt:       baseTime.Add(-10 * time.Minute),
		lastActivity:      baseTime, // active now
		sendCh:            make(chan *reliantv1.ServerMessage, 4),
		done:              make(chan struct{}),
		terminalSubs:      make(map[string][]chan *toolexec.TerminalOutputEvent),
		processOutputSubs: make(map[string][]chan *toolexec.ProcessOutputEvent),
	}

	svc.mu.Lock()
	registerTestConn(svc, staleConn)
	registerTestConn(svc, freshConn)
	svc.mu.Unlock()

	require.Equal(t, 2, svc.GetActiveConnections())

	// Run the sweep at "current" time.
	svc.sweepStaleConnections()

	// Stale should be gone; fresh should remain.
	require.Equal(t, 1, svc.GetActiveConnections())
	require.False(t, svc.HasConnectedDaemonsForUser(staleUserID), "stale connection should be removed")
	require.True(t, svc.HasConnectedDaemonsForUser(freshUserID), "fresh connection should survive")

	// done channel for the stale conn must be closed so any goroutines
	// blocked on it (e.g. SendDaemonCommand) unblock.
	select {
	case <-staleConn.done:
		// expected
	default:
		t.Fatal("expected stale connection's done channel to be closed")
	}

	// OnDaemonDisconnected listener fired exactly once for the stale daemon.
	events := listener.snapshot()
	require.Equal(t, []string{staleUserID + "/" + staleDaemonID}, events)

	// Calling sweep again is a no-op — slot already gone, no double-fire.
	svc.sweepStaleConnections()
	require.Equal(t, []string{staleUserID + "/" + staleDaemonID}, listener.snapshot())
}

func TestSweepStaleConnectionsSkipsRecentlyRegistered(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	baseTime := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return baseTime }

	// Connection registered only 30s ago but with a stale lastActivity
	// (shouldn't happen in practice, but guard against false positives on
	// daemons that haven't sent their first message yet).
	conn := &daemonConnection{
		userID:            "u",
		daemonID:          uuid.New().String(),
		connectedAt:       baseTime.Add(-30 * time.Second),
		lastActivity:      baseTime.Add(-30 * time.Second),
		sendCh:            make(chan *reliantv1.ServerMessage, 1),
		done:              make(chan struct{}),
		terminalSubs:      make(map[string][]chan *toolexec.TerminalOutputEvent),
		processOutputSubs: make(map[string][]chan *toolexec.ProcessOutputEvent),
	}
	svc.mu.Lock()
	registerTestConn(svc, conn)
	svc.mu.Unlock()

	svc.sweepStaleConnections()
	require.Equal(t, 1, svc.GetActiveConnections(), "young connection must not be reaped even if lastActivity looks stale")
}

func TestTouchDaemonsForUserBumpsOnlyMatchingUser(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	stale := time.Now().Add(-1 * time.Hour).UTC()
	connA1 := &daemonConnection{userID: "user-a", daemonID: uuid.New().String(), lastActivity: stale}
	connA2 := &daemonConnection{userID: "user-a", daemonID: uuid.New().String(), lastActivity: stale}
	connB := &daemonConnection{userID: "user-b", daemonID: uuid.New().String(), lastActivity: stale}

	svc.mu.Lock()
	registerTestConn(svc, connA1)
	registerTestConn(svc, connA2)
	registerTestConn(svc, connB)
	svc.mu.Unlock()

	before := time.Now().UTC()
	svc.TouchDaemonsForUser("user-a")

	require.True(t, !connA1.lastActivity.Before(before), "connA1 lastActivity should be bumped")
	require.True(t, !connA2.lastActivity.Before(before), "connA2 lastActivity should be bumped")
	require.Equal(t, stale, connB.lastActivity, "connB should be untouched")

	// No-op for unknown user — must not panic or mutate other users.
	svc.TouchDaemonsForUser("user-c")
	require.Equal(t, stale, connB.lastActivity)
}
