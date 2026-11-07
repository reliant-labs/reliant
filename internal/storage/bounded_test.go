package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/gitsource"
)

// TestNonDisruptiveGCHonorsItsDeadlineInsideALayer is M3. The opportunistic
// pass's 2-minute context reached only the docker calls: the temp sweep and
// source eviction each ran lsof on their own 2-minute background context and
// then deleted on whatever it said, so `forge env up` could sit for minutes
// past its budget — and, on a first run, delete ~117 GB synchronously before
// returning. The deadline must bound the whole pass, and a layer cut off by
// it must fail closed, not act on an unfinished check.
func TestNonDisruptiveGCHonorsItsDeadlineInsideALayer(t *testing.T) {
	tempRoot := t.TempDir()
	tempEntry := writeTempEntry(t, tempRoot, "go-link-bounded", 48*time.Hour)
	sources := filepath.Join(t.TempDir(), "sources")
	for name, age := range map[string]time.Duration{"app-aaaaaaaaaaaa": 0, "app-bbbbbbbbbbbb": 90 * 24 * time.Hour} {
		marker := filepath.Join(sources, name, gitsource.MetadataFile)
		if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte(`{"repo":"github.com/acme/app","ref":"v1"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(marker, at, at); err != nil {
			t.Fatal(err)
		}
	}
	staleSource := filepath.Join(sources, "app-bbbbbbbbbbbb")
	// An lsof that takes longer than the pass is allowed to run.
	fakeLsof(t, unrelatedOpenFile, "exec sleep 2")

	p := DefaultPolicy()
	p.Builders = nil
	p.SourceCacheKeep = 1
	r := Runner{
		Policy: p, TempRoot: tempRoot, SourceCacheRoot: sources,
		Command: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[{"Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}]`), nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := r.NonDisruptiveGC(ctx, true)
	elapsed := time.Since(start)

	if elapsed > 1500*time.Millisecond {
		t.Errorf("the pass ran %s against a 300ms budget", elapsed.Round(time.Millisecond))
	}
	if _, statErr := os.Stat(tempEntry); statErr != nil {
		t.Errorf("the temp sweep deleted after its deadline passed (%v)", statErr)
	}
	if _, statErr := os.Stat(staleSource); statErr != nil {
		t.Errorf("source eviction deleted after the deadline passed (%v)", statErr)
	}
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Errorf("a pass cut off by its deadline must say so: %v", err)
	}
}
