//go:build liveprobe

// Copyright (c) 2025 Reliant Labs
package daemonruntime

// In-process timings of the daemon-side work behind two latency complaints,
// against a real directory on this machine. Pairs with the round-trip harness
// in internal/toolexec/live_daemon_latency_test.go, which measures the same
// commands through NATS, the gateway and a running daemon.
//
//	PROBE_PATH=/path/to/project go test -tags liveprobe -run TestLive -v -count=1 ./internal/toolexec/daemonruntime/

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"
)

// TestLiveCodePresenceScanLatency times the project.code_presence scan.
func TestLiveCodePresenceScanLatency(t *testing.T) {
	root := os.Getenv("PROBE_PATH")
	if root == "" {
		t.Skip("PROBE_PATH not set")
	}
	const n = 20
	samples := make([]time.Duration, 0, n)
	var (
		last  codePresenceResponse
		stats codePresenceScanStats
	)
	for i := 0; i < n; i++ {
		start := time.Now()
		resp, st, err := scanCodePresence(context.Background(), root)
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		samples = append(samples, time.Since(start))
		last, stats = resp, st
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	t.Logf("scanCodePresence(%s) n=%d min=%s p50=%s p90=%s max=%s hasCode=%v dirsRead=%d filesVisited=%d",
		root, n, samples[0], samples[n/2], samples[n*9/10], samples[n-1], last.HasCode, stats.dirsRead, stats.filesVisited)
}

// TestLiveProjectSnapshotLatency times the config snapshot the daemon builds
// for every project named in a RegistrationAck. The first run is cold.
func TestLiveProjectSnapshotLatency(t *testing.T) {
	root := os.Getenv("PROBE_PATH")
	if root == "" {
		t.Skip("PROBE_PATH not set")
	}
	for i := 0; i < 3; i++ {
		start := time.Now()
		snap, err := buildProjectSnapshot(root)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		t.Logf("buildProjectSnapshot(%s) run %d: %s skills=%d encodedBytes=%d",
			root, i, time.Since(start), len(snap.GetSkills()), len(snap.String()))
	}
}
