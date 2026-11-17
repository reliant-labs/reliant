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

// TestSourcesRefusesTheRealCacheUnderTest is the guard on the accident
// that motivated SourceCacheRoot: four tests in this package call
// GC(apply=true) to assert things about Docker endpoints, and before this
// refusal existed the source layer silently reclaimed five of the
// developer's real clones as a side effect.
//
// The refusal, not a convention, is the fix. A test that has no reason to
// think about a source cache cannot be expected to remember to scope one.
func TestSourcesRefusesTheRealCacheUnderTest(t *testing.T) {
	var out strings.Builder
	r := Runner{Policy: DefaultPolicy(), Out: &out}
	if err := r.Sources(true); err != nil {
		t.Fatalf("Sources must skip, not fail, so it never aborts an unrelated GC assertion: %v", err)
	}
	if !strings.Contains(out.String(), "SourceCacheRoot is unset under test") {
		t.Fatalf("the skip must say what to set:\n%s", out.String())
	}
	// And nothing was reclaimed: the real cache was never even read.
	if strings.Contains(out.String(), "evict source cache") {
		t.Fatalf("an unscoped Sources() planned real evictions:\n%s", out.String())
	}
}

// TestSourcesReclaimsWithinAnExplicitRoot is the positive path: given a
// root, the layer evicts through it and reports what it did.
func TestSourcesReclaimsWithinAnExplicitRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake lsof through shell subprocesses; runs in task test")
	}
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	root := filepath.Join(t.TempDir(), "sources")
	for name, age := range map[string]time.Duration{
		"app-aaaaaaaaaaaa": 0,
		"app-bbbbbbbbbbbb": time.Hour,
		"app-cccccccccccc": 90 * 24 * time.Hour,
	} {
		entry := filepath.Join(root, name)
		if err := os.MkdirAll(entry, 0o755); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(entry, gitsource.MetadataFile)
		if err := os.WriteFile(marker, []byte(`{"repo":"github.com/acme/app","ref":"v1"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(marker, at, at); err != nil {
			t.Fatal(err)
		}
	}

	var out strings.Builder
	r := Runner{Policy: DefaultPolicy(), Out: &out, SourceCacheRoot: root}
	if err := r.Sources(true); err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "app-cccccccccccc")); err == nil {
		t.Error("the stale entry survived")
	}
	for _, kept := range []string{"app-aaaaaaaaaaaa", "app-bbbbbbbbbbbb"} {
		if _, err := os.Stat(filepath.Join(root, kept)); err != nil {
			t.Errorf("%s was evicted but is within the keep floor: %v", kept, err)
		}
	}
}

// TestSourcesAppliesThePolicyBudgets is the guard on the OTHER half of the
// wiring: Sources passed gitsource.EvictPolicy{} for a while, so every
// production pass silently ran on gitsource's defaults and the policy fields
// were decoration. Two entries of one repository, one fresh and one 2h old:
//
//   - under the DEFAULT policy (336h unused, keep 2) the stale one is retained
//     TWICE over — it is inside the keep floor AND inside the age window;
//   - under a policy of 1h unused / keep 1 it is outside both.
//
// So an eviction here is only possible if BOTH policy numbers reached Evict:
// the default MaxAge alone would retain it on age, and the default KeepPerSlug
// alone would retain it on rank.
func TestSourcesAppliesThePolicyBudgets(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake lsof through shell subprocesses; runs in task test")
	}
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	root := filepath.Join(t.TempDir(), "sources")
	stale := filepath.Join(root, "app-bbbbbbbbbbbb")
	for name, age := range map[string]time.Duration{
		"app-aaaaaaaaaaaa": 0,
		"app-bbbbbbbbbbbb": 2 * time.Hour,
	} {
		marker := filepath.Join(root, name, gitsource.MetadataFile)
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

	var byDefault strings.Builder
	r := Runner{Policy: DefaultPolicy(), Out: &byDefault, SourceCacheRoot: root}
	if err := r.Sources(false); err != nil {
		t.Fatalf("Sources under the default policy: %v", err)
	}
	if strings.Contains(byDefault.String(), "evict source cache") {
		t.Fatalf("the default policy planned an eviction of a 2h-old entry inside the keep floor:\n%s", byDefault.String())
	}

	tight := DefaultPolicy()
	tight.SourceCacheUnused = "1h"
	tight.SourceCacheKeep = 1
	var byPolicy strings.Builder
	r = Runner{Policy: tight, Out: &byPolicy, SourceCacheRoot: root}
	if err := r.Sources(true); err != nil {
		t.Fatalf("Sources under a tight policy: %v", err)
	}
	if !strings.Contains(byPolicy.String(), "evict source cache") {
		t.Fatalf("source_cache_unused=1h/source_cache_keep=1 did not reach Evict:\n%s", byPolicy.String())
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("the entry the policy doomed survived apply=true")
	}
	if _, err := os.Stat(filepath.Join(root, "app-aaaaaaaaaaaa")); err != nil {
		t.Errorf("the keep floor must still retain the newest entry: %v", err)
	}
}

// TestSourcesSurvivesAnUnparseableUnusedWindow pins the direction the fallback
// errs in. Validate rejects a bad duration and GC validates first, so this is
// unreachable through the CLI — but a hand-built Runner must not read a
// malformed window as "evict everything".
func TestSourcesSurvivesAnUnparseableUnusedWindow(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sources")
	for _, name := range []string{"app-aaaaaaaaaaaa", "app-bbbbbbbbbbbb", "app-cccccccccccc"} {
		marker := filepath.Join(root, name, gitsource.MetadataFile)
		if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte(`{"repo":"github.com/acme/app","ref":"v1"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p := DefaultPolicy()
	p.SourceCacheUnused = "not-a-duration"
	var out strings.Builder
	r := Runner{Policy: p, Out: &out, SourceCacheRoot: root}
	if err := r.Sources(true); err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if strings.Contains(out.String(), "evict source cache") {
		t.Fatalf("a malformed window evicted fresh entries:\n%s", out.String())
	}
}

// TestGCRunsTheSourceLayer pins the wiring: the layer has to be reachable
// from GC, or none of the above ever runs in production.
func TestGCRunsTheSourceLayer(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake lsof through shell subprocesses; runs in task test")
	}
	fakeLsof(t, unrelatedOpenFile, "exit 0")
	root := filepath.Join(t.TempDir(), "sources")
	// Three entries of one repo: the default keep floor is 2, so a single
	// stale entry would be RETAINED on rank and prove nothing about the
	// wiring. The third is what makes a plan appear at all.
	entry := filepath.Join(root, "app-cccccccccccc")
	for name, age := range map[string]time.Duration{
		"app-aaaaaaaaaaaa": 0,
		"app-bbbbbbbbbbbb": time.Hour,
		"app-cccccccccccc": 90 * 24 * time.Hour,
	} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(root, name, gitsource.MetadataFile)
		if err := os.WriteFile(marker, []byte(`{"repo":"github.com/acme/app","ref":"v1"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(marker, at, at); err != nil {
			t.Fatal(err)
		}
	}

	var out strings.Builder
	// GC stops at the Docker endpoint check, which is AFTER the source
	// layer — so a plan in the output proves the layer ran, without this
	// test needing a Docker daemon.
	r := Runner{
		Policy:          DefaultPolicy(),
		Out:             &out,
		SourceCacheRoot: root,
		Command: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`[{"Endpoints":{"docker":{"Host":"ssh://prod"}}}]`), nil
		},
	}
	if err := r.GC(context.Background(), false); err == nil {
		t.Fatal("expected the remote-endpoint refusal")
	}
	if !strings.Contains(out.String(), "evict source cache") {
		t.Fatalf("GC did not run the source layer:\n%s", out.String())
	}
	// Preview: the entry must still be there.
	if _, err := os.Stat(entry); err != nil {
		t.Fatalf("GC(apply=false) removed %s: %v", entry, err)
	}
}
