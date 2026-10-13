// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
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
	t *testing.T, svc *Service, userID, daemonID string, nameByPath map[string]string,
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

// TestSettledClone_LearnsTheForgeNameWithoutTheForgeTab is the owner's exact
// complaint, at the layer it actually happens: the daemon announces the clone
// landed, and the project must come out of that knowing its forge name — with
// nobody having opened the Forge tab.
func TestSettledClone_LearnsTheForgeNameWithoutTheForgeTab(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := newTestService(repo)
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
