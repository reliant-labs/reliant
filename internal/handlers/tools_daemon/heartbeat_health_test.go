// Copyright (c) 2026 Reliant Labs
package tools_daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// healthRepo records every health write.
func healthRepo(writes *[]db.MachineHealth) *MockRepository {
	return &MockRepository{
		UpdateDaemonAttachmentHealthFunc: func(_ context.Context, _ string, h db.MachineHealth) error {
			*writes = append(*writes, h)
			return nil
		},
	}
}

func degradedHB(reason string) *reliantv1.MachineHealth {
	return &reliantv1.MachineHealth{
		State: reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_DEGRADED,
		Code:  "process_table_full", Reason: reason,
	}
}

// The gateway stores what a heartbeat says about the machine's health on the
// lease, writing only on a change. A heartbeat without health (an old daemon)
// is a healthy machine. The since is the gateway's clock at the change and
// carries forward while the state holds — a reason that changes inside one
// degraded episode keeps it, and so does a lease the renewer re-created.
func TestRecordMachineHealth(t *testing.T) {
	var writes []db.MachineHealth
	svc := newTestService(healthRepo(&writes))
	t.Cleanup(svc.Close)
	conn := &daemonConnection{daemonID: "daemon-1"}
	ctx := context.Background()
	t0 := time.Date(2026, 10, 30, 17, 0, 0, 0, time.UTC)

	// An old daemon's heartbeats: healthy, written once.
	svc.recordMachineHealth(ctx, conn, nil, t0)
	svc.recordMachineHealth(ctx, conn, nil, t0.Add(15*time.Second))
	svc.recordMachineHealth(ctx, conn, &reliantv1.MachineHealth{State: reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_OK}, t0.Add(30*time.Second))
	require.Equal(t, []db.MachineHealth{{State: db.MachineHealthOK, Since: t0}}, writes)

	// The process table fills.
	t1 := t0.Add(57 * time.Minute)
	svc.recordMachineHealth(ctx, conn, degradedHB("full: 3 attempts"), t1)
	require.Len(t, writes, 2)
	assert.Equal(t, db.MachineHealth{State: db.MachineHealthDegraded, Code: "process_table_full", Reason: "full: 3 attempts", Since: t1}, writes[1])

	// Same episode, new reason: rewritten, since kept.
	svc.recordMachineHealth(ctx, conn, degradedHB("full: 4 attempts"), t1.Add(30*time.Second))
	require.Len(t, writes, 3)
	assert.Equal(t, "full: 4 attempts", writes[2].Reason)
	assert.Equal(t, t1, writes[2].Since)

	// Unchanged: no write.
	svc.recordMachineHealth(ctx, conn, degradedHB("full: 4 attempts"), t1.Add(45*time.Second))
	require.Len(t, writes, 3)

	// The renewer re-created the lease (columns back at their defaults):
	// written again, since carried.
	conn.healthWritten = false
	svc.recordMachineHealth(ctx, conn, degradedHB("full: 4 attempts"), t1.Add(time.Minute))
	require.Len(t, writes, 4)
	assert.Equal(t, t1, writes[3].Since)

	// Recovered: healthy, with a new since.
	t2 := t1.Add(24 * time.Minute)
	svc.recordMachineHealth(ctx, conn, &reliantv1.MachineHealth{State: reliantv1.MachineHealthState_MACHINE_HEALTH_STATE_OK}, t2)
	require.Len(t, writes, 5)
	assert.Equal(t, db.MachineHealth{State: db.MachineHealthOK, Since: t2}, writes[4])
}

// The heartbeat a web client receives says whether the machine can work, so
// the app re-reads the daemon list when that changes; an old daemon's
// heartbeat reads "ok".
func TestPublishDaemonHeartbeat_CarriesHealthState(t *testing.T) {
	svc := newTestService(listChangedRepo(nil))
	t.Cleanup(svc.Close)
	hub := &recordingUserHub{}
	svc.deps.UserUpdateHub = hub

	now := time.Now().UTC()
	svc.publishDaemonHeartbeat(context.Background(), "user-1", "daemon-1", now, &reliantv1.DaemonHeartbeat{Health: degradedHB("full")})
	svc.publishDaemonHeartbeat(context.Background(), "user-1", "daemon-1", now, &reliantv1.DaemonHeartbeat{})

	hub.mu.Lock()
	defer hub.mu.Unlock()
	require.Len(t, hub.events, 2)
	var degraded, old map[string]any
	require.NoError(t, json.Unmarshal(hub.events[0].Payload.Data, &degraded))
	require.NoError(t, json.Unmarshal(hub.events[1].Payload.Data, &old))
	assert.Equal(t, "degraded", degraded["health_state"])
	assert.Equal(t, "ok", old["health_state"])
}
