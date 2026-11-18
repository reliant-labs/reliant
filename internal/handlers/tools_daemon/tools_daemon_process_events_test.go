// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

func recordingRepo() (*MockRepository, *[]*db.UserUpdate) {
	var updates []*db.UserUpdate
	return &MockRepository{
		CreateUserUpdateFunc: func(_ context.Context, u *db.UserUpdate) error {
			updates = append(updates, u)
			return nil
		},
	}, &updates
}

// A process an agent started reaches its owner's clients as the update their
// process list handles, addressed from the stream — never from the event.
func TestProcessEvent_BecomesTheOwnersUserUpdate(t *testing.T) {
	repo, updates := recordingRepo()
	svc := newTestService(repo)
	defer svc.Close()
	conn := &daemonConnection{userID: "owner", daemonID: "machine-1", done: make(chan struct{})}

	require.NoError(t, svc.handleProcessEvent(context.Background(), conn, &reliantv1.ProcessLifecycleEvent{
		Type:            "started",
		ProcessId:       "proc-1",
		Command:         "npm run dev",
		WorkingDir:      "/w",
		Status:          "running",
		StartTimeUnixMs: 1_763_000_000_000,
		ChatId:          "chat-1",
		ThreadId:        "thread-2",
		KeepAlive:       true,
		Ports:           []*reliantv1.ProcessLifecyclePort{{Port: 5173, Protocol: "tcp"}},
	}))

	require.Len(t, *updates, 1)
	u := (*updates)[0]
	assert.Equal(t, "owner", u.UserID)
	assert.Equal(t, db.UserUpdateProcessStarted, u.UpdateType)
	assert.Equal(t, db.EntityTypeBackgroundProcess, u.EntityType)
	assert.Equal(t, "proc-1", u.EntityID)
	assert.Nil(t, u.ChatID, "a daemon-named chat stays out of the row's foreign keys")
	assert.Nil(t, u.WorktreeID)

	var data map[string]any
	require.NoError(t, json.Unmarshal([]byte(u.Data), &data))
	assert.Equal(t, "chat-1", data["chat_id"])
	assert.Equal(t, "thread-2", data["thread_id"])
	assert.Equal(t, "machine-1", data["daemon_id"], "the machine is the stream's")
	assert.Equal(t, "2025-11-13T02:13:20Z", data["start_time"])
	assert.Equal(t, true, data["keep_alive"])
	assert.Len(t, data["ports"], 1)
}

// A kill is a FAILED update carrying status "killed": the web tells the two
// apart by status. An end carries its exit code and end time.
func TestProcessEvent_EndsMapToTheWebsUpdateTypes(t *testing.T) {
	repo, updates := recordingRepo()
	svc := newTestService(repo)
	defer svc.Close()
	conn := &daemonConnection{userID: "owner", daemonID: "m", done: make(chan struct{})}

	for _, ev := range []*reliantv1.ProcessLifecycleEvent{
		{Type: "completed", ProcessId: "a", Status: "completed", ExitCode: proto.Int32(0), EndTimeUnixMs: proto.Int64(1_763_000_000_000)},
		{Type: "failed", ProcessId: "b", Status: "failed", ExitCode: proto.Int32(2)},
		{Type: "killed", ProcessId: "c", Status: "killed"},
		{Type: "something-newer", ProcessId: "d"},
		{Type: "started"}, // no process id
	} {
		require.NoError(t, svc.handleProcessEvent(context.Background(), conn, ev))
	}

	require.Len(t, *updates, 3, "an unknown type or a nameless process writes nothing")
	assert.Equal(t, db.UserUpdateProcessCompleted, (*updates)[0].UpdateType)
	assert.Equal(t, db.UserUpdateProcessFailed, (*updates)[1].UpdateType)
	assert.Equal(t, db.UserUpdateProcessFailed, (*updates)[2].UpdateType)

	var completed, killed map[string]any
	require.NoError(t, json.Unmarshal([]byte((*updates)[0].Data), &completed))
	require.NoError(t, json.Unmarshal([]byte((*updates)[2].Data), &killed))
	assert.EqualValues(t, 0, completed["exit_code"])
	assert.Equal(t, "2025-11-13T02:13:20Z", completed["end_time"])
	assert.Equal(t, "killed", killed["status"])
}

// Every registration ack says the gateway handles process events, so a daemon
// knows it may send them.
func TestGatewayCapabilities_ListProcessEvents(t *testing.T) {
	assert.Contains(t, GatewayCapabilities(), "process_events")
}
