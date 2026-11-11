package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRegistryRetentionProtectsDigestAliasesAndReleaseTags(t *testing.T) {
	now := time.Now()
	p := DefaultPolicy()
	p.RegistryKeep = 2
	reg := Registry{Repositories: []string{"app"}}
	tags := []Tag{{"app", "new", "new", now}, {"app", "second", "second", now.Add(-time.Hour)}, {"app", "deployed", "keep", now.Add(-365 * 24 * time.Hour)}, {"app", "old-alias", "keep", now.Add(-100 * 24 * time.Hour)}, {"app", "v1.2.3", "release", now.Add(-365 * 24 * time.Hour)}, {"app", "expired", "drop", now.Add(-60 * 24 * time.Hour)}, {"unregistered/config.v1/app", "old", "artifact", now.Add(-60 * 24 * time.Hour)}}
	got := RegistryCandidates(tags, p, reg, map[string]map[string]bool{"app": {"deployed": true}}, now)
	if len(got) != 1 || got[0].Digest != "drop" {
		t.Fatalf("unsafe plan: %+v", got)
	}
}

func TestRegistryVersionFloorCountsDistinctDigests(t *testing.T) {
	now := time.Now()
	p := DefaultPolicy()
	p.RegistryKeep = 2
	tags := []Tag{{"app", "a", "one", now}, {"app", "alias", "one", now}, {"app", "b", "two", now.Add(-30 * 24 * time.Hour)}, {"app", "c", "three", now.Add(-40 * 24 * time.Hour)}}
	got := RegistryCandidates(tags, p, Registry{Repositories: []string{"app"}}, nil, now)
	if len(got) != 1 || got[0].Digest != "three" {
		t.Fatalf("plan: %+v", got)
	}
}

func TestRecentImagesSurviveRegardlessOfRank(t *testing.T) {
	now := time.Now()
	p := DefaultPolicy()
	p.RegistryKeep = 2
	var tags []Tag
	for i := 0; i < 20; i++ {
		tags = append(tags, Tag{"app", fmt.Sprint(i), fmt.Sprint(i), now.Add(-time.Duration(i) * time.Hour)})
	}
	if got := RegistryCandidates(tags, p, Registry{Repositories: []string{"app"}}, nil, now); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestPolicyRejectsUnsafeValues(t *testing.T) {
	for _, change := range []func(*Policy){func(p *Policy) { p.RegistryKeep = 1 }, func(p *Policy) { p.HostReserveGiB = 0 }, func(p *Policy) { p.ImageUnused = "0s" }, func(p *Policy) { p.SourceCacheUnused = "30m" }, func(p *Policy) { p.SourceCacheUnused = "" }, func(p *Policy) { p.SourceCacheKeep = 0 }, func(p *Policy) { p.Registries = []Registry{{Container: "remote"}} }} {
		p := DefaultPolicy()
		change(&p)
		if p.Validate() == nil {
			t.Fatalf("accepted %+v", p)
		}
	}
}

// TestPolicyFileWithoutSourceCacheKeysGetsDefaults pins the compatibility
// contract for every policy.json already on a developer's disk. Load decodes
// over DefaultPolicy with DisallowUnknownFields, so a document that omits the
// source-cache keys inherits the defaults — and must still Validate, because
// Load returns Validate's error.
func TestPolicyFileWithoutSourceCacheKeysGetsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"registry_keep":3}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("a policy predating the source-cache keys no longer loads: %v", err)
	}
	if p.SourceCacheUnused != "168h" || p.SourceCacheKeep != 2 {
		t.Fatalf("source cache budgets = %q/%d, want the defaults 168h/2", p.SourceCacheUnused, p.SourceCacheKeep)
	}
	if p.RegistryKeep != 3 {
		t.Fatalf("the document's own value was lost: registry_keep = %d", p.RegistryKeep)
	}
}

