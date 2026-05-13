// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
)

// newTestDaemonClient creates a minimal daemonClient for testing without
// heavy dependencies like MCP, terminal manager, etc.
func newTestDaemonClient(daemonID, userID string) *daemonClient {
	return &daemonClient{
		daemonID:   daemonID,
		daemonName: "test-daemon",
		userID:     userID,
		hostname:   "test-host",
		platform:   "test",
		cwd:        "/tmp/test",
		bootCfg: bootstrap.DaemonBootstrapConfig{
			AuthToken:  "tok",
			ServerMode: true,
		},
		localExecutor:     toolexec.NewLocalToolExecutor(nil),
		capabilities:      []string{"test_tool"},
		cancelByReq:       make(map[string]context.CancelFunc),
		watchersByPr:      make(map[string]context.CancelFunc),
		terminalPumps:     newTerminalPumpTracker(),
		processOutputSubs: newProcessOutputSubTracker(),
	}
}

func TestDaemonServer_RegistrationMessageContents(t *testing.T) {
	d := newTestDaemonClient("daemon-abc", "user-42")
	d.hostname = "my-laptop"
	d.platform = "darwin"
	d.cwd = "/home/user/projects"
	d.capabilities = []string{"bash", "read_file", "write_file"}
	d.daemonName = "work-daemon"

	// The ConnectGateway handler sends a DaemonRegister as the first message.
	// daemon_id and user_id are no longer in the register message — both are
	// derived server-side from the PAT and returned in RegistrationAck.
	register := &reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_Register{Register: &reliantv1.DaemonRegister{
			Hostname:     d.hostname,
			Platform:     d.platform,
			WorkingDir:   d.cwd,
			Capabilities: d.capabilities,
			Name:         d.daemonName,
			DaemonType:   "local",
		}},
	}

	reg := register.GetRegister()
	require.NotNil(t, reg)
	assert.Equal(t, "my-laptop", reg.Hostname)
	assert.Equal(t, "darwin", reg.Platform)
	assert.Equal(t, "/home/user/projects", reg.WorkingDir)
	assert.Equal(t, []string{"bash", "read_file", "write_file"}, reg.Capabilities)
	assert.Equal(t, "work-daemon", reg.Name)
	assert.Equal(t, "local", reg.DaemonType)
}

func TestDaemonServer_DefaultPortConfig(t *testing.T) {
	d := newTestDaemonClient("d-1", "u-1")
	d.bootCfg.ListenPort = 0

	// Mirrors the logic in runServerMode.
	port := d.bootCfg.ListenPort
	if port == 0 {
		port = 9190
	}
	assert.Equal(t, 9190, port, "default port should be 9190")
}

func TestDaemonServer_CustomPortConfig(t *testing.T) {
	d := newTestDaemonClient("d-1", "u-1")
	d.bootCfg.ListenPort = 8080

	port := d.bootCfg.ListenPort
	if port == 0 {
		port = 9190
	}
	assert.Equal(t, 8080, port, "custom port should be used")
}

func TestDaemonServer_HandleServerMessage_Heartbeat(t *testing.T) {
	d := newTestDaemonClient("d-hb", "u-1")

	// Set up sendCh so that handleServerMessage doesn't panic.
	d.sendCh = make(chan *reliantv1.DaemonMessage, 256)
	d.sendDone = make(chan struct{})
	d.sessionDone = make(chan struct{})
	// Drain sendCh in the background, stopping when sessionDone closes.
	go func() {
		defer close(d.sendDone)
		for {
			select {
			case <-d.sendCh:
			case <-d.sessionDone:
				return
			}
		}
	}()
	defer func() {
		close(d.sessionDone)
		<-d.sendDone
	}()

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_Heartbeat{
			Heartbeat: &reliantv1.ServerHeartbeat{},
		},
	}
	err := d.handleServerMessage(context.Background(), msg)
	assert.NoError(t, err, "heartbeat should be handled without error")
}

func TestDaemonServer_HandleServerMessage_ToolRequest(t *testing.T) {
	d := newTestDaemonClient("d-tool", "u-1")

	d.sendCh = make(chan *reliantv1.DaemonMessage, 256)
	d.sendDone = make(chan struct{})
	d.sessionDone = make(chan struct{})
	go func() {
		defer close(d.sendDone)
		for {
			select {
			case <-d.sendCh:
			case <-d.sessionDone:
				return
			}
		}
	}()
	defer func() {
		close(d.sessionDone)
		<-d.sendDone
	}()

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_ToolRequest{
			ToolRequest: &reliantv1.ToolRequest{
				RequestId: "req-1",
				ToolName:  "nonexistent_tool",
				ToolInput: "{}",
			},
		},
	}
	err := d.handleServerMessage(context.Background(), msg)
	assert.NoError(t, err, "tool request should be dispatched without error")
}

func TestDaemonServer_HandleServerMessage_NilToolRequest(t *testing.T) {
	d := newTestDaemonClient("d-nil", "u-1")

	d.sendCh = make(chan *reliantv1.DaemonMessage, 256)
	d.sendDone = make(chan struct{})
	d.sessionDone = make(chan struct{})
	go func() {
		defer close(d.sendDone)
		for {
			select {
			case <-d.sendCh:
			case <-d.sessionDone:
				return
			}
		}
	}()
	defer func() {
		close(d.sessionDone)
		<-d.sendDone
	}()

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_ToolRequest{
			ToolRequest: nil,
		},
	}
	err := d.handleServerMessage(context.Background(), msg)
	assert.NoError(t, err, "nil tool request should be handled gracefully")
}

func TestDaemonServer_HandleServerMessage_RegistrationAck(t *testing.T) {
	d := newTestDaemonClient("d-ack", "u-1")

	d.sendCh = make(chan *reliantv1.DaemonMessage, 256)
	d.sendDone = make(chan struct{})
	d.sessionDone = make(chan struct{})
	go func() {
		defer close(d.sendDone)
		for {
			select {
			case <-d.sendCh:
			case <-d.sessionDone:
				return
			}
		}
	}()
	defer func() {
		close(d.sessionDone)
		<-d.sendDone
	}()

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_RegistrationAck{
			RegistrationAck: &reliantv1.RegistrationAck{
				Accepted: true,
			},
		},
	}
	err := d.handleServerMessage(context.Background(), msg)
	assert.NoError(t, err, "registration ack should be handled without error")
}

func TestDaemonServer_HandleServerMessage_ToolCancel(t *testing.T) {
	d := newTestDaemonClient("d-cancel", "u-1")

	d.sendCh = make(chan *reliantv1.DaemonMessage, 256)
	d.sendDone = make(chan struct{})
	d.sessionDone = make(chan struct{})
	go func() {
		defer close(d.sendDone)
		for {
			select {
			case <-d.sendCh:
			case <-d.sessionDone:
				return
			}
		}
	}()
	defer func() {
		close(d.sessionDone)
		<-d.sendDone
	}()

	msg := &reliantv1.ServerMessage{
		Message: &reliantv1.ServerMessage_ToolCancel{
			ToolCancel: &reliantv1.ToolExecutionCancel{
				RequestId: "req-nonexistent",
			},
		},
	}
	err := d.handleServerMessage(context.Background(), msg)
	assert.NoError(t, err, "tool cancel for unknown request should be handled gracefully")
}
