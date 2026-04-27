// Copyright (c) 2025 Reliant Labs

package servergateway

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock NATS message for handleMessage tests
// ---------------------------------------------------------------------------

type mockMsg struct {
	subject string
	data    []byte

	mu     sync.Mutex
	acked  bool
	naked  bool
	termed bool
}

func (m *mockMsg) Subject() string                           { return m.subject }
func (m *mockMsg) Data() []byte                              { return m.data }
func (m *mockMsg) Reply() string                             { return "" }
func (m *mockMsg) Headers() nats.Header                      { return nats.Header{} }
func (m *mockMsg) Metadata() (*jetstream.MsgMetadata, error) { return nil, nil }
func (m *mockMsg) NakWithDelay(_ time.Duration) error        { return nil }
func (m *mockMsg) InProgress() error                         { return nil }
func (m *mockMsg) DoubleAck(_ context.Context) error         { return nil }

func (m *mockMsg) Ack() error {
	m.mu.Lock()
	m.acked = true
	m.mu.Unlock()
	return nil
}

func (m *mockMsg) Nak() error {
	m.mu.Lock()
	m.naked = true
	m.mu.Unlock()
	return nil
}

func (m *mockMsg) Term() error {
	m.mu.Lock()
	m.termed = true
	m.mu.Unlock()
	return nil
}

func (m *mockMsg) TermWithReason(_ string) error { return m.Term() }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func newTestConnector() *DaemonConnector {
	return &DaemonConnector{
		activeConns: make(map[string]context.CancelFunc),
	}
}

func TestStartConnection_AddsToActiveConns(t *testing.T) {
	dc := newTestConnector()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dc.startConnection(ctx, DaemonConnectCommand{
		DaemonID: "d-1",
		PodIP:    "10.0.0.1",
		Port:     9190,
	})

	dc.mu.Lock()
	_, exists := dc.activeConns["d-1"]
	dc.mu.Unlock()
	assert.True(t, exists, "expected activeConns to contain daemon d-1")

	// Clean up.
	dc.stopConnection("d-1")
}

func TestStopConnection_CancelsActiveConnection(t *testing.T) {
	dc := newTestConnector()

	connCtx, connCancel := context.WithCancel(context.Background())
	dc.activeConns["d-1"] = connCancel

	dc.stopConnection("d-1")

	dc.mu.Lock()
	_, exists := dc.activeConns["d-1"]
	dc.mu.Unlock()
	assert.False(t, exists, "d-1 should be removed from activeConns after disconnect")
	assert.Error(t, connCtx.Err(), "connection context should be cancelled")
}

func TestStopConnection_UnknownDaemon_NoOp(t *testing.T) {
	dc := newTestConnector()

	// Should not panic.
	dc.stopConnection("nonexistent")

	dc.mu.Lock()
	assert.Len(t, dc.activeConns, 0)
	dc.mu.Unlock()
}

func TestStartConnection_ReplacesExisting(t *testing.T) {
	dc := newTestConnector()
	ctx := context.Background()

	firstCtx, firstCancel := context.WithCancel(ctx)
	dc.activeConns["d-1"] = firstCancel

	dc.startConnection(ctx, DaemonConnectCommand{
		DaemonID: "d-1",
		PodIP:    "10.0.0.2",
		Port:     9190,
	})

	assert.Error(t, firstCtx.Err(), "old connection context should be cancelled")

	dc.mu.Lock()
	_, exists := dc.activeConns["d-1"]
	dc.mu.Unlock()
	assert.True(t, exists, "new connection should exist in activeConns")

	dc.stopConnection("d-1")
}

