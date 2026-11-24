// Copyright (c) 2026 Reliant Labs
package tools_daemon

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/reliant-labs/forge/pkg/svcerr"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

const (
	floorCommitTime = int64(1_763_000_000)
	floorVersion    = "1.20.0"
)

func testFloorPolicy(enforce bool) *configv1.ClientVersionPolicy {
	return &configv1.ClientVersionPolicy{
		Enforce:                 enforce,
		DaemonMinVersion:        floorVersion,
		DaemonMinCommitTimeUnix: floorCommitTime,
		DaemonUpgradeCommand:    "brew upgrade --cask reliant",
		DaemonUpgradeUrl:        "https://reliantlabs.io/download",
	}
}

// The commit time orders builds first; semver decides only when either side
// has none; a build that can be compared with nothing is unknown.
func TestJudgeDaemonBuild(t *testing.T) {
	t.Parallel()
	both := daemonFloorOf(testFloorPolicy(false))
	versionOnly := daemonFloor{minVersion: floorVersion}
	timeOnly := daemonFloor{minCommitTimeUnix: floorCommitTime}

	for name, tc := range map[string]struct {
		build db.DaemonBuild
		floor daemonFloor
		want  floorVerdict
	}{
		"older commit":                     {build: db.DaemonBuild{CommitTimeUnix: floorCommitTime - 1}, floor: both, want: floorBelow},
		"same commit time":                 {build: db.DaemonBuild{CommitTimeUnix: floorCommitTime}, floor: both, want: floorMet},
		"commit time wins over semver":     {build: db.DaemonBuild{Version: "1.0.0", CommitTimeUnix: floorCommitTime + 1}, floor: both, want: floorMet},
		"newer semver, older commit":       {build: db.DaemonBuild{Version: "9.0.0", CommitTimeUnix: floorCommitTime - 1}, floor: both, want: floorBelow},
		"unordered build, older semver":    {build: db.DaemonBuild{Version: "1.19.3"}, floor: both, want: floorBelow},
		"unordered build, dev of floor":    {build: db.DaemonBuild{Version: "1.20.0-dev+abc123"}, floor: both, want: floorBelow},
		"unordered build, at floor":        {build: db.DaemonBuild{Version: "1.20.0"}, floor: both, want: floorMet},
		"go install build (leading v)":     {build: db.DaemonBuild{Version: "v1.19.0"}, floor: versionOnly, want: floorBelow},
		"bare commit, version floor":       {build: db.DaemonBuild{Version: "8bcfc6ee"}, floor: versionOnly, want: floorUnknown},
		"unknown build":                    {build: db.DaemonBuild{}, floor: both, want: floorUnknown},
		"commit-time floor, unordered":     {build: db.DaemonBuild{Version: "1.0.0"}, floor: timeOnly, want: floorUnknown},
		"no floor":                         {build: db.DaemonBuild{Version: "1.0.0", CommitTimeUnix: 1}, floor: daemonFloor{}, want: floorUnknown},
		"semver floor, newer prerelease":   {build: db.DaemonBuild{Version: "1.21.0-rc.1"}, floor: versionOnly, want: floorMet},
		"commit-time floor, newer commit":  {build: db.DaemonBuild{CommitTimeUnix: floorCommitTime + 100}, floor: timeOnly, want: floorMet},
		"commit-time floor, older commit":  {build: db.DaemonBuild{CommitTimeUnix: 1}, floor: timeOnly, want: floorBelow},
		"dirty version with build suffix":  {build: db.DaemonBuild{Version: "1.19.0.dirty"}, floor: versionOnly, want: floorUnknown},
		"dev build metadata, newer minor":  {build: db.DaemonBuild{Version: "1.20.1-dev+abc.dirty"}, floor: versionOnly, want: floorMet},
		"version floor, empty build field": {build: db.DaemonBuild{CommitTimeUnix: floorCommitTime}, floor: versionOnly, want: floorUnknown},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, judgeDaemonBuild(tc.build, tc.floor))
		})
	}
}

