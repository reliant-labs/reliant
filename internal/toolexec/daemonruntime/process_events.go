// Copyright (c) 2025 Reliant Labs

package daemonruntime

import (
	"slices"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
)

// processEventsCapability is the RegistrationAck capability of a gateway that
// turns DaemonMessage.process_event into its owner's PROCESS_* user updates.
const processEventsCapability = "process_events"

// gatewayAcceptsProcessEvents reports whether a registration ack's gateway
// handles process lifecycle events.
func gatewayAcceptsProcessEvents(ack *reliantv1.RegistrationAck) bool {
	return slices.Contains(ack.GetCapabilities(), processEventsCapability)
}

// sendProcessEvent tells the gateway a background process started, ended or
// changed its ports, so its owner's clients update their process lists at
// once instead of on their next read. It is the BackgroundManager's event
// callback (wired in run).
//
// Best effort, by design: an event is a hint that a list is stale, and the
// list itself (exec.bg_list) is always the truth. So it goes only to a
// current stream whose gateway handles it, and is dropped — never queued for
// later, never waited on — when there is none or its writer is full.
func (d *daemonClient) sendProcessEvent(event shell.ProcessEvent) {
	s := d.session.Load()
	if s == nil || !s.processEvents.Load() {
		return
	}
	s.trySend(&reliantv1.DaemonMessage{
		Message: &reliantv1.DaemonMessage_ProcessEvent{ProcessEvent: processLifecycleEvent(event)},
	})
}

// processLifecycleEvent is a BackgroundManager event on the wire.
func processLifecycleEvent(event shell.ProcessEvent) *reliantv1.ProcessLifecycleEvent {
	out := &reliantv1.ProcessLifecycleEvent{
		Type:            event.Type,
		ProcessId:       event.ProcessID,
		Command:         event.Command,
		WorkingDir:      event.WorkingDir,
		Status:          event.Status,
		StartTimeUnixMs: event.StartTime.UnixMilli(),
		ChatId:          event.ChatID,
		ThreadId:        event.ThreadID,
		WorktreeId:      event.WorktreeID,
		PackageCommand:  event.PackageCommand,
		KeepAlive:       event.KeepAlive,
	}
	if event.ExitCode != nil {
		code := int32(*event.ExitCode)
		out.ExitCode = &code
	}
	if event.EndTime != nil {
		end := event.EndTime.UnixMilli()
		out.EndTimeUnixMs = &end
	}
	for _, p := range event.Ports {
		out.Ports = append(out.Ports, &reliantv1.ProcessLifecyclePort{
			Port: int32(p.Port), Protocol: p.Protocol, State: p.State, Address: p.Address,
		})
	}
	return out
}
