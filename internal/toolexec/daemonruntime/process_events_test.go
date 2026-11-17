// Copyright (c) 2025 Reliant Labs

package daemonruntime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
)

func queued(s *gatewaySession) []*reliantv1.DaemonMessage {
	var out []*reliantv1.DaemonMessage
	for {
		select {
		case m := <-s.sendCh:
			out = append(out, m)
		default:
			return out
		}
	}
}

// An event goes only to a stream whose gateway said it handles them: an older
// gateway logs a warning for every message it does not know.
func TestSendProcessEvent_OnlyToAGatewayThatAcceptsThem(t *testing.T) {
	d := &daemonClient{}
	event := shell.ProcessEvent{Type: "started", ProcessID: "p1", Status: "running", StartTime: time.UnixMilli(1000)}

	d.sendProcessEvent(event) // no stream at all: dropped, no panic

	old := newGatewaySession()
	old.processEvents.Store(gatewayAcceptsProcessEvents(&reliantv1.RegistrationAck{}))
	d.session.Store(old)
	d.sendProcessEvent(event)
	assert.Empty(t, queued(old), "a gateway that lists no capability is sent none")

	current := newGatewaySession()
	current.processEvents.Store(gatewayAcceptsProcessEvents(&reliantv1.RegistrationAck{Capabilities: []string{"process_events"}}))
	d.session.Store(current)
	d.sendProcessEvent(event)
	msgs := queued(current)
	require.Len(t, msgs, 1)
	assert.Equal(t, "p1", msgs[0].GetProcessEvent().GetProcessId())
}

// An event is a hint, never a wait: a full writer drops it rather than block
// the process manager's callback.
func TestSendProcessEvent_DropsWhenTheWriterIsFull(t *testing.T) {
	s := newGatewaySession()
	s.processEvents.Store(true)
	for range sessionSendBuffer {
		require.True(t, s.trySend(&reliantv1.DaemonMessage{}))
	}
	d := &daemonClient{}
	d.session.Store(s)

	done := make(chan struct{})
	go func() {
		d.sendProcessEvent(shell.ProcessEvent{Type: "started", ProcessID: "p1"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sendProcessEvent blocked on a full writer")
	}
}

func TestProcessLifecycleEvent_CarriesWhoStartedItAndHowItEnded(t *testing.T) {
	code := 2
	end := time.UnixMilli(5000)
	got := processLifecycleEvent(shell.ProcessEvent{
		Type: "failed", ProcessID: "p", Command: "go test", WorkingDir: "/w", Status: "failed",
		ExitCode: &code, StartTime: time.UnixMilli(1000), EndTime: &end,
		ChatID: "c", ThreadID: "t", KeepAlive: true,
		Ports: []shell.PortInfo{{Port: 8080, Protocol: "tcp"}},
	})
	assert.Equal(t, int32(2), got.GetExitCode())
	assert.Equal(t, int64(1000), got.GetStartTimeUnixMs())
	assert.Equal(t, int64(5000), got.GetEndTimeUnixMs())
	assert.Equal(t, "c", got.GetChatId())
	assert.Equal(t, "t", got.GetThreadId())
	assert.True(t, got.GetKeepAlive())
	require.Len(t, got.GetPorts(), 1)
	assert.Equal(t, int32(8080), got.GetPorts()[0].GetPort())
}
