// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/diskwatch"
)

// diskStateRepo records the disk reports the gateway stores on the daemon row.
type diskStateRepo struct {
	Repository
	mu     sync.Mutex
	stored []string
}

var _ Repository = (*diskStateRepo)(nil)

func (r *diskStateRepo) SetDaemonStorageState(_ context.Context, _ string, state string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stored = append(r.stored, state)
	return nil
}

func (r *diskStateRepo) GetDaemonStorageState(context.Context, string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.stored) == 0 {
		return "", nil
	}
	return r.stored[len(r.stored)-1], nil
}

func (r *diskStateRepo) writes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.stored...)
}

// The heartbeat's disk report reaches the daemon row, where the registry and
// the storage inbox item read it — throttled to material changes, with the UI
// told to refetch only when what it must show at once changed.
func TestRecordDiskReport_StoresMaterialChangesAndAnnouncesUrgentOnes(t *testing.T) {
	repo := &diskStateRepo{}
	svc := newTestService(repo)
	t.Cleanup(svc.Close)
	hub := &recordingUserHub{}
	svc.deps.UserUpdateHub = hub
	conn := newTestConn("user-1", "daemon-1", newParkedStream())

	vol := &reliantv1.DiskVolume{Label: "build caches & docker", TotalBytes: 40 << 30, FreeBytes: 5 << 30}
	rep := &reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{vol}}

	svc.recordDiskReport(context.Background(), conn, rep)
	require.Len(t, repo.writes(), 1, "the first report is stored")
	stored := diskwatch.Decode(repo.writes()[0])
	require.NotNil(t, stored)
	assert.NotZero(t, stored.ReportedAtMs, "the gateway stamps its own clock")
	assert.Equal(t, "build caches & docker", stored.Volumes[0].Label)
	assert.Len(t, daemonListRefetches(t, hub)["user-1"], 1)

	// The next heartbeat with the same picture is not a write.
	svc.recordDiskReport(context.Background(), conn, rep)
	assert.Len(t, repo.writes(), 1)

	// Free space moving by 2% of the volume is material, but not urgent.
	vol.FreeBytes -= 1 << 30
	svc.recordDiskReport(context.Background(), conn, rep)
	assert.Len(t, repo.writes(), 2)
	assert.Len(t, daemonListRefetches(t, hub)["user-1"], 1)

	// The volume coming under pressure is urgent: clients refetch now.
	vol.FreeBytes, vol.Pressure = 0, true
	svc.recordDiskReport(context.Background(), conn, rep)
	assert.Len(t, repo.writes(), 3)
	assert.Len(t, daemonListRefetches(t, hub)["user-1"], 2)

	// An unchanged picture is still rewritten once it is old, so the
	// reported-at the UI shows stays honest.
	conn.lastDiskWrite = time.Now().Add(-diskReportRefresh - time.Second)
	svc.recordDiskReport(context.Background(), conn, rep)
	assert.Len(t, repo.writes(), 4)
}

// The pressure episode — what the storage inbox item is keyed by — is carried
// from the stored row, not the connection: a machine that reconnects (a cloud
// machine resuming from suspend) with the same full disk is still in the same
// episode, so a dismissed storage item stays dismissed.
func TestRecordDiskReport_PressureEpisodeOutlivesTheConnection(t *testing.T) {
	repo := &diskStateRepo{}
	svc := newTestService(repo)
	t.Cleanup(svc.Close)
	ctx := context.Background()
	vol := &reliantv1.DiskVolume{Label: "build caches & docker", TotalBytes: 40 << 30, FreeBytes: 1 << 30, Pressure: true}
	rep := &reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{vol}}
	last := func() *reliantv1.DiskReport {
		w := repo.writes()
		require.NotEmpty(t, w)
		return diskwatch.Decode(w[len(w)-1])
	}

	svc.recordDiskReport(ctx, newTestConn("user-1", "daemon-1", newParkedStream()), rep)
	since := last().PressureSinceMs
	require.NotZero(t, since, "pressure starts an episode")

	time.Sleep(2 * time.Millisecond)
	conn := newTestConn("user-1", "daemon-1", newParkedStream())
	svc.recordDiskReport(ctx, conn, rep)
	require.Len(t, repo.writes(), 2, "a new connection's first report is stored")
	assert.Equal(t, since, last().PressureSinceMs, "a reconnect does not start a new episode")

	vol.FreeBytes, vol.Pressure = 12<<30, false
	svc.recordDiskReport(ctx, conn, rep)
	assert.Zero(t, last().PressureSinceMs, "the pressure clearing ends the episode")

	time.Sleep(2 * time.Millisecond)
	vol.FreeBytes, vol.Pressure = 1<<30, true
	svc.recordDiskReport(ctx, conn, rep)
	assert.Greater(t, last().PressureSinceMs, since, "pressure after a clear is a new episode")
}
