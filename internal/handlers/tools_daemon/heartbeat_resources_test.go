// Copyright (c) 2026 Reliant Labs
package tools_daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// The gateway stores a heartbeat's resource reading on the lease at most once
// a minute, and again at once after the renewer re-created the lease. A
// heartbeat with no reading (a daemon that predates it) writes nothing.
func TestRecordResourceStats(t *testing.T) {
	var writes []string
	svc := newTestServiceWithLeases(&MockRepository{}, &MockAttachments{
		SetDaemonAttachmentResourcesFunc: func(_ context.Context, daemonID, statsJSON string) error {
			assert.Equal(t, "daemon-1", daemonID)
			writes = append(writes, statsJSON)
			return nil
		},
	}, nil)
	t.Cleanup(svc.Close)
	conn := &daemonConnection{daemonID: "daemon-1"}
	ctx := context.Background()
	t0 := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	rss := uint64(256 << 20)
	hb := &reliantv1.DaemonHeartbeat{
		MachineStats: &reliantv1.MachineStats{Cpus: 4, MemTotalBytes: 1000, MemAvailableBytes: 400},
		ProcessStats: &reliantv1.DaemonProcessStats{RssBytes: &rss, Goroutines: 50},
	}

	svc.recordResourceStats(ctx, conn, &reliantv1.DaemonHeartbeat{}, t0)
	require.Empty(t, writes, "an old daemon's heartbeat reports nothing")

	svc.recordResourceStats(ctx, conn, hb, t0)
	require.Len(t, writes, 1)
	var got reliantv1.DaemonResourceStats
	require.NoError(t, protojson.Unmarshal([]byte(writes[0]), &got))
	assert.True(t, proto.Equal(&reliantv1.DaemonResourceStats{Machine: hb.MachineStats, Process: hb.ProcessStats}, &got))

	svc.recordResourceStats(ctx, conn, hb, t0.Add(45*time.Second))
	require.Len(t, writes, 1, "within the minute")
	svc.recordResourceStats(ctx, conn, hb, t0.Add(time.Minute))
	require.Len(t, writes, 2)

	conn.resourcesWrittenAt = time.Time{} // the lease was re-created
	svc.recordResourceStats(ctx, conn, hb, t0.Add(61*time.Second))
	require.Len(t, writes, 3)
}