// Who is asked, refused or only observed. A person's machine is told whatever
// enforce says (it never drains); a managed one, and a legacy daemon's
// refusal, wait for enforce; the gateway's own outbound dial is never refused.
func TestDecideFloorAction(t *testing.T) {
	t.Parallel()
	below := func(capable bool) *reliantv1.DaemonRegister {
		return &reliantv1.DaemonRegister{Version: "1.19.0", CommitTimeUnix: floorCommitTime - 1, DrainsOnRestartRequest: capable}
	}
	for name, tc := range map[string]struct {
		reg               *reliantv1.DaemonRegister
		enforce           bool
		managed, outbound bool
		want              floorAction
	}{
		"below, capable, person's machine":          {reg: below(true), want: floorActionRestart},
		"below, capable, person's machine, enforce": {reg: below(true), enforce: true, want: floorActionRestart},
		"below, capable, managed":                   {reg: below(true), managed: true, want: floorActionObserve},
		"below, capable, managed, enforce":          {reg: below(true), managed: true, enforce: true, want: floorActionRestart},
		"below, legacy":                             {reg: below(false), want: floorActionObserve},
		"below, legacy, enforce":                    {reg: below(false), enforce: true, want: floorActionRefuse},
		"below, legacy, outbound, enforce":          {reg: below(false), managed: true, outbound: true, enforce: true, want: floorActionObserve},
		"at the floor":                              {reg: &reliantv1.DaemonRegister{CommitTimeUnix: floorCommitTime, DrainsOnRestartRequest: true}, enforce: true, want: floorActionNone},
		"above the floor, legacy":                   {reg: &reliantv1.DaemonRegister{Version: "1.30.0"}, enforce: true, want: floorActionNone},
		"unknown build":                             {reg: &reliantv1.DaemonRegister{DrainsOnRestartRequest: true}, enforce: true, want: floorActionNone},
		"unknown legacy build":                      {reg: &reliantv1.DaemonRegister{}, enforce: true, want: floorActionNone},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, _ := decideFloorAction(testFloorPolicy(tc.enforce), tc.reg, tc.managed, tc.outbound)
			assert.Equal(t, tc.want, got)
		})
	}
	got, _ := decideFloorAction(nil, below(false), false, false)
	assert.Equal(t, floorActionNone, got, "no policy is no floor")
}

// A daemon registers as managed only when it says so; "cloud" (the dial-in
// transport's fallback) is judged managed too, which only holds a restart back.
func TestRegisteredManaged(t *testing.T) {
	t.Parallel()
	for daemonType, want := range map[string]bool{"managed": true, "cloud": true, "local": false, "self_hosted": false, "": false} {
		assert.Equal(t, want, registeredManaged(&reliantv1.DaemonRegister{DaemonType: daemonType}), daemonType)
	}
}

// drainFor collects what arrives on msgs for d, or until it closes.
func drainFor(msgs <-chan *reliantv1.ServerMessage, d time.Duration) []*reliantv1.ServerMessage {
	var got []*reliantv1.ServerMessage
	timeout := time.After(d)
	for {
		select {
		case msg, ok := <-msgs:
			if !ok {
				return got
			}
			got = append(got, msg)
		case <-timeout:
			return got
		}
	}
}

func restartRequests(msgs []*reliantv1.ServerMessage) []*reliantv1.RestartRequested {
	var out []*reliantv1.RestartRequested
	for _, m := range msgs {
		if r := m.GetRestartRequested(); r != nil {
			out = append(out, r)
		}
	}
	return out
}

