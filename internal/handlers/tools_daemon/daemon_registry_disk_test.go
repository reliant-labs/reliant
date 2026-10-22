// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/diskwatch"
)

// clearCachesRouter answers disk.clear_caches the way a daemon does.
type clearCachesRouter struct {
	localModelRouter
	reply diskwatch.ClearCachesResponse
}

func (r *clearCachesRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	_, _ = r.localModelRouter.SendDaemonCommandToDaemon(ctx, userID, daemonID, commandType, payload, timeoutMs)
	return json.Marshal(r.reply)
}

// A heartbeat's disk report travels gateway -> daemon row -> registry, and is
// still there after the machine disconnects: a full disk is still full when
// it resumes.
func TestDiskReport_HeartbeatToRegistryAndKeptWhileOffline(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	seedDaemon(t, repo, "user-1", "daemon-1", true)

	gw := newTestService(repo)
	t.Cleanup(gw.Close)
	conn := newTestConn("user-1", "daemon-1", newParkedStream())
	gw.recordDiskReport(context.Background(), conn, &reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{
		{Label: "build caches & docker", Paths: []string{"/var/lib/docker"}, TotalBytes: 40 << 30, FreeBytes: 0, Pressure: true, LastNoSpaceAtMs: 1760000000000},
		{Label: "home", Paths: []string{"/home/workspace"}, TotalBytes: 157 << 30, FreeBytes: 124 << 30},
	}})

	reg := newDaemonRegistry(t, repo, nil)
	list, err := reg.ListDaemons(userCtx("user-1"), connect.NewRequest(&reliantv1.ListDaemonsRequest{}))
	require.NoError(t, err)
	require.Len(t, list.Msg.Daemons, 1)
	disk := list.Msg.Daemons[0].GetDisk()
	require.NotNil(t, disk, "ListDaemons carries the disk report")
	require.Len(t, disk.Volumes, 2)
	assert.True(t, disk.Volumes[0].Pressure)
	assert.EqualValues(t, 1760000000000, disk.Volumes[0].LastNoSpaceAtMs)
	assert.NotZero(t, disk.ReportedAtMs)

	// The session ends: release every attachment row up to now (#722 made a
	// release take the session's attached_at, so it cannot drop a newer one).
	require.NoError(t, repo.ReleaseDaemonAttachment(context.Background(), "daemon-1", time.Now()))
	got, err := reg.GetDaemon(userCtx("user-1"), connect.NewRequest(&reliantv1.GetDaemonRequest{DaemonId: "daemon-1"}))
	require.NoError(t, err)
	require.NotNil(t, got.Msg.Daemon.GetDisk(), "the report outlives the connection")
	assert.True(t, got.Msg.Daemon.GetDisk().Volumes[0].Pressure)
}

// ClearMachineCaches relays the user's confirmed kinds to the machine and
// reports what it started and refused; it never decides safety itself.
func TestClearMachineCaches_RelaysToTheMachine(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	seedDaemon(t, repo, "user-1", "daemon-1", true)
	seedDaemon(t, repo, "user-1", "daemon-off", false)
	router := &clearCachesRouter{reply: diskwatch.ClearCachesResponse{
		Started: []string{diskwatch.KindGoBuildCache},
		Skipped: []diskwatch.ClearSkip{{Kind: diskwatch.KindDockerVolumes, Reason: "not clearable"}},
	}}
	reg := newDaemonRegistry(t, repo, router)

	resp, err := reg.ClearMachineCaches(userCtx("user-1"), connect.NewRequest(&reliantv1.ClearMachineCachesRequest{
		DaemonId: "daemon-1", Kinds: []string{diskwatch.KindGoBuildCache, diskwatch.KindDockerVolumes},
	}))
	require.NoError(t, err)
	assert.Equal(t, []string{diskwatch.KindGoBuildCache}, resp.Msg.Started)
	require.Len(t, resp.Msg.Skipped, 1)
	assert.Equal(t, "not clearable", resp.Msg.Skipped[0].Reason)
	require.Len(t, router.commands, 1)
	assert.Equal(t, diskwatch.ClearCachesCommand, router.commands[0].commandType)
	assert.JSONEq(t, `{"kinds":["go_build_cache","docker_volumes"]}`, string(router.commands[0].payload))

	_, err = reg.ClearMachineCaches(userCtx("user-2"), connect.NewRequest(&reliantv1.ClearMachineCachesRequest{DaemonId: "daemon-1", Kinds: []string{"go_build_cache"}}))
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "another user's machine")
	_, err = reg.ClearMachineCaches(userCtx("user-1"), connect.NewRequest(&reliantv1.ClearMachineCachesRequest{DaemonId: "daemon-off", Kinds: []string{"go_build_cache"}}))
	assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err), "an offline machine")
	_, err = reg.ClearMachineCaches(userCtx("user-1"), connect.NewRequest(&reliantv1.ClearMachineCachesRequest{DaemonId: "daemon-1"}))
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
}