func TestHandleMessage_ConnectCommand_ValidatesFields(t *testing.T) {
	dc := newTestConnector()
	ctx := context.Background()

	tests := []struct {
		name     string
		cmd      DaemonConnectCommand
		wantTerm bool
		wantConn bool
	}{
		{
			name:     "missing DaemonID",
			cmd:      DaemonConnectCommand{PodIP: "10.0.0.1", Port: 9190},
			wantTerm: true,
		},
		{
			name:     "missing PodIP",
			cmd:      DaemonConnectCommand{DaemonID: "d-1", Port: 9190},
			wantTerm: true,
		},
		{
			name:     "missing Port",
			cmd:      DaemonConnectCommand{DaemonID: "d-1", PodIP: "10.0.0.1"},
			wantTerm: true,
		},
		{
			name:     "valid command",
			cmd:      DaemonConnectCommand{DaemonID: "d-valid", PodIP: "10.0.0.1", Port: 9190},
			wantConn: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc.activeConns = make(map[string]context.CancelFunc)
			data, _ := json.Marshal(tt.cmd)
			msg := &mockMsg{
				subject: subjectCommandConnect,
				data:    data,
			}
			dc.handleMessage(ctx, msg)

			if tt.wantTerm {
				msg.mu.Lock()
				assert.True(t, msg.termed, "expected message to be terminated")
				msg.mu.Unlock()
			}
			if tt.wantConn {
				time.Sleep(50 * time.Millisecond)
				dc.mu.Lock()
				_, exists := dc.activeConns[tt.cmd.DaemonID]
				dc.mu.Unlock()
				assert.True(t, exists, "expected connection to be started")
				dc.stopConnection(tt.cmd.DaemonID)
			}
		})
	}
}

func TestHandleMessage_DisconnectCommand(t *testing.T) {
	dc := newTestConnector()
	ctx := context.Background()

	_, cancel := context.WithCancel(ctx)
	dc.activeConns["d-disc"] = cancel

	data, _ := json.Marshal(DaemonDisconnectCommand{DaemonID: "d-disc"})
	msg := &mockMsg{
		subject: subjectCommandDisconnect,
		data:    data,
	}
	dc.handleMessage(ctx, msg)

	msg.mu.Lock()
	assert.True(t, msg.acked, "expected disconnect message to be acked")
	msg.mu.Unlock()

	dc.mu.Lock()
	_, exists := dc.activeConns["d-disc"]
	dc.mu.Unlock()
	assert.False(t, exists, "connection should be removed after disconnect")
}

func TestHandleMessage_UnknownSubject_Terms(t *testing.T) {
	dc := newTestConnector()

	msg := &mockMsg{
		subject: "daemon.v1.commands.unknown",
		data:    []byte("{}"),
	}
	dc.handleMessage(context.Background(), msg)

	msg.mu.Lock()
	assert.True(t, msg.termed, "expected unknown subject message to be terminated")
	msg.mu.Unlock()
}

func TestHandleMessage_InvalidConnectJSON_Naks(t *testing.T) {
	dc := newTestConnector()

	msg := &mockMsg{
		subject: subjectCommandConnect,
		data:    []byte("not json"),
	}
	dc.handleMessage(context.Background(), msg)

	msg.mu.Lock()
	assert.True(t, msg.naked, "expected invalid JSON connect command to be naked")
	msg.mu.Unlock()
}

func TestHandleMessage_InvalidDisconnectJSON_Naks(t *testing.T) {
	dc := newTestConnector()

	msg := &mockMsg{
		subject: subjectCommandDisconnect,
		data:    []byte("{invalid"),
	}
	dc.handleMessage(context.Background(), msg)

	msg.mu.Lock()
	assert.True(t, msg.naked, "expected invalid JSON disconnect command to be naked")
	msg.mu.Unlock()
}

func TestHandleMessage_ValidConnect_Acks(t *testing.T) {
	dc := newTestConnector()

	cmd := DaemonConnectCommand{
		DaemonID: "d-ack",
		PodIP:    "10.0.0.1",
		Port:     9190,
	}
	data, err := json.Marshal(cmd)
	require.NoError(t, err)

	msg := &mockMsg{
		subject: subjectCommandConnect,
		data:    data,
	}
	dc.handleMessage(context.Background(), msg)

	msg.mu.Lock()
	assert.True(t, msg.acked, "expected valid connect command to be acked")
	msg.mu.Unlock()

	// Clean up.
	time.Sleep(50 * time.Millisecond)
	dc.stopConnection("d-ack")
}
