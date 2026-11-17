// Copyright (c) 2025 Reliant Labs
package background

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

const testUser = "test-user" // testkit.AuthedContext's caller

// machineRouter answers exec.bg_list per machine and records where each
// command went ("" for default resolution).
type machineRouter struct {
	MockProcessRouter
	lists map[string][]*daemon.ProcessInfo
	sent  []string
}

func newMachineRouter(lists map[string][]*daemon.ProcessInfo) *machineRouter {
	r := &machineRouter{lists: lists}
	reply := func(daemonID, commandType string) ([]byte, error) {
		r.sent = append(r.sent, daemonID+" "+commandType)
		if commandType == "exec.bg_list" {
			return json.Marshal(r.lists[daemonID])
		}
		return json.Marshal(struct{}{})
	}
	r.SendDaemonCommandFunc = func(_ context.Context, _, commandType string, _ []byte, _ int32) ([]byte, error) {
		return reply("", commandType)
	}
	r.SendDaemonCommandToDaemonFunc = func(_ context.Context, _, daemonID, commandType string, _ []byte, _ int32) ([]byte, error) {
		return reply(daemonID, commandType)
	}
	return r
}

func chatRepo(chats map[string]*db.Chat, daemons map[string]*db.Daemon) *MockRepository {
	return &MockRepository{
		GetChatFunc: func(_ context.Context, id string) (*db.Chat, error) {
			if c, ok := chats[id]; ok {
				return c, nil
			}
			return nil, core.ErrChatNotFound
		},
		GetWorktreeFunc: func(context.Context, string) (*db.Worktree, error) {
			return nil, core.ErrWorktreeNotFound
		},
		GetDaemonFunc: func(_ context.Context, id string) (*db.Daemon, error) {
			if d, ok := daemons[id]; ok {
				return d, nil
			}
			return nil, svcerr.NotFound("daemon")
		},
	}
}

// A chat-scoped list is the processes that chat's agents started, read from
// the machine the chat runs on, with everything the daemon knows about each.
func TestListProcesses_ChatScopedReadsTheChatsMachine(t *testing.T) {
	started := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	stops := started.Add(4 * time.Hour)
	router := newMachineRouter(map[string][]*daemon.ProcessInfo{
		"machine-b": {
			{ID: "mine", Command: "npm run dev", Status: "running", StartTime: started, ChatID: "chat-1", ThreadID: "thread-9",
				WorkingDir: "/w", KeepAlive: true, StopsAfter: &stops, Ports: []daemon.PortInfo{{Port: 5173, Protocol: "tcp"}}},
			{ID: "other-chat", Command: "go test", Status: "running", StartTime: started, ChatID: "chat-2"},
			{ID: "panel", Command: "make", Status: "running", StartTime: started, PackageCommand: true},
		},
	})
	daemonB := "machine-b"
	repo := chatRepo(map[string]*db.Chat{
		"chat-1": {ID: "chat-1", UserID: testUser, ActiveDaemonID: &daemonB},
	}, nil)
	svc := NewTestBackground(t, WithBackgroundDeps(Deps{Router: router, Repo: repo}))

	resp, err := svc.ListProcesses(AuthedContext(t), connect.NewRequest(&reliantv1.ListBackgroundProcessesRequest{ChatId: proto.String("chat-1")}))
	require.NoError(t, err)

	assert.Equal(t, []string{"machine-b exec.bg_list"}, router.sent)
	require.Len(t, resp.Msg.Processes, 1)
	got := resp.Msg.Processes[0]
	assert.Equal(t, "mine", got.Id)
	assert.Equal(t, "chat-1", got.GetChatId())
	assert.Equal(t, "thread-9", got.GetThreadId())
	assert.Equal(t, "machine-b", got.GetDaemonId(), "the machine asked, since the daemon stamped none")
	assert.Equal(t, "/w", got.WorkingDir)
	assert.True(t, got.KeepAlive)
	assert.Equal(t, stops.Format(time.RFC3339), got.GetStopsAfter())
	require.Len(t, got.Ports, 1)
	assert.Equal(t, int32(5173), got.Ports[0].Port)
}

// Another user's chat reads like a missing one, and nothing is asked of any
// machine.
func TestListProcesses_ForeignChatIsNotFound(t *testing.T) {
	router := newMachineRouter(nil)
	repo := chatRepo(map[string]*db.Chat{"chat-x": {ID: "chat-x", UserID: "someone-else"}}, nil)
	svc := NewTestBackground(t, WithBackgroundDeps(Deps{Router: router, Repo: repo}))

	for _, chatID := range []string{"chat-x", "missing"} {
		_, err := svc.ListProcesses(AuthedContext(t), connect.NewRequest(&reliantv1.ListBackgroundProcessesRequest{ChatId: proto.String(chatID)}))
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), chatID)
	}
	assert.Empty(t, router.sent)
}

// A kill or an output read naming a machine goes to that machine, and only
// when it is the caller's.
func TestProcessRequests_RouteToTheNamedMachine(t *testing.T) {
	router := newMachineRouter(nil)
	repo := chatRepo(nil, map[string]*db.Daemon{
		"mine":   {ID: "mine", UserID: testUser},
		"theirs": {ID: "theirs", UserID: "someone-else"},
	})
	svc := NewTestBackground(t, WithBackgroundDeps(Deps{Router: router, Repo: repo}))
	ctx := AuthedContext(t)

	_, err := svc.KillProcess(ctx, connect.NewRequest(&reliantv1.KillProcessRequest{ProcessId: "p", DaemonId: proto.String("mine")}))
	require.NoError(t, err)
	_, err = svc.GetProcessOutput(ctx, connect.NewRequest(&reliantv1.GetProcessOutputRequest{ProcessId: "p", DaemonId: proto.String("mine")}))
	require.NoError(t, err)
	assert.Equal(t, []string{"mine exec.bg_kill", "mine exec.bg_output"}, router.sent)

	_, err = svc.KillProcess(ctx, connect.NewRequest(&reliantv1.KillProcessRequest{ProcessId: "p", DaemonId: proto.String("theirs")}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	_, err = svc.GetProcessOutput(ctx, connect.NewRequest(&reliantv1.GetProcessOutputRequest{ProcessId: "p", DaemonId: proto.String("theirs")}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	assert.Len(t, router.sent, 2, "nothing reaches another user's machine")
}
