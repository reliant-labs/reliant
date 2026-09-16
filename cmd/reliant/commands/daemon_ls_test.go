// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/toolexec/daemonstate"
	"github.com/stretchr/testify/require"
)

func TestPrintDaemonInstancesSeparatesRunningFromRecorded(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	instances := []daemonstate.Instance{
		{
			Slug:      "http-localhost-8090-aaaaaaaa/_default/reliant-11111111",
			Locked:    true,
			HasRecord: true,
			Record: daemonstate.State{
				PID:         4242,
				StartedAt:   now.Add(-90 * time.Second),
				Stream:      daemonstate.StreamConnected,
				ConnectedAt: now.Add(-90 * time.Second),
				Instance:    "http-localhost-8090-aaaaaaaa/_default/reliant-11111111",
			},
		},
		{
			// A record whose process is gone. Reporting this as running is the
			// bug the flock probe exists to prevent.
			Slug:      "http-localhost-8090-aaaaaaaa/sub-bbbbbbbb/reliant-22222222",
			Locked:    false,
			HasRecord: true,
			Record: daemonstate.State{
				PID:       999999,
				StartedAt: now.Add(-3 * time.Hour),
				Stream:    daemonstate.StreamDisconnected,
				Instance:  "http-localhost-8090-aaaaaaaa/sub-bbbbbbbb/reliant-22222222",
			},
		},
		{
			// Alive but has not published a record yet.
			Slug:   "https-staging-cccccccc/_default/reliant-33333333",
			Locked: true,
		},
	}

	var out bytes.Buffer
	printDaemonInstances(&out, instances, "/home/dev/.reliant/instances", now)
	rendered := out.String()

	require.Contains(t, rendered, "/home/dev/.reliant/instances")
	require.Contains(t, rendered, "RUNNING")
	require.Contains(t, rendered, "4242")
	require.Contains(t, rendered, "connected")
	require.Contains(t, rendered, "1m30s")
	require.Contains(t, rendered, "disconnected")
	require.Contains(t, rendered, "no record")

	// Each instance is listed under its own key, never merged.
	for _, instance := range instances {
		require.Contains(t, rendered, instance.Slug)
	}
	require.NotContains(t, rendered, "WARNING")
}

// A record that names an instance other than the directory holding it must be
// surfaced, not rendered as an ordinary row — acting on it would mean acting on
// a different daemon than the one you asked about.
func TestPrintDaemonInstancesFlagsAForeignRecord(t *testing.T) {
	now := time.Now().UTC()
	var out bytes.Buffer
	printDaemonInstances(&out, []daemonstate.Instance{{
		Slug:      "origin-aaaaaaaa/_default/ws-bbbbbbbb",
		Locked:    true,
		HasRecord: true,
		Record: daemonstate.State{
			PID:      7,
			Stream:   daemonstate.StreamConnected,
			Instance: "origin-cccccccc/_default/ws-dddddddd",
		},
	}}, "/root", now)

	rendered := out.String()
	require.Contains(t, rendered, "WARNING")
	require.Contains(t, rendered, "claims instance origin-cccccccc/_default/ws-dddddddd")
}

// An established stream that dropped moments ago will drop again mid-run. A
// bare "connected" would read as healthy.
func TestDescribeInstanceStreamMarksAFlappingStream(t *testing.T) {
	now := time.Now().UTC()

	steady := daemonstate.State{Stream: daemonstate.StreamConnected, ConnectedAt: now.Add(-time.Hour)}
	require.Equal(t, "connected", describeInstanceStream(steady, now))

	flapping := daemonstate.State{
		Stream:           daemonstate.StreamConnected,
		ConnectedAt:      now.Add(-5 * time.Second),
		LastDisconnectAt: now.Add(-7 * time.Second),
		Sessions:         6,
	}
	require.Equal(t, "connected (flapping)", describeInstanceStream(flapping, now))

	require.Equal(t, "unknown", describeInstanceStream(daemonstate.State{}, now))
}

// `ls` is a diagnostic. It must not be able to kill, stop, or clear anything.
func TestDaemonLsTakesNoArgumentsAndOnlyReports(t *testing.T) {
	cmd := newDaemonLsCmd()
	require.Equal(t, "ls", cmd.Name())
	require.NoError(t, cmd.Args(cmd, nil))
	require.Error(t, cmd.Args(cmd, []string{"some-instance"}),
		"ls must not accept a target — it reports on everything and acts on nothing")
	require.Nil(t, cmd.Flags().Lookup("force"))
	require.Nil(t, cmd.Flags().Lookup("kill"))
}
