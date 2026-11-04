// Copyright (c) 2026 Reliant Labs
package machinehealth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func attrs(kv []any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func fakeProc(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "loadavg"), "48.00 32.00 16.00 3/1234 5678\n")
	writeFile(t, filepath.Join(root, "pressure", "cpu"), "some avg10=91.50 avg60=80.25 avg300=70.00 total=1\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	writeFile(t, filepath.Join(root, "pressure", "memory"), "some avg10=1.00 avg60=2.50 avg300=3.00 total=1\nfull avg10=0.50 avg60=1.25 avg300=1.00 total=1\n")
	// no pressure/io: a missing file is left out, never logged as 0.
	writeFile(t, filepath.Join(root, "100", "cmdline"), "go\x00install\x00github.com/reliant-labs/forge/cmd/forge@v1.0.15\x00")
	writeFile(t, filepath.Join(root, "101", "cmdline"), "/usr/local/go/bin/go\x00install\x00-trimpath\x00github.com/reliant-labs/forge/cmd/forge@abc123\x00")
	writeFile(t, filepath.Join(root, "102", "cmdline"), "go\x00build\x00github.com/reliant-labs/forge/cmd/forge\x00")
	writeFile(t, filepath.Join(root, "103", "cmdline"), "go\x00install\x00golang.org/x/tools/gopls@latest\x00")
	writeFile(t, filepath.Join(root, "self", "cmdline"), "go\x00install\x00github.com/reliant-labs/forge/cmd/forge@v1\x00")
	return root
}

func TestSampleReadsLoadPressureDiskSlotsAndForgeInstalls(t *testing.T) {
	s := New(Config{
		ProcRoot: fakeProc(t),
		CPUs:     16,
		Disk: func() (*reliantv1.DiskReport, bool) {
			return &reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{
				{Label: "home", TotalBytes: 1000, FreeBytes: 500},
				{Label: "build caches & docker", TotalBytes: 1000, FreeBytes: 50},
				{Label: "unsized"},
			}}, true
		},
		Slots: func() (Slots, bool) {
			return Slots{Capacity: 4, Held: 4, Waiting: 3, OldestWait: 95 * time.Second}, true
		},
	})

	health, volumes := s.Sample()
	h := attrs(health)
	assert.Equal(t, 48.0, h["load1"])
	assert.Equal(t, 3.0, h["load1_per_cpu"])
	assert.Equal(t, 2.0, h["load5_per_cpu"])
	assert.Equal(t, 80.25, h["psi_cpu_some_avg60"])
	assert.Equal(t, 2.5, h["psi_memory_some_avg60"])
	assert.Equal(t, 1.25, h["psi_memory_full_avg60"])
	assert.NotContains(t, h, "psi_io_some_avg60", "a missing PSI file is not a zero")
	assert.Equal(t, 0.05, h["disk_min_free_ratio"])
	assert.Equal(t, "build caches & docker", h["disk_min_free_volume"])
	assert.Equal(t, 4, h["heavy_slots"])
	assert.Equal(t, 3, h["heavy_waiting"])
	assert.Equal(t, int64(95), h["heavy_oldest_wait_s"])
	assert.Equal(t, 2, h["forge_installs"], "only `go install .../forge...@v` counts, once per process")

	require.Len(t, volumes, 2, "a volume with no size is not reported")
	assert.Equal(t, "home", attrs(volumes[0])["volume"])
	assert.Equal(t, 0.5, attrs(volumes[0])["free_ratio"])
}

func TestLogOnceWritesTheHealthLineThenOnePerVolume(t *testing.T) {
	var msgs []string
	s := New(Config{
		ProcRoot: fakeProc(t),
		Disk: func() (*reliantv1.DiskReport, bool) {
			return &reliantv1.DiskReport{Volumes: []*reliantv1.DiskVolume{{Label: "home", TotalBytes: 10, FreeBytes: 1}}}, true
		},
		Log: func(msg string, _ ...any) { msgs = append(msgs, msg) },
	})
	s.LogOnce()
	assert.Equal(t, []string{HealthMessage, VolumeMessage}, msgs)
}

func TestRunReturnsAtOnceWithoutLoadavg(t *testing.T) {
	logged := false
	s := New(Config{ProcRoot: t.TempDir(), Interval: time.Millisecond, Log: func(string, ...any) { logged = true }})
	done := make(chan struct{})
	go func() { s.Run(t.Context()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return on a machine without /proc/loadavg")
	}
	assert.False(t, logged)
}
