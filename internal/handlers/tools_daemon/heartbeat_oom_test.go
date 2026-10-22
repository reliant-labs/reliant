// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// The heartbeat a web client receives carries the workspace's OOM kills, so
// the app can say a process was killed at the memory limit even when no tool
// call ran it (a dev server, a docker build). A workspace with none sends no
// key, and the app shows nothing.
func TestPublishDaemonHeartbeat_CarriesOOMKills(t *testing.T) {
	svc := newTestService(listChangedRepo(nil))
	t.Cleanup(svc.Close)
	hub := &recordingUserHub{}
	svc.deps.UserUpdateHub = hub

	killedAt := time.Date(2026, 10, 19, 3, 23, 31, 0, time.UTC)
	svc.publishDaemonHeartbeat(context.Background(), "user-1", "daemon-1", killedAt, &reliantv1.DaemonHeartbeat{
		MemoryUsedBytes: 60 << 30, MemoryLimitBytes: 64 << 30, OomKills: 2, LastOomKillAtMs: killedAt.UnixMilli(),
	})
	svc.publishDaemonHeartbeat(context.Background(), "user-1", "daemon-1", killedAt, &reliantv1.DaemonHeartbeat{
		MemoryUsedBytes: 1 << 30, MemoryLimitBytes: 64 << 30,
	})

	hub.mu.Lock()
	defer hub.mu.Unlock()
	require.Len(t, hub.events, 2)
	var withKills, without map[string]any
	require.NoError(t, json.Unmarshal(hub.events[0].Payload.Data, &withKills))
	require.NoError(t, json.Unmarshal(hub.events[1].Payload.Data, &without))
	assert.EqualValues(t, 2, withKills["oom_kills"])
	assert.EqualValues(t, killedAt.UnixMilli(), withKills["last_oom_kill_at_ms"])
	assert.NotContains(t, without, "oom_kills")
	assert.NotContains(t, without, "last_oom_kill_at_ms")
}
