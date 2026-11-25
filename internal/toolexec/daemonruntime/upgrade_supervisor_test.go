// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
)

// desktopDaemon is a person's daemon the desktop app spawned, whose stdout
// is buf.
func desktopDaemon(buf *bytes.Buffer) *daemonClient {
	d := newTestDaemonClient("d-desktop", "u-desktop")
	d.bootCfg.Supervisor = daemonstate.SupervisorElectron
	d.upgrade.supervisorOut = buf
	return d
}

func securityRequest() *reliantv1.RestartRequested {
	return &reliantv1.RestartRequested{
		TargetVersion:  "1.40.0",
		TargetCommit:   "abc123",
		CommitTimeUnix: 1_790_000_000,
		Urgency:        reliantv1.RestartUrgency_RESTART_URGENCY_SECURITY,
		DeadlineUnixMs: 1_790_000_600_000,
		RolloutId:      "roll-7",
		Reason:         "a security fix",
		UpgradeCommand: "brew upgrade --cask reliant",
		UpgradeUrl:     "https://reliantlabs.io/download",
	}
}

// noticeLines are the upgrade notices printed to buf.
func noticeLines(buf *bytes.Buffer) []string {
	var out []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(line, daemonstate.UpgradeNoticePrefix) {
			out = append(out, line)
		}
	}
	return out
}

// The line's exact bytes are the contract the desktop app parses
// (electron/src/daemon-contract.js parseDaemonUpgradeNotice): a rename here
// is a desktop app that no longer hears of upgrades. Fields may be added, at
// the end, never renamed or removed.
func TestDesktopDaemonPrintsTheUpgradeNoticeShape(t *testing.T) {
	var buf bytes.Buffer
	d := desktopDaemon(&buf)

	d.handleRestartRequested(context.Background(), securityRequest())

	assert.Equal(t,
		`@@RELIANT_UPGRADE {"target_version":"1.40.0","target_commit":"abc123","commit_time_unix":1790000000,`+
			`"urgency":"security","deadline_unix_ms":1790000600000,"rollout_id":"roll-7","reason":"a security fix",`+
			`"upgrade_command":"brew upgrade --cask reliant","upgrade_url":"https://reliantlabs.io/download"}`+"\n",
		buf.String())
	// The notice changes nothing else: the daemon awaits its owner and
	// keeps serving (D6).
	assert.Equal(t, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_AWAITING_OWNER, upgradeState(d))
	assert.False(t, d.upgrade.refusing())
}

// Every field is present even when empty, so the desktop app needs no
// defaults; an unset urgency is named, not omitted.
func TestDesktopUpgradeNoticeCarriesEveryField(t *testing.T) {
	var buf bytes.Buffer
	d := desktopDaemon(&buf)

	d.handleRestartRequested(context.Background(), &reliantv1.RestartRequested{})

	assert.Equal(t,
		`@@RELIANT_UPGRADE {"target_version":"","target_commit":"","commit_time_unix":0,"urgency":"unspecified",`+
			`"deadline_unix_ms":0,"rollout_id":"","reason":"","upgrade_command":"","upgrade_url":""}`+"\n",
		buf.String())
}

// The gateway asks again on every new stream: the notice is printed once per
// request, not once per ask, and a changed request is printed again so the
// app can act on it.
func TestDesktopUpgradeNoticeIsPrintedOncePerRequest(t *testing.T) {
	var buf bytes.Buffer
	d := desktopDaemon(&buf)
	ctx := context.Background()

	d.handleRestartRequested(ctx, securityRequest())
	d.handleRestartRequested(ctx, securityRequest())
	d.handleRestartRequested(ctx, securityRequest())
	require.Len(t, noticeLines(&buf), 1, "a repeat of the same request is not printed again")

	sooner := securityRequest()
	sooner.DeadlineUnixMs -= 60_000
	d.handleRestartRequested(ctx, sooner)
	d.handleRestartRequested(ctx, sooner)
	lines := noticeLines(&buf)
	require.Len(t, lines, 2, "a changed request is printed, once")
	assert.Contains(t, lines[1], `"deadline_unix_ms":1790000540000`)
}

// The urgency names the desktop app reads.
func TestRestartUrgencyName(t *testing.T) {
	cases := map[reliantv1.RestartUrgency]string{
		reliantv1.RestartUrgency_RESTART_URGENCY_UNSPECIFIED: "unspecified",
		reliantv1.RestartUrgency_RESTART_URGENCY_ROUTINE:     "routine",
		reliantv1.RestartUrgency_RESTART_URGENCY_NORMAL:      "normal",
		reliantv1.RestartUrgency_RESTART_URGENCY_SECURITY:    "security",
		reliantv1.RestartUrgency_RESTART_URGENCY_EMERGENCY:   "emergency",
	}
	for u, want := range cases {
		assert.Equal(t, want, restartUrgencyName(u), u.String())
	}
}

// Only a daemon the desktop app spawned prints the notice: one a person runs
// in a terminal, or a managed one, has no reader for it.
func TestUpgradeNoticeOnlyForTheDesktopApp(t *testing.T) {
	t.Run("person's terminal daemon", func(t *testing.T) {
		var buf bytes.Buffer
		d := desktopDaemon(&buf)
		d.bootCfg.Supervisor = ""
		d.handleRestartRequested(context.Background(), securityRequest())
		assert.Empty(t, buf.String())
		assert.Equal(t, reliantv1.DaemonUpgradeState_DAEMON_UPGRADE_STATE_AWAITING_OWNER, upgradeState(d))
	})
	t.Run("another supervisor", func(t *testing.T) {
		var buf bytes.Buffer
		d := desktopDaemon(&buf)
		d.bootCfg.Supervisor = "systemd"
		d.handleRestartRequested(context.Background(), securityRequest())
		assert.Empty(t, buf.String())
	})
	t.Run("managed daemon", func(t *testing.T) {
		var buf bytes.Buffer
		d := managedTestDaemon(time.Hour, time.Hour)
		d.bootCfg.Supervisor = daemonstate.SupervisorElectron
		d.upgrade.supervisorOut = &buf
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		d.handleRestartRequested(ctx, securityRequest())
		assert.Empty(t, buf.String(), "a managed daemon drains; its pod is rolled")
	})
}
