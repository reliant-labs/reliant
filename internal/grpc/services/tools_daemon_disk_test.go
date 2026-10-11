// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/diskwatch"
)

// diskStateRepo records the disk reports the gateway stores on the daemon row.
type diskStateRepo struct {
	db.Repository
	mu     sync.Mutex
	stored []string
}

func (r *diskStateRepo) SetDaemonStorageState(_ context.Context, _ string, state string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stored = append(r.stored, state)
	return nil
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
	svc := NewToolsDaemonService(repo)
	t.Cleanup(svc.Close)
	hub := &recordingUserHub{}
	svc.SetUserUpdateHub(hub)
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
