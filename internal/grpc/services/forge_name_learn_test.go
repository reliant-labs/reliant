// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// A FRESHLY CLONED FORGE PROJECT MUST KNOW ITS OWN FORGE NAME.
//
// projects.forge_project_name is how the web joins a project to its
// control-plane deploy environments, and it must be readable with the daemon
// OFFLINE. Before this, the only writer ran when a forge TOPOLOGY RPC
// succeeded — so a just-cloned project reported "this project's forge name has
// not been read from its forge.yaml by a daemon yet" until somebody opened the
// Forge tab while a daemon happened to be up. The daemon that did the clone had
// the file in its hands the whole time.

// fakeForgeNameDaemon answers forge.project_name for one path, and records
// what it was asked, so a test can assert the read was aimed at the right
// daemon and the right directory.
type fakeForgeNameDaemon struct {
	mu sync.Mutex
	// nameByPath is the forge.yaml `name` each path reports. A path absent
	// from the map is a non-forge repo.
	nameByPath map[string]string
	// err, when set, is returned instead of an answer — a daemon that
	// dropped between the clone and the read.
	err error

	gotDaemonIDs   []string
	gotCommands    []string
	gotPaths       []string
	gotTimeoutMsgs []int32
}

func (f *fakeForgeNameDaemon) SendDaemonCommandToDaemon(
	_ context.Context, _, daemonID, commandType string, payload []byte, timeoutMs int32,
) ([]byte, error) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.gotDaemonIDs = append(f.gotDaemonIDs, daemonID)
	f.gotCommands = append(f.gotCommands, commandType)
	f.gotPaths = append(f.gotPaths, req.Path)
	f.gotTimeoutMsgs = append(f.gotTimeoutMsgs, timeoutMs)
	name, isForge := f.nameByPath[req.Path]
	err := f.err
	f.mu.Unlock()

	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"has_forge":          isForge,
		"forge_project_name": name,
	})
}

func (f *fakeForgeNameDaemon) calls() (daemons, commands, paths []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.gotDaemonIDs...),
		append([]string(nil), f.gotCommands...),
		append([]string(nil), f.gotPaths...)
}

// forgeNameTestWait / forgeNameTestTick bound the wait for the off-loop read.
// It is an in-process stub answering immediately, so this is slack for a busy
// CI box rather than a real latency budget.
const (
	forgeNameTestWait = 5 * time.Second
	forgeNameTestTick = 10 * time.Millisecond
)

// newStubbedForgeNameConn returns a daemonConnection that answers
// forge.project_name the way a real daemon would.
//
// The production code reaches the daemon by writing a ServerMessage to
// conn.sendCh and waiting for the matching response to be delivered into
// conn.pendingCommands — normally by the receive loop. This stands in for both
// halves: it drains sendCh and replies on the registered channel, which lets
// the real handler run end to end with no daemon stream.
func newStubbedForgeNameConn(
	t *testing.T, svc *ToolsDaemonService, userID, daemonID string, nameByPath map[string]string,
) *daemonConnection {
	t.Helper()
	conn := &daemonConnection{
		userID:          userID,
		daemonID:        daemonID,
		sendCh:          make(chan *reliantv1.ServerMessage, 8),
		done:            make(chan struct{}),
		pendingCommands: make(map[string]chan *reliantv1.DaemonCommandResponse),
	}
	t.Cleanup(func() { close(conn.done) })

	go func() {
		for {
			select {
			case <-conn.done:
				return
			case msg := <-conn.sendCh:
				cmd := msg.GetDaemonCommand()
				if cmd == nil {
					continue
				}
				var req struct {
					Path string `json:"path"`
				}
				_ = json.Unmarshal(cmd.GetPayload(), &req)
				name, isForge := nameByPath[req.Path]
				payload, err := json.Marshal(map[string]any{
					"has_forge":          isForge,
					"forge_project_name": name,
				})
				if err != nil {
					continue
				}

				conn.pendingCommandsMu.Lock()
				ch, ok := conn.pendingCommands[cmd.GetRequestId()]
				if ok {
					delete(conn.pendingCommands, cmd.GetRequestId())
				}
				conn.pendingCommandsMu.Unlock()
				if ok {
					ch <- &reliantv1.DaemonCommandResponse{
						RequestId:   cmd.GetRequestId(),
						CommandType: cmd.GetCommandType(),
						Success:     true,
						Payload:     payload,
					}
				}
			}
		}
	}()
	return conn
}

// seedProjectForForgeName creates a project with no forge name recorded —
// exactly the state a clone leaves behind.
func seedProjectForForgeName(t *testing.T, repo db.Repository, userID string) *db.Project {
	t.Helper()
	project := &db.Project{
		ID:        uuid.NewString(),
		Name:      "widgets-" + uuid.NewString(),
		Path:      "/home/workspace/projects/widgets-" + uuid.NewString(),
		UserID:    userID,
		IsGitRepo: true,
	}
	require.NoError(t, repo.CreateProject(context.Background(), project))

	stored, err := repo.GetProjectWithUserCheck(context.Background(), project.ID, userID)
	require.NoError(t, err)
	require.Nil(t, stored.ForgeProjectName, "precondition: the clone left no forge name")
	require.False(t, stored.IsForge, "precondition: the row is not marked a forge project yet")
	return project
}