// Over the real ConnectDaemon path: a person's daemon below the floor that
// acts on RestartRequested is asked exactly once, after its registration_ack,
// and told how to upgrade; one at the floor is asked nothing.
func TestConnectDaemonAsksABelowFloorDaemonToRestartOnce(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	for name, tc := range map[string]struct {
		commitTime int64
		want       int
	}{
		"below the floor": {commitTime: floorCommitTime - 1, want: 1},
		"at the floor":    {commitTime: floorCommitTime, want: 0},
		"unknown build":   {commitTime: 0, want: 0},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newTestService(repo)
			svc.deps.ClientVersions = testFloorPolicy(false)
			daemonID := uuid.NewString()
			stream := dialDaemonAs(t, svc, "floor-user", &reliantv1.DaemonRegister{
				DaemonId: daemonID, Hostname: "laptop-" + daemonID[:8], Platform: "darwin", DaemonType: "local",
				CommitTimeUnix: tc.commitTime, DrainsOnRestartRequest: true,
			})

			msgs := make(chan *reliantv1.ServerMessage, 64)
			go func() {
				defer close(msgs)
				for {
					msg, err := stream.Receive()
					if err != nil {
						return
					}
					msgs <- msg
				}
			}()
			if tc.want == 0 {
				// Absence: a window that ends early can only miss a request
				// the gateway wrongly sent, never fail a correct gateway.
				require.Empty(t, restartRequests(drainFor(msgs, 300*time.Millisecond)))
				return
			}
			// The request is waited for, not timed: the deadline is only a
			// hang guard, so a loaded runner that delivers it late passes.
			var got []*reliantv1.RestartRequested
			guard := time.After(30 * time.Second)
			for len(got) == 0 {
				select {
				case msg, ok := <-msgs:
					require.True(t, ok, "the stream ended before a RestartRequested arrived")
					got = restartRequests([]*reliantv1.ServerMessage{msg})
				case <-guard:
					t.Fatal("no RestartRequested arrived")
				}
			}
			// One request per registration (sendFloorRestart runs once in
			// ConnectDaemon); a second within the window fails the test.
			require.Empty(t, restartRequests(drainFor(msgs, 300*time.Millisecond)), "the daemon is asked once")
			req := got[0]
			assert.Equal(t, floorRestartReason, req.GetReason())
			assert.Equal(t, floorVersion, req.GetTargetVersion())
			assert.Equal(t, floorCommitTime, req.GetCommitTimeUnix())
			assert.Equal(t, reliantv1.RestartUrgency_RESTART_URGENCY_NORMAL, req.GetUrgency())
			assert.Equal(t, "brew upgrade --cask reliant", req.GetUpgradeCommand())
			assert.Equal(t, "https://reliantlabs.io/download", req.GetUpgradeUrl())
		})
	}
}

// A daemon below the floor that predates RestartRequested is refused only
// under enforce, with the upgrade_required refusal every client reads, before
// the gateway records anything about it (the service has no repository). Its
// reconnect backoff paces the retries: FailedPrecondition is not fatal to it.
func TestConnectDaemonRefusesALegacyBelowFloorDaemonOnlyUnderEnforce(t *testing.T) {
	svc := newTestService(nil)
	svc.deps.ClientVersions = testFloorPolicy(true)
	daemonID := uuid.NewString()
	stream := sendRegistration(t, svc, "floor-user", &reliantv1.DaemonRegister{
		DaemonId: daemonID, Hostname: "old-laptop", DaemonType: "local", Version: "1.19.0", CommitTimeUnix: floorCommitTime - 1,
	})

	_, err := stream.Receive()
	require.Error(t, err)
	var connectErr *connect.Error
	require.True(t, errors.As(err, &connectErr), "got %v", err)
	assert.Equal(t, connect.CodeFailedPrecondition, connectErr.Code())
	assert.Equal(t, upgradeRequiredReason, connectErr.Meta().Get(svcerr.ReasonHeader))
	assert.Contains(t, connectErr.Message(), "this daemon (1.19.0) is older than the minimum this server supports")
	assert.Contains(t, connectErr.Message(), "upgrade: brew upgrade --cask reliant")

	var info *errdetails.ErrorInfo
	for _, d := range connectErr.Details() {
		v, derr := d.Value()
		require.NoError(t, derr)
		if ei, ok := v.(*errdetails.ErrorInfo); ok {
			info = ei
		}
	}
	require.NotNil(t, info, "the refusal carries a google.rpc.ErrorInfo")
	assert.Equal(t, upgradeRequiredInfoReason, info.GetReason())
	assert.Equal(t, upgradeRequiredInfoDomain, info.GetDomain())
	assert.Equal(t, map[string]string{
		"client_kind": "daemon", "client_version": "1.19.0", "min_version": floorVersion,
		"upgrade_url": "https://reliantlabs.io/download", "upgrade_command": "brew upgrade --cask reliant",
	}, info.GetMetadata())

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	assert.Empty(t, svc.connections, "a refused daemon holds no connection")
}
