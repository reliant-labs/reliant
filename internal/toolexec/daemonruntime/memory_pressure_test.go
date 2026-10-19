// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/cgroupmem"
)

// TestMemWatcherIgnoresReclaimableCache runs the watcher the heartbeat reads
// over the cgroup of a machine measured at memory.current 63.9 of 68.7 GB with
// 1.7 GB anon and the rest page cache and slab. It must not report pressure.
// The same cgroup with the memory held as anon must.
func TestMemWatcherIgnoresReclaimableCache(t *testing.T) {
	const (
		limit = "68719476736\n" // 64 GiB
	)
	cache := "anon 1700000000\nfile 40800000000\ninactive_file 11200000000\nactive_file 29500000000\n" +
		"slab 19300000000\nslab_reclaimable 18500000000\nslab_unreclaimable 800000000\n"
	anon := "anon 60000000000\nfile 2000000000\ninactive_file 1000000000\nactive_file 1000000000\n" +
		"slab 600000000\nslab_reclaimable 500000000\nslab_unreclaimable 100000000\n"

	for _, tt := range []struct {
		name         string
		stat         string
		wantPressure bool
	}{
		{"reclaimable cache and slab", cache, false},
		{"anonymous memory", anon, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range map[string]string{
				"memory.current": "63900000000\n",
				"memory.max":     limit,
				"memory.events":  "low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\n",
				"memory.stat":    tt.stat,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			w := cgroupmem.NewWatcher(cgroupmem.NewReader(dir), time.Hour)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { w.Run(ctx); close(done) }()
			t.Cleanup(func() { cancel(); <-done })

			deadline := time.Now().Add(5 * time.Second)
			for {
				if s, ok := w.Latest(); ok {
					if s.Pressure != tt.wantPressure {
						t.Fatalf("Pressure = %v, want %v (sample %+v)", s.Pressure, tt.wantPressure, s)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatal("watcher produced no sample")
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}
