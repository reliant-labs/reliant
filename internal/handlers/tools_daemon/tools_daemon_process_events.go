// Copyright (c) 2025 Reliant Labs

package tools_daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// processEventsCapability is what a RegistrationAck lists when this gateway
// handles DaemonMessage.process_event. A daemon sends the events only to a
// gateway that lists it, so an older gateway is never sent a message it does
// not know.
const processEventsCapability = "process_events"

// GatewayCapabilities is what every RegistrationAck this gateway sends lists
// (RegistrationAck.capabilities), whichever side dialled the stream.
func GatewayCapabilities() []string {
	return []string{processEventsCapability}
}

// processUpdateTypes maps a daemon's process event to the user update the
// web's process list handles. A kill is a FAILED update: the web tells it
// apart by the status it carries ("killed").
var processUpdateTypes = map[string]db.UserUpdateType{
	"started":      db.UserUpdateProcessStarted,
	"completed":    db.UserUpdateProcessCompleted,
	"failed":       db.UserUpdateProcessFailed,
	"killed":       db.UserUpdateProcessFailed,
	"port_changed": db.UserUpdateProcessPortChanged,
}

// handleProcessEvent turns a daemon's background-process lifecycle event into
// a PROCESS_* user update for the machine's owner, so their clients' process
// lists change the moment the process does.
//
// The update is addressed from the stream (conn.userID), never from the
// event: a daemon can describe only its own processes, to its own owner. The
// chat, thread and workspace the event names ride in the update's data, not
// on the row's chat_id / worktree_id: those columns are foreign keys a
// daemon-supplied id must not be trusted to satisfy (an insert naming a
// deleted chat would fail and drop the update), and the row would take a key
// lock on the chat or workspace for a hint nobody joins on.
func (s *Service) handleProcessEvent(ctx context.Context, conn *daemonConnection, event *reliantv1.ProcessLifecycleEvent) error {
	if event == nil || event.GetProcessId() == "" {
		return nil
	}
	updateType, ok := processUpdateTypes[event.GetType()]
	if !ok {
		return nil // a type from a newer daemon: the list read still covers it
	}
	data, err := json.Marshal(processEventData(event, conn.daemonID))
	if err != nil {
		return fmt.Errorf("marshal process event: %w", err)
	}
	return s.deps.Repo.CreateUserUpdate(ctx, &db.UserUpdate{
		UserID:     conn.userID,
		UpdateType: updateType,
		EntityType: db.EntityTypeBackgroundProcess,
		EntityID:   event.GetProcessId(),
		Data:       data,
	})
}

// processEventPort is a listening port as the web's process handlers read it.
type processEventPort struct {
	Port     int32  `json:"port"`
	Protocol string `json:"protocol"`
	State    string `json:"state"`
	Address  string `json:"address"`
}

// processEventPayload is a PROCESS_* update's data, in the shape
// frontends/web/src/store/globalUpdatesStore.ts (handleProcessStarted,
// updateProcessStatus) reads.
type processEventPayload struct {
	ProcessID      string             `json:"process_id"`
	Command        string             `json:"command"`
	WorkingDir     string             `json:"working_dir"`
	Status         string             `json:"status"`
	ExitCode       *int32             `json:"exit_code,omitempty"`
	StartTime      string             `json:"start_time"`
	EndTime        string             `json:"end_time,omitempty"`
	ChatID         string             `json:"chat_id,omitempty"`
	ThreadID       string             `json:"thread_id,omitempty"`
	WorktreeID     string             `json:"worktree_id,omitempty"`
	PackageCommand bool               `json:"package_command,omitempty"`
	KeepAlive      bool               `json:"keep_alive,omitempty"`
	DaemonID       string             `json:"daemon_id,omitempty"`
	Ports          []processEventPort `json:"ports,omitempty"`
}

func processEventData(event *reliantv1.ProcessLifecycleEvent, daemonID string) processEventPayload {
	out := processEventPayload{
		ProcessID:      event.GetProcessId(),
		Command:        event.GetCommand(),
		WorkingDir:     event.GetWorkingDir(),
		Status:         event.GetStatus(),
		ExitCode:       event.ExitCode,
		ChatID:         event.GetChatId(),
		ThreadID:       event.GetThreadId(),
		WorktreeID:     event.GetWorktreeId(),
		PackageCommand: event.GetPackageCommand(),
		KeepAlive:      event.GetKeepAlive(),
		// The machine is the stream's, whatever the event says.
		DaemonID: daemonID,
	}
	if ms := event.GetStartTimeUnixMs(); ms > 0 {
		out.StartTime = time.UnixMilli(ms).UTC().Format(time.RFC3339)
	}
	if event.EndTimeUnixMs != nil {
		out.EndTime = time.UnixMilli(event.GetEndTimeUnixMs()).UTC().Format(time.RFC3339)
	}
	for _, p := range event.GetPorts() {
		out.Ports = append(out.Ports, processEventPort{Port: p.GetPort(), Protocol: p.GetProtocol(), State: p.GetState(), Address: p.GetAddress()})
	}
	return out
}
