// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/mergequeue/candidate"
)

func TestTheMergeQueueJobCachesAreAnOwnCacheOfTheDiskWatcher(t *testing.T) {
	w := diskwatch.New(diskwatch.Options{Env: diskwatch.Environment{Getenv: func(string) string { return "" }}})
	registerOwnCaches(w)
	if got := w.OwnCaches(); !slices.Contains(got, pipelineJobCacheName) {
		t.Fatalf("the disk watcher's own caches are %v, want %q among them", got, pipelineJobCacheName)
	}
	if c := pipelineJobCache(pipelineJobs()); c.Path != pipelineJobs().Root || c.Prune == nil {
		t.Fatalf("the own cache is not the job host's caches: %+v", c)
	}
}

// jobCacheAt writes a job cache for acme/<name> under root, last used at t.
func jobCacheAt(t *testing.T, root, name string, at time.Time) string {
	t.Helper()
	dir := filepath.Join(root, "acme", name+".git")
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "objects", "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+".lease", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir+".lease", at, at); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPipelineJobCachePrunesOnTheScheduledAndTheUrgentBudget(t *testing.T) {
	root := t.TempDir()
	h := &candidate.Host{Root: root}
	c := pipelineJobCache(h)
	twoDays := jobCacheAt(t, root, "two-days", time.Now().Add(-48*time.Hour))
	twoWeeks := jobCacheAt(t, root, "two-weeks", time.Now().Add(-14*24*time.Hour))

	c.Prune(t.Context(), false)
	if _, err := os.Stat(twoWeeks); !os.IsNotExist(err) {
		t.Fatalf("a cache unused for two weeks survived the scheduled pass: %v", err)
	}
	if _, err := os.Stat(twoDays); err != nil {
		t.Fatalf("a cache used two days ago was removed by the scheduled pass: %v", err)
	}

	c.Prune(t.Context(), true)
	if _, err := os.Stat(twoDays); !os.IsNotExist(err) {
		t.Fatalf("a cache unused for two days survived the urgent pass: %v", err)
	}
}
