// Copyright (c) 2025 Reliant Labs

package toolexec

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// countingCommandMgr answers every daemon command at once, counting them.
type countingCommandMgr struct {
	recordingDaemonMgr
	handled atomic.Int64
}

func (m *countingCommandMgr) SendDaemonCommandToDaemon(context.Context, string, string, *reliantv1.DaemonCommandRequest) (*reliantv1.DaemonCommandResponse, error) {
	m.handled.Add(1)
	return &reliantv1.DaemonCommandResponse{Success: true}, nil
}

// Close waits for the bridge's handlers, and those are started by
// subscription callbacks on nats.go's own goroutines. Nothing ordered a
// callback's WaitGroup.Add before Close's Wait, so a gateway shutting down
// while commands were still arriving was a data race — the race detector
// reported it in the release gate, in the teardown of a rollout test whose
// replica was still being asked tool.status. Run under -race with commands
// arriving throughout, this fails on the old code.
//
// Once Close has begun nothing new starts: a handler started then would
// outlive the Wait that is meant to be the bridge's last word.
func TestClose_DoesNotRaceHandlersStartedByArrivingCommands(t *testing.T) {
	nc := startBridgeTestNATS(t)
	mgr := &countingCommandMgr{}
	bridge := NewNATSToolBridge(nc, nil, mgr)
	const userID, daemonID = "u-1", "d-1"
	bridge.OnDaemonConnected(userID, daemonID)

	subject := daemonSubject(daemonCommandSubject, userID, daemonID)
	data, err := json.Marshal(daemonCommandWire{RequestID: "status-1", CommandType: ToolStatusCommand, TimeoutMs: 10_000})
	require.NoError(t, err)
	inbox := nats.NewInbox()

	// A daemon's worth of commands, arriving until the bridge is closed.
	stop := make(chan struct{})
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if nc.PublishRequest(subject, inbox, data) != nil {
				return
			}
			time.Sleep(50 * time.Microsecond)
		}
	}()
	t.Cleanup(func() { close(stop); <-sent })

	require.Eventually(t, func() bool { return mgr.handled.Load() >= 50 },
		30*time.Second, time.Millisecond, "the bridge never handled the arriving commands")

	closed := make(chan struct{})
	go func() {
		_ = bridge.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(30 * time.Second):
		t.Fatal("Close never returned")
	}
	require.False(t, bridge.goTracked(func() { t.Error("a goroutine started after Close") }),
		"nothing starts once Close has begun")
}
