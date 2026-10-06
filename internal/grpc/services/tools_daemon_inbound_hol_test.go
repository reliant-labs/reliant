// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"sync"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

// stallingRepo parks the database writes the gateway performs for a daemon's
// state messages until the test releases them — a slow DB round trip from the
// gateway pod, or a 10MB project-config upsert, held open on purpose.
type stallingRepo struct {
	db.Repository

	entered chan string   // receives the name of each stalled call as it starts
	release chan struct{} // closed to let every stalled call return

	mu          sync.Mutex
	userUpdates []*db.UserUpdate
}

func newStallingRepo() *stallingRepo {
	return &stallingRepo{entered: make(chan string, 16), release: make(chan struct{})}
}

func (r *stallingRepo) stall(name string) {
	r.entered <- name
	<-r.release
}

func (r *stallingRepo) TouchDaemonAttachmentIfNewer(context.Context, string, time.Time) error {
	r.stall("TouchDaemonAttachmentIfNewer")
	return nil
}

func (r *stallingRepo) UpdateDaemonAttachmentPorts(context.Context, string, []uint32) error {
	return nil
}

func (r *stallingRepo) GetProjectByPathAndUser(context.Context, string, string) (*db.Project, error) {
	r.stall("GetProjectByPathAndUser")
	return nil, nil
}

func (r *stallingRepo) CreateUserUpdate(_ context.Context, u *db.UserUpdate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.userUpdates = append(r.userUpdates, u)
	return nil
}

func (r *stallingRepo) recordedUserUpdates() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.userUpdates)
}

// A daemon's reply must reach its waiter while the gateway is still busy
// persisting an unrelated message from the same daemon.
//
// Every message on a daemon's stream used to be handled inline, in arrival
// order, by one receive loop — including the ones that write to the database:
// the 15s heartbeat lease renewal, every FileSystemChanged an agent's edit
// produces, and project-config snapshots that reach 10MB for a multi-repo
// project (measured: 16 snapshots persisted back to back for ~1s on every
// reconnect). A DaemonCommandResponse arriving behind any of them waited for
// that write to finish, so a millisecond daemon command could take as long as
// the slowest unrelated DB write in front of it.
func TestInboundReplyIsNotQueuedBehindStatePersistence(t *testing.T) {
	stallers := map[string]*reliantv1.DaemonMessage{
		"heartbeat": {Message: &reliantv1.DaemonMessage_Heartbeat{
			Heartbeat: &reliantv1.DaemonHeartbeat{Timestamp: time.Now().UnixMilli()},
		}},
		"filesystem changed": {Message: &reliantv1.DaemonMessage_FileSystemChanged{
			FileSystemChanged: &reliantv1.FileSystemChanged{ProjectPath: "/work/project"},
		}},
	}

	for name, staller := range stallers {
		t.Run(name, func(t *testing.T) {
			repo := newStallingRepo()
			svc := NewToolsDaemonService(repo)
			stream := newParkedStream()
			conn := newTestConn("user-1", "daemon-1", stream)

			respCh := make(chan *reliantv1.DaemonCommandResponse, 1)
			conn.pendingCommands["req-fast"] = respCh

			ctx, cancel := context.WithCancel(context.Background())
			handlerDone := make(chan struct{})
			go func() {
				defer close(handlerDone)
				_ = svc.handleIncoming(ctx, conn)
			}()
			t.Cleanup(func() {
				close(repo.release)
				cancel()
				close(stream.recv)
				<-handlerDone
			})

			stream.recv <- staller
			select {
			case <-repo.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("the gateway never started persisting the state message")
			}

			// The DB write is now parked. The daemon answers a command.
			stream.recv <- &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_DaemonCommandResponse{
				DaemonCommandResponse: &reliantv1.DaemonCommandResponse{
					RequestId: "req-fast", CommandType: "project.code_presence", Success: true,
				},
			}}

			select {
			case resp := <-respCh:
				require.Equal(t, "req-fast", resp.GetRequestId())
			case <-time.After(time.Second):
				t.Fatal("the command reply waited behind an unrelated database write on the same " +
					"daemon stream: replies must be routed without waiting for state persistence")
			}
		})
	}
}

// Moving persistence off the receive loop must not lose it. A message the
// gateway received before the stream ended is still applied — a
// DaemonCommandFailed is the only outcome signal a fire-and-forget clone ever
// produces, so dropping it would leave the user with no trace of the failure.
func TestInboundStateReceivedBeforeStreamEndIsStillApplied(t *testing.T) {
	repo := newStallingRepo()
	svc := NewToolsDaemonService(repo)
	stream := newParkedStream()
	conn := newTestConn("user-1", "daemon-1", stream)

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		_ = svc.handleIncoming(context.Background(), conn)
	}()

	// Park the state worker on a slow write, queue a failure announcement
	// behind it, then end the stream.
	stream.recv <- &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_FileSystemChanged{
		FileSystemChanged: &reliantv1.FileSystemChanged{ProjectPath: "/work/project"},
	}}
	<-repo.entered
	stream.recv <- &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_DaemonCommandFailed{
		DaemonCommandFailed: &reliantv1.DaemonCommandFailed{
			RequestId: "clone-1", CommandType: "git.pull", ErrorMessage: "remote hung up",
		},
	}}
	close(stream.recv)
	<-handlerDone

	close(repo.release)
	require.Eventually(t, func() bool { return repo.recordedUserUpdates() == 1 }, 2*time.Second, 10*time.Millisecond,
		"a DaemonCommandFailed received before the stream ended must still be persisted")
}
