// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

type fakeChats struct {
	chats []*reliantv1.Chat
	set   *reliantv1.SetChatDaemonRequest
}

func (f *fakeChats) ListChats(context.Context, *connect.Request[reliantv1.ListChatsRequest]) (*connect.Response[reliantv1.ListChatsResponse], error) {
	return connect.NewResponse(&reliantv1.ListChatsResponse{Chats: f.chats}), nil
}

func (f *fakeChats) GetChat(_ context.Context, req *connect.Request[reliantv1.GetChatRequest]) (*connect.Response[reliantv1.GetChatResponse], error) {
	for _, c := range f.chats {
		if c.GetId() == req.Msg.GetChatId() {
			return connect.NewResponse(&reliantv1.GetChatResponse{Chat: c}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, nil)
}

func (f *fakeChats) SetChatDaemon(_ context.Context, req *connect.Request[reliantv1.SetChatDaemonRequest]) (*connect.Response[reliantv1.SetChatDaemonResponse], error) {
	f.set = req.Msg
	chat := &reliantv1.Chat{Id: req.Msg.GetChatId()}
	if req.Msg.GetDaemonId() != "" {
		chat.ActiveDaemonId = proto.String(req.Msg.GetDaemonId())
	}
	return connect.NewResponse(&reliantv1.SetChatDaemonResponse{Chat: chat}), nil
}

type fakeMachines struct{ daemons []*reliantv1.DaemonInfo }

func (f fakeMachines) ListDaemons(context.Context, *connect.Request[reliantv1.ListDaemonsRequest]) (*connect.Response[reliantv1.ListDaemonsResponse], error) {
	return connect.NewResponse(&reliantv1.ListDaemonsResponse{Daemons: f.daemons}), nil
}

// withChatClients points the chat commands at fakes for one test. Not
// parallel: chatClients is package state.
func withChatClients(t *testing.T, chats *fakeChats, machines fakeMachines) {
	t.Helper()
	prev := chatClients
	chatClients = func(*cobra.Command) (chatClient, machineLister, *connection, error) {
		return chats, machines, &connection{}, nil
	}
	t.Cleanup(func() { chatClients = prev })
}

func runChat(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newChatCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// TestChatCommands: `reliant chat` shows the machine a chat runs on and
// moves it, as the chat header's machine picker does (north-star AW-32).
func TestChatCommands(t *testing.T) {
	chats := &fakeChats{chats: []*reliantv1.Chat{
		{Id: "c1", Title: "Fix the build", ProjectId: "p1", ActiveDaemonId: proto.String("d-build"), State: reliantv1.ChatState_CHAT_STATE_IDLE},
		{Id: "c2", Title: "Branch work", ProjectId: "p1", WorktreeId: proto.String("w1"), Worktree: &reliantv1.WorktreeSummary{Branch: "feat/x"}, State: reliantv1.ChatState_CHAT_STATE_IDLE},
		{Id: "c3", Title: "Old", ProjectId: "p1", State: reliantv1.ChatState_CHAT_STATE_ARCHIVED},
	}}
	machines := fakeMachines{daemons: []*reliantv1.DaemonInfo{
		{DaemonId: "d-build", Name: "build-box"},
		{DaemonId: "d-laptop", Hostname: "laptop"},
	}}
	withChatClients(t, chats, machines)

	out, err := runChat(t, "list")
	require.NoError(t, err)
	assert.Contains(t, out, "build-box (d-build)", "the machine is named, with its id")
	assert.Contains(t, out, "(default machine)", "an unpinned chat follows the default machine")
	assert.Contains(t, out, "feat/x", "a branch chat shows its workspace's branch")
	assert.NotContains(t, out, "c3", "archived chats are hidden by default")

	out, err = runChat(t, "list", "--archived", "--json")
	require.NoError(t, err)
	var listed struct {
		Chats []map[string]any `json:"chats"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &listed))
	assert.Len(t, listed.Chats, 3)
	assert.Equal(t, "d-build", listed.Chats[0]["active_daemon_id"])

	out, err = runChat(t, "show", "c2")
	require.NoError(t, err)
	assert.Contains(t, out, "feat/x")
	assert.Contains(t, out, "(default machine)")

	out, err = runChat(t, "set-machine", "c1", "laptop")
	require.NoError(t, err)
	require.NotNil(t, chats.set)
	assert.Equal(t, "d-laptop", chats.set.GetDaemonId(), "a machine named by hostname resolves to its id")
	assert.Contains(t, out, "laptop (d-laptop)")

	out, err = runChat(t, "set-machine", "c1", "--clear")
	require.NoError(t, err)
	assert.Empty(t, chats.set.GetDaemonId(), "--clear sends an empty daemon id")
	assert.Contains(t, out, "follows your default machine")

	_, err = runChat(t, "set-machine", "c1")
	assert.ErrorContains(t, err, "--clear", "neither a machine nor --clear is a usage error")
	_, err = runChat(t, "set-machine", "c1", "nope")
	assert.ErrorContains(t, err, `no machine "nope"`)
}