// A storage.json written by a different forge version (or carrying a retired
// or misspelled key) must not fail unrelated commands: it loads, the known
// keys are honoured, and the unknown one is named in a warning.
func TestUnknownPolicyFieldToleratedWithWarning(t *testing.T) {
	var warned strings.Builder
	prev := PolicyWarnings
	PolicyWarnings = &warned
	t.Cleanup(func() { PolicyWarnings = prev })
	path := filepath.Join(t.TempDir(), "storage.json")
	doc := `{"registry_keep":3,"go_cache_unused":"48h","go_cache_gib":60,"from_the_future":true,"go_mod_cache_gib":20,"tool_cache_unused":"336h","tool_cache_keep":1}`
	if err := os.WriteFile(path, []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("storage.json with unknown key must load: %v", err)
	}
	if p.RegistryKeep != 3 {
		t.Fatalf("known key lost: registry_keep = %d", p.RegistryKeep)
	}
	// Retired keys are what older forges wrote; they load silently.
	got := warned.String()
	if !strings.Contains(got, "from_the_future") || strings.Contains(got, "go_cache_unused") ||
		strings.Contains(got, "go_mod_cache_gib") || strings.Contains(got, "tool_cache") {
		t.Fatalf("warning should name only the unknown key, got %q", got)
	}
}

func TestKubeletHasAgeGCIndependentOfPressure(t *testing.T) {
	var config map[string]any
	_ = json.Unmarshal(DefaultPolicy().KubeletConfig(), &config)
	if config["imageMaximumGCAge"] != "168h" {
		t.Fatal(config)
	}
	if config["imageGCLowThresholdPercent"].(float64) >= config["imageGCHighThresholdPercent"].(float64) {
		t.Fatal(config)
	}
}

func TestHostReserveChecked(t *testing.T) {
	p := DefaultPolicy()
	p.HostReserveGiB = 1 << 30
	dir := t.TempDir()
	_, err := CheckSpace(p, dir)
	if err == nil {
		t.Fatal("unavailable reserve accepted")
	}
	// A measured shortfall is the one outcome a caller (the CI admission
	// path) may knowingly accept, so it must be distinguishable from a disk
	// that could not be read at all.
	var short *ReserveError
	if !errors.As(err, &short) {
		t.Fatalf("a shortfall is not a *ReserveError: %T %v", err, err)
	}
	if short.Path != dir || short.ReserveGiB != 1<<30 || short.Available == 0 {
		t.Errorf("shortfall = %+v, want the checked path, the policy reserve and the measured free bytes", short)
	}
}

func TestGCRejectsRemoteDockerBeforeMutation(t *testing.T) {
	var calls []string
	r := Runner{Policy: DefaultPolicy(), Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return []byte(`[{"Endpoints":{"docker":{"Host":"ssh://prod"}}}]`), nil
	}}
	if err := r.GC(context.Background(), true); err == nil {
		t.Fatal("remote accepted")
	}
	if len(calls) != 1 {
		t.Fatal(calls)
	}
}

func TestGCRejectsRemoteBuilder(t *testing.T) {
	r := Runner{Policy: DefaultPolicy(), Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "context" {
			return []byte(`[{"Endpoints":{"docker":{"Host":"unix:///var/run/docker.sock"}}}]`), nil
		}
		if args[0] == "ps" || (len(args) > 1 && (args[0] == "image" || args[0] == "volume") && args[1] == "ls") {
			return nil, nil
		}
		if len(args) > 1 && args[1] == "inspect" {
			return []byte(`{"Driver":"docker-container","Nodes":[{"Endpoint":"ssh://prod"}]}`), nil
		}
		t.Fatalf("mutation: %v", args)
		return nil, nil
	}}
	if err := r.GC(context.Background(), true); err == nil {
		t.Fatal("remote builder accepted")
	}
}

func TestUnreachableClusterFailsProtection(t *testing.T) {
	r := Runner{Policy: DefaultPolicy(), Command: func(context.Context, string, ...string) ([]byte, error) { return nil, fmt.Errorf("unreachable") }}
	if _, err := r.protected(context.Background(), Registry{Contexts: []string{"k3d-offline"}}); err == nil {
		t.Fatal("unresolved protected set accepted")
	}
}

func TestMaintenanceLockExcludesConcurrentRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := WithLock(path, func() error {
		if err := WithLock(path, func() error { t.Fatal("second writer admitted"); return nil }); err == nil {
			t.Fatal("lock succeeded")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLogRetentionKeepsCurrentStreamsAndRecentDiagnostics(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".forge", "logs", "dev")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 9; i++ {
		name := fmt.Sprintf("worker.2026-01-%02dT00-00-00.log", i+1)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("log"), 0600); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-time.Duration(i+10) * 24 * time.Hour)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"worker.log", "not-a-rotated-log.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := DefaultPolicy()
	p.Projects = []string{root}
	r := Runner{Policy: p}
	if err := r.Logs(false); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadDir(dir)
	if len(before) != 11 {
		t.Fatal("preview mutated logs")
	}
	if err := r.Logs(true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadDir(dir)
	if len(after) != 7 {
		t.Fatalf("want 5 rotated + current + unrelated, got %d", len(after))
	}
	for _, name := range []string{"worker.log", "not-a-rotated-log.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoricalLedgerPinsFailClosed(t *testing.T) {
	dir := t.TempDir()
	digest := "sha256:" + strings.Repeat("a", 64)
	b := []byte(`{"artifacts":{"api":{"digests":{"*":"` + digest + `"}}}}`)
	if err := os.WriteFile(filepath.Join(dir, "v1.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	pins, err := LedgerPins(dir)
	if err != nil || len(pins) != 1 || pins[0] != digest {
		t.Fatalf("pins %v: %v", pins, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "v2.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LedgerPins(dir); err == nil {
		t.Fatal("accepted unreadable ledger")
	}
}

func TestKubeletDurationNormalization(t *testing.T) {
	if !sameDuration("168h0m0s", "168h") || sameDuration("0s", "168h") || sameDuration("bad", "bad") {
		t.Fatal("incorrect kubelet duration comparison")
	}
}

// A release cut must not be turned away because a maintenance pass holds the
// lock for a moment. WithLockWait retries a held lock and runs once it frees.
func TestWithLockWaitRunsOnceTheHolderReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	prev := LockWaitPoll
	LockWaitPoll = 10 * time.Millisecond
	t.Cleanup(func() { LockWaitPoll = prev })

	held := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- WithLock(path, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	// The holder lets go after the waiter has started waiting.
	go func() { time.Sleep(80 * time.Millisecond); close(release) }()

	ran := false
	start := time.Now()
	if err := WithLockWait(path, 5*time.Second, nil, func() error { ran = true; return nil }); err != nil {
		t.Fatalf("a bounded wait for a lock that frees in 80ms failed: %v", err)
	}
	if !ran {
		t.Fatal("the waiter never ran")
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Errorf("the waiter ran after %v, before the holder released: it did not wait", time.Since(start))
	}
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
}

// The wait is BOUNDED: a wedged maintenance pass must not hang a cut forever.
func TestWithLockWaitGivesUpAfterItsBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	prev := LockWaitPoll
	LockWaitPoll = 10 * time.Millisecond
	t.Cleanup(func() { LockWaitPoll = prev })

	err := WithLock(path, func() error {
		start := time.Now()
		werr := WithLockWait(path, 120*time.Millisecond, nil, func() error {
			t.Fatal("ran while the lock was held")
			return nil
		})
		if werr == nil || !strings.Contains(werr.Error(), "already running") {
			t.Fatalf("want the 'already running' error after the bound, got %v", werr)
		}
		if d := time.Since(start); d < 100*time.Millisecond || d > 3*time.Second {
			t.Errorf("gave up after %v, want about the 120ms bound", d)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPolicyFileWithoutSourceBudgetKeysGetsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"registry_keep":3}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("a policy predating the budgets no longer loads: %v", err)
	}
	if p.SourceCacheGiB != 6 || p.SourceCacheSlimAfter != "24h" {
		t.Fatalf("defaults = %d GiB sources, slim %q", p.SourceCacheGiB, p.SourceCacheSlimAfter)
	}
	bad := DefaultPolicy()
	bad.SourceCacheSlimAfter = "30m"
	if bad.Validate() == nil {
		t.Fatal("a slim window under an hour must be rejected")
	}
	if p.GolangciCacheGiB != 4 {
		t.Fatalf("golangci_cache_gib default = %d, want 4", p.GolangciCacheGiB)
	}
	bad = DefaultPolicy()
	bad.GolangciCacheGiB = 0
	if bad.Validate() == nil {
		t.Fatal("an unbounded golangci-lint cache must be rejected")
	}
}

// A maintenance pass gives way to a writer: once one starts waiting, the
// pass's context is cancelled with ErrWriterWaiting, the pass returns, and the
// writer runs well inside its bound.
func TestWithMaintenanceYieldsToAWaitingWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	prev := LockWaitPoll
	LockWaitPoll = 10 * time.Millisecond
	t.Cleanup(func() { LockWaitPoll = prev })

	running := make(chan struct{})
	var cause error
	passDone := make(chan error, 1)
	go func() {
		passDone <- WithMaintenance(t.Context(), path, nil, func(ctx context.Context) error {
			close(running)
			select {
			case <-ctx.Done():
				cause = context.Cause(ctx)
				return ctx.Err()
			case <-time.After(20 * time.Second):
				return nil
			}
		})
	}()
	<-running
	start := time.Now()
	if err := WithLockWait(path, 3*time.Second, nil, func() error { return nil }); err != nil {
		t.Fatalf("the writer failed behind a pass that should have yielded: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("the writer waited %v: the pass did not yield promptly", d)
	}
	if err := <-passDone; !errors.Is(err, context.Canceled) {
		t.Errorf("pass returned %v, want its cancelled context", err)
	}
	if !errors.Is(cause, ErrWriterWaiting) {
		t.Errorf("pass cancelled with cause %v, want ErrWriterWaiting", cause)
	}
	if writerWaiting(path) {
		t.Error("the writer's announcement outlived its wait")
	}
}

// A test binary started from a shell that exports FORGE_STORAGE_POLICY (a
// Reliant agent's does) inherits the HOST's policy. Writing it from a test
// put fake pins in the machine policy and queued release-cut tests behind the
// host's own maintenance pass; the inherited path is refused like the default.
func TestGuardRefusesTheInheritedPolicyPath(t *testing.T) {
	host := filepath.Join(t.TempDir(), "host-storage.json")
	prev := inheritedPolicyPath
	inheritedPolicyPath = host
	t.Cleanup(func() { inheritedPolicyPath = prev })
	t.Setenv("FORGE_STORAGE_POLICY", host)

	if err := WithLock(host, func() error { t.Fatal("ran against the host's policy"); return nil }); !errors.Is(err, ErrMachinePolicyUnderTest) {
		t.Fatalf("WithLock on the inherited policy = %v, want ErrMachinePolicyUnderTest", err)
	}
	fixture := filepath.Join(t.TempDir(), "storage.json")
	t.Setenv("FORGE_STORAGE_POLICY", fixture)
	if err := WithLock(fixture, func() error { return nil }); err != nil {
		t.Fatalf("a test's own policy was refused: %v", err)
	}
}

// The holder record names the CURRENT holder or nobody: it is removed on
// release, so a later holder that records nothing is not misnamed.
func TestLockHolderIsClearedOnRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	var during string
	if err := WithLock(path, func() error { during = lockHolder(path); return nil }); err != nil {
		t.Fatal(err)
	}
	if want := "pid " + strconv.Itoa(os.Getpid()); during != want {
		t.Errorf("holder while held = %q, want %q", during, want)
	}
	if after := lockHolder(path); after != "holder unknown" {
		t.Errorf("holder after release = %q, want it cleared", after)
	}
}
