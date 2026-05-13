package services

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

func TestSweepStaleDaemonsMarksDisconnectedAndClosesConnection(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	now := time.Now().UTC().Truncate(time.Second)
	staleHeartbeat := now.Add(-10 * time.Minute)
	freshHeartbeat := now.Add(-30 * time.Second)
	connectedAt := now.Add(-20 * time.Minute)

	staleDaemonID := uuid.New().String()
	freshDaemonID := uuid.New().String()

	require.NoError(t, repo.UpsertDaemon(context.Background(), &db.Daemon{
		ID:            staleDaemonID,
		UserID:        "test-user",
		Status:        db.DaemonStatusActive,
		ConnectedAt:   &connectedAt,
		LastHeartbeat: &staleHeartbeat,
	}))
	require.NoError(t, repo.UpsertDaemon(context.Background(), &db.Daemon{
		ID:            freshDaemonID,
		UserID:        "test-user",
		Status:        db.DaemonStatusActive,
		ConnectedAt:   &connectedAt,
		LastHeartbeat: &freshHeartbeat,
	}))

	staleConn := &daemonConnection{userID: "test-user", daemonID: staleDaemonID, done: make(chan struct{})}
	freshConn := &daemonConnection{userID: "fresh-user", daemonID: freshDaemonID, done: make(chan struct{})}

	svc.mu.Lock()
	registerTestConn(svc, staleConn)
	registerTestConn(svc, freshConn)
	svc.mu.Unlock()

	require.NoError(t, svc.sweepStaleDaemons(context.Background(), now))

	staleReloaded, err := repo.GetDaemon(context.Background(), staleDaemonID)
	require.NoError(t, err)
	require.Equal(t, db.DaemonStatusDisconnected, staleReloaded.Status)
	require.NotNil(t, staleReloaded.DisconnectedAt)

	freshReloaded, err := repo.GetDaemon(context.Background(), freshDaemonID)
	require.NoError(t, err)
	require.Equal(t, db.DaemonStatusActive, freshReloaded.Status)

	svc.mu.RLock()
	_, staleStillPresent := svc.connections[staleConn.daemonID]
	_, freshStillPresent := svc.connections[freshConn.daemonID]
	svc.mu.RUnlock()
	require.False(t, staleStillPresent)
	require.True(t, freshStillPresent)

	select {
	case <-staleConn.done:
		// expected
	default:
		t.Fatal("expected stale daemon connection to be closed")
	}

	select {
	case <-freshConn.done:
		t.Fatal("fresh daemon connection should remain open")
	default:
	}
}

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

func TestHandleProjectDiscoveryCreatesProjectAndRequestsConfig(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	now := time.Now().UTC()
	daemonID := uuid.New().String()
	require.NoError(t, repo.UpsertDaemon(context.Background(), &db.Daemon{
		ID:            daemonID,
		UserID:        "test-user",
		Status:        db.DaemonStatusActive,
		ConnectedAt:   &now,
		LastHeartbeat: &now,
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

	now := time.Now().UTC()
	daemonID := uuid.New().String()
	require.NoError(t, repo.UpsertDaemon(context.Background(), &db.Daemon{
		ID:            daemonID,
		UserID:        "test-user",
		Status:        db.DaemonStatusActive,
		ConnectedAt:   &now,
		LastHeartbeat: &now,
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

	record, err := repo.GetProjectConfigRecord(context.Background(), "test-project")
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
		ID:            daemonID,
		UserID:        "test-user",
		Status:        db.DaemonStatusActive,
		ConnectedAt:   &now,
		LastHeartbeat: &now,
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
		ID:            daemonID,
		UserID:        userID,
		Status:        db.DaemonStatusActive,
		ConnectedAt:   &now,
		LastHeartbeat: &now,
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

	// 2) Disconnect via stale sweep, then reconnect.
	staleHeartbeat := now.Add(-10 * time.Minute)
	require.NoError(t, repo.UpdateDaemonStatus(ctx, daemonID, db.DaemonStatusActive, nil, &staleHeartbeat, nil))
	require.NoError(t, svc.sweepStaleDaemons(ctx, now))

	select {
	case <-conn1.done:
		// expected
	default:
		t.Fatal("expected stale sweep to close first connection")
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
			ID:            daemonID,
			UserID:        userID,
			Status:        db.DaemonStatusActive,
			ConnectedAt:   &now,
			LastHeartbeat: &now,
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
