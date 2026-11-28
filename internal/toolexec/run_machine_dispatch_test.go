// Copyright (c) 2025 Reliant Labs
package toolexec

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/daemonquery"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// answerLiveness subscribes what daemonliveness asks: the per-user any-live
// subject and daemonID's status subject. It is the registry's "attached"
// answer, not the bridge's subscriptions commands travel on.
func answerLiveness(t *testing.T, nc *nats.Conn, userID, daemonID string) {
	t.Helper()
	_, err := nc.Subscribe(daemonquery.SubjectUserAnyLive(userID), func(m *nats.Msg) {
		body, _ := json.Marshal(daemonquery.UserLiveness{Live: true, Count: 1})
		_ = m.Respond(body)
	})
	require.NoError(t, err)
	_, err = nc.Subscribe(daemonquery.SubjectStatus(daemonID), func(m *nats.Msg) {
		body, _ := json.Marshal(daemonquery.Status{Connected: true, LastActiveMs: time.Now().UnixMilli()})
		_ = m.Respond(body)
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
}

// answerDispatch subscribes daemonID's tools.online subject the way the
// gateway's bridge does when it holds the daemon's stream, beside the
// subjects commands are sent on.
func answerDispatch(t *testing.T, nc *nats.Conn, userID, daemonID string) {
	t.Helper()
	_, err := nc.Subscribe(daemonSubject(toolOnlineSubject, userID, daemonID), func(m *nats.Msg) {
		_ = m.Respond([]byte("true"))
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())
}

// reliant#2812: wait_for_machine said "connected" and the very next shell call
// failed with "no daemon connected for user". The user's default machine was
// away while another of their machines was up: liveness with no selector asks
// about ANY daemon, a command with no selector goes to the default one.
// Connected now means the default machine answers.
func TestRunMachineStatus_NoSelectorAsksAboutTheMachineCommandsGoTo(t *testing.T) {
	nc := onlineTestNATS(t)
	records := &fakeDaemonRecords{daemons: []*db.Daemon{
		{ID: "laptop", UserID: "u", IsDefault: true},
		{ID: "cloud", UserID: "u"},
	}}
	router := NewNATSDaemonRouter(nc, WithDatabase(records))
	answerLiveness(t, nc, "u", "cloud")
	answerDispatch(t, nc, "u", "cloud")

	online, err := router.IsDaemonOnline(context.Background(), "u", nil)
	require.NoError(t, err)
	require.True(t, online, "precondition: liveness with no selector is any daemon")

	got, err := NewRunMachine(router).MachineStatus(context.Background(), "u")
	require.NoError(t, err)
	assert.False(t, got.Connected, "a command would go to the default machine, which is away")
	assert.Equal(t, notAnsweringMessage, got.StatusMessage)

	answerDispatch(t, nc, "u", "laptop")
	got, err = NewRunMachine(router).MachineStatus(context.Background(), "u")
	require.NoError(t, err)
	assert.Equal(t, tools.MachineStatus{Connected: true}, got)
}

// The same daemon, named by the run's selector: its liveness subject answers
// (the gateway registered the stream) but the bridge's subscriptions are not
// there, so a command finds no responder. That is not connected.
func TestRunMachineStatus_LiveButNotDispatchableIsNotConnected(t *testing.T) {
	nc := onlineTestNATS(t)
	records := &fakeDaemonRecords{daemons: []*db.Daemon{{ID: "daemon-a", UserID: "u"}}, attached: []string{"daemon-a"}}
	router := NewNATSDaemonRouter(nc, WithDatabase(records))
	answerLiveness(t, nc, "u", "daemon-a")
	ctx := WithDaemonSelector(context.Background(), &DaemonSelector{ID: "daemon-a"})

	got, err := NewRunMachine(router).MachineStatus(ctx, "u")
	require.NoError(t, err)
	assert.False(t, got.Connected)
	assert.False(t, got.Removed)
	assert.Equal(t, notAnsweringMessage, got.StatusMessage)

	answerDispatch(t, nc, "u", "daemon-a")
	got, err = NewRunMachine(router).MachineStatus(ctx, "u")
	require.NoError(t, err)
	assert.Equal(t, tools.MachineStatus{Connected: true}, got)
}

// A daemon draining for a restart answers tools.online with "updating", not
// "true": a command would not run until it is back, so it is not connected.
func TestDispatchReachable_UpdatingIsNotReachable(t *testing.T) {
	nc := onlineTestNATS(t)
	records := &fakeDaemonRecords{daemons: []*db.Daemon{{ID: "daemon-a", UserID: "u"}}, attached: []string{"daemon-a"}}
	router := NewNATSDaemonRouter(nc, WithDatabase(records))
	_, err := nc.Subscribe(daemonSubject(toolOnlineSubject, "u", "daemon-a"), func(m *nats.Msg) {
		_ = m.Respond(EncodeOnlineReplyUpdating(time.Now().Add(time.Minute)))
	})
	require.NoError(t, err)
	require.NoError(t, nc.Flush())

	assert.False(t, router.DispatchReachable(context.Background(), "u", &DaemonSelector{ID: "daemon-a"}))
}

func TestDispatchReachable_NoNATSIsNotReachable(t *testing.T) {
	router := NewNATSDaemonRouter(nil, WithDatabase(&fakeDaemonRecords{}))
	assert.False(t, router.DispatchReachable(context.Background(), "u", nil))
}