func TestLearnForgeProjectName_PersistsTheNameFromForgeYAML(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-forgename-" + uuid.NewString()
	project := seedProjectForForgeName(t, repo, userID)
	daemonID := uuid.NewString()
	daemon := &fakeForgeNameDaemon{nameByPath: map[string]string{project.Path: "barksocial"}}

	got := learnForgeProjectName(context.Background(), daemon, repo,
		userID, daemonID, project.ID, project.Path)
	assert.Equal(t, "barksocial", got)

	stored, err := repo.GetProjectWithUserCheck(context.Background(), project.ID, userID)
	require.NoError(t, err)
	require.NotNil(t, stored.ForgeProjectName,
		"the daemon that cloned the repo read forge.yaml; the project must not still say the name was never read")
	assert.Equal(t, "barksocial", *stored.ForgeProjectName)
	assert.True(t, stored.IsForge, "a project with a forge.yaml is a forge project")
}

func TestLearnForgeProjectName_AsksTheDaemonHoldingTheCheckout(t *testing.T) {
	// A user with two machines has the clone on exactly one of them. Reading
	// forge.yaml off the default daemon would read the wrong disk.
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-forgename-" + uuid.NewString()
	project := seedProjectForForgeName(t, repo, userID)
	clonedOnto := uuid.NewString()
	daemon := &fakeForgeNameDaemon{nameByPath: map[string]string{project.Path: "barksocial"}}

	learnForgeProjectName(context.Background(), daemon, repo,
		userID, clonedOnto, project.ID, project.Path)

	daemons, commands, paths := daemon.calls()
	require.Equal(t, []string{clonedOnto}, daemons,
		"the read must target the daemon the project was cloned onto")
	assert.Equal(t, []string{"forge.project_name"}, commands)
	assert.Equal(t, []string{project.Path}, paths)
}

func TestLearnForgeProjectName_LeavesANonForgeRepoUnmarked(t *testing.T) {
	// SetProjectForgeName also sets is_forge, so writing "" for a plain repo
	// would label every ordinary project a forge project.
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-forgename-" + uuid.NewString()
	project := seedProjectForForgeName(t, repo, userID)
	daemon := &fakeForgeNameDaemon{nameByPath: map[string]string{}} // no forge.yaml

	assert.Empty(t, learnForgeProjectName(context.Background(), daemon, repo,
		userID, uuid.NewString(), project.ID, project.Path))

	stored, err := repo.GetProjectWithUserCheck(context.Background(), project.ID, userID)
	require.NoError(t, err)
	assert.Nil(t, stored.ForgeProjectName, "a repo with no forge.yaml must record no forge name")
	assert.False(t, stored.IsForge, "a plain git repo must not be marked a forge project")
}

func TestLearnForgeProjectName_ADroppedDaemonDefersRatherThanCorrupts(t *testing.T) {
	// The clone already succeeded. A daemon that disconnects before the read
	// costs a deferred label (the topology path still backfills), never a
	// wrong one and never the install.
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "user-forgename-" + uuid.NewString()
	project := seedProjectForForgeName(t, repo, userID)
	daemon := &fakeForgeNameDaemon{err: fmt.Errorf("daemon disconnected")}

	assert.Empty(t, learnForgeProjectName(context.Background(), daemon, repo,
		userID, uuid.NewString(), project.ID, project.Path))

	stored, err := repo.GetProjectWithUserCheck(context.Background(), project.ID, userID)
	require.NoError(t, err)
	assert.Nil(t, stored.ForgeProjectName)
	assert.False(t, stored.IsForge)
}

// TestSettledClone_LearnsTheForgeNameWithoutTheForgeTab is the owner's exact
// complaint, at the layer it actually happens: the daemon announces the clone
// landed, and the project must come out of that knowing its forge name — with
// nobody having opened the Forge tab.
func TestSettledClone_LearnsTheForgeNameWithoutTheForgeTab(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewToolsDaemonService(repo)
	defer svc.Close()

	userID := "user-forgename-" + uuid.NewString()
	daemonID := uuid.NewString()
	project := seedInstallingProject(t, repo, userID, daemonID, "clone:req:"+uuid.NewString())

	// A daemon connection whose command responses are served by a stub, so
	// the test exercises the real handler without a live daemon stream.
	conn := newStubbedForgeNameConn(t, svc, userID, daemonID,
		map[string]string{project.Path: "barksocial"})

	require.NoError(t, svc.handleFileSystemChanged(context.Background(), conn,
		&reliantv1.FileSystemChanged{ProjectPath: project.Path}))

	// The read is dispatched off the receive loop on purpose (waiting on it
	// there would deadlock), so settle for eventual consistency.
	require.Eventually(t, func() bool {
		stored, err := repo.GetProjectWithUserCheck(context.Background(), project.ID, userID)
		return err == nil && stored.ForgeProjectName != nil && *stored.ForgeProjectName == "barksocial"
	}, forgeNameTestWait, forgeNameTestTick,
		"a settled clone must leave the project knowing its forge name, with no Forge tab opened")
}
