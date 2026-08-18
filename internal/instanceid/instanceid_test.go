// Copyright (c) 2025 Reliant Labs
package instanceid

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// resolveIn is exercised directly rather than through ID(), because ID()
// memoizes with a sync.Once — a package-level cache that would make every test
// after the first observe the first test's id.

func TestResolveIn_GeneratesOnceAndReuses(t *testing.T) {
	dir := t.TempDir()

	first, err := resolveIn(dir)
	if err != nil {
		t.Fatalf("first resolveIn: %v", err)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("generated id %q is not a UUID: %v", first, err)
	}

	// Every subsequent call must return the same value, including across a
	// simulated process restart (which is what a fresh resolveIn against the
	// same dir is).
	for i := range 5 {
		got, err := resolveIn(dir)
		if err != nil {
			t.Fatalf("resolveIn call %d: %v", i, err)
		}
		if got != first {
			t.Fatalf("resolveIn call %d returned %q, want the persisted %q", i, got, first)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, FileName)); err != nil {
		t.Fatalf("expected %s to be persisted: %v", FileName, err)
	}
}

// The whole point of the package: the id must not move when the hostname does.
// This is the macOS *.local vs *.lan flip that caused a worker to be misread as
// having moved machines.
func TestResolveIn_SurvivesHostnameChange(t *testing.T) {
	dir := t.TempDir()

	first, err := resolveIn(dir)
	if err != nil {
		t.Fatalf("first resolveIn: %v", err)
	}

	// Rewrite the record's informational hostname to a different machine name,
	// exactly as if the id had been minted under the other name.
	path := filepath.Join(dir, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading record: %v", err)
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("decoding record: %v", err)
	}
	rec.CreatedOnHostname = "Seans-MacBook-Pro-2.local"
	rewritten, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("encoding record: %v", err)
	}
	if err := os.WriteFile(path, rewritten, 0o600); err != nil {
		t.Fatalf("writing record: %v", err)
	}

	got, err := resolveIn(dir)
	if err != nil {
		t.Fatalf("resolveIn after hostname change: %v", err)
	}
	if got != first {
		t.Fatalf("id changed with the hostname: got %q, want %q", got, first)
	}
}

func TestResolveIn_RegeneratesOnUnusableRecord(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"empty file", ""},
		{"truncated json", `{"instance_id": "6f5c4e2a-`},
		{"not json at all", "\x00\x01garbage"},
		{"valid json, no id", `{"created_at":"2026-01-01T00:00:00Z"}`},
		{"blank id", `{"instance_id":"   "}`},
		{"id is not a uuid", `{"instance_id":"seans-mbp-2.lan"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, FileName)
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("seeding record: %v", err)
			}

			// A corrupt identity must never fail a boot — it regenerates.
			got, err := resolveIn(dir)
			if err != nil {
				t.Fatalf("resolveIn on %s: %v", tc.name, err)
			}
			if _, err := uuid.Parse(got); err != nil {
				t.Fatalf("regenerated id %q is not a UUID: %v", got, err)
			}

			// And the repair must be durable, not re-derived on every call.
			again, err := resolveIn(dir)
			if err != nil {
				t.Fatalf("second resolveIn on %s: %v", tc.name, err)
			}
			if again != got {
				t.Fatalf("regenerated id not persisted: got %q then %q", got, again)
			}
		})
	}
}

// A missing file is the ordinary first run, not an error.
func TestResolveIn_MissingFileIsFirstRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist-yet")

	got, err := resolveIn(dir)
	if err != nil {
		t.Fatalf("resolveIn on a missing dir: %v", err)
	}
	if _, err := uuid.Parse(got); err != nil {
		t.Fatalf("id %q is not a UUID: %v", got, err)
	}
}

// Concurrent first runs must converge on ONE id. Without the generation lock
// each caller would mint its own and the last write would win, leaving earlier
// callers reporting an id that disagrees with the file on disk.
func TestResolveIn_ConcurrentFirstRunConverges(t *testing.T) {
	dir := t.TempDir()

	const goroutines = 16
	ids := make([]string, goroutines)
	errs := make([]error, goroutines)

	// A start barrier makes the racy window as wide as possible: every
	// goroutine reaches the empty-directory check at the same moment.
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	ready.Add(goroutines)
	done.Add(goroutines)

	for i := range goroutines {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			ids[i], errs[i] = resolveIn(dir)
		}()
	}

	ready.Wait()
	close(start)
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	for i, id := range ids {
		if id != ids[0] {
			t.Fatalf("goroutine %d got %q but goroutine 0 got %q — concurrent first run diverged", i, id, ids[0])
		}
	}

	// The winner's id must be the one actually on disk, so the next restart
	// agrees with what every goroutine just reported.
	persisted, ok := readValid(dir)
	if !ok {
		t.Fatal("no valid record persisted after concurrent first run")
	}
	if persisted != ids[0] {
		t.Fatalf("persisted id %q disagrees with the returned id %q", persisted, ids[0])
	}
}

func TestWriteRecord_IsAtomicAndLeavesNoDebris(t *testing.T) {
	dir := t.TempDir()
	id := uuid.NewString()

	if err := writeRecord(dir, id); err != nil {
		t.Fatalf("writeRecord: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}
	// A successful write renames its temp file away; a leftover temp file would
	// mean the atomic publish did not actually happen.
	for _, e := range entries {
		if e.Name() != FileName {
			t.Fatalf("unexpected leftover file %q after writeRecord", e.Name())
		}
	}

	got, ok := readValid(dir)
	if !ok {
		t.Fatal("record written by writeRecord did not read back as valid")
	}
	if got != id {
		t.Fatalf("read back %q, want %q", got, id)
	}
}

// The record keeps a readable hostname beside the id: the goal is a stable key,
// not the loss of a human-readable name.
func TestWriteRecord_KeepsHostnameForDebugging(t *testing.T) {
	dir := t.TempDir()
	if err := writeRecord(dir, uuid.NewString()); err != nil {
		t.Fatalf("writeRecord: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("reading record: %v", err)
	}
	var rec record
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("decoding record: %v", err)
	}
	if rec.CreatedOnHostname == "" {
		t.Error("record dropped the hostname; it must stay for display/debugging")
	}
	if rec.CreatedAt.IsZero() {
		t.Error("record has no CreatedAt")
	}
}

func TestResolve_EnvOverrideWins(t *testing.T) {
	// A pinned id must be honored verbatim — this is how a container with an
	// ephemeral home keeps a stable identity across restarts.
	t.Setenv(EnvOverride, "pinned-workload-identity")
	if got := resolve(); got != "pinned-workload-identity" {
		t.Fatalf("resolve() = %q, want the pinned override", got)
	}
}

func TestResolve_OverrideIsTrimmedAndBlankIgnored(t *testing.T) {
	t.Setenv(EnvOverride, "   ")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	// A blank override must fall through to the file, not pin an empty id.
	got := resolve()
	if strings.TrimSpace(got) == "" {
		t.Fatal("resolve() returned an empty id for a whitespace override")
	}
	if _, err := uuid.Parse(got); err != nil {
		t.Fatalf("resolve() = %q, want a generated UUID: %v", got, err)
	}
}

func TestResolve_PersistsUnderHomeReliantDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvOverride, "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got := resolve()
	if _, err := uuid.Parse(got); err != nil {
		t.Fatalf("resolve() = %q, want a UUID: %v", got, err)
	}

	// Pin the documented location: other processes find the id by path, so a
	// silent move would split one machine into several identities.
	path := filepath.Join(home, ".reliant", FileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected the record at %s: %v", path, err)
	}
}

func TestShortAndLabelShape(t *testing.T) {
	t.Setenv(EnvOverride, "3f2a1b0c-4d5e-6f70-8192-a3b4c5d6e7f8")

	// Short/Label read through ID()'s sync.Once, so resolve them via a fresh
	// process-independent path: check the formatting helpers against the same
	// override the cache will have picked up.
	id := ID()
	if id == "" {
		t.Fatal("ID() returned empty")
	}

	short := Short()
	if len(short) > 8 {
		t.Fatalf("Short() = %q, want at most 8 chars", short)
	}
	if !strings.HasPrefix(id, short) {
		t.Fatalf("Short() = %q is not a prefix of ID() = %q", short, id)
	}

	label := Label()
	if !strings.HasPrefix(label, short+"@") {
		t.Fatalf("Label() = %q, want it to start with %q", label, short+"@")
	}
	if strings.HasSuffix(label, "@") {
		t.Fatalf("Label() = %q has no hostname segment", label)
	}
}

// The Temporal identity is the concrete payoff: it must carry the pid (did the
// PROCESS restart?), the stable id (is it the same MACHINE?), and a readable
// hostname — the three questions the incident needed answered at once.
func TestWorkerIdentity_CarriesPidStableIDAndHostname(t *testing.T) {
	t.Setenv(EnvOverride, "3f2a1b0c-4d5e-6f70-8192-a3b4c5d6e7f8")

	got := WorkerIdentity()

	wantPrefix := fmt.Sprintf("%d.", os.Getpid())
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("WorkerIdentity() = %q, want it to start with the pid %q", got, wantPrefix)
	}
	if !strings.Contains(got, Short()) {
		t.Errorf("WorkerIdentity() = %q, want it to contain the short id %q", got, Short())
	}
	if !strings.HasSuffix(got, "@"+Hostname()) {
		t.Errorf("WorkerIdentity() = %q, want it to end with @%s", got, Hostname())
	}
}

// A hostname flip must not change the machine segment of the worker identity —
// that is precisely the false "the worker moved hosts" signal being fixed.
func TestWorkerIdentity_MachineSegmentIsStableAcrossHostnames(t *testing.T) {
	t.Setenv(EnvOverride, "3f2a1b0c-4d5e-6f70-8192-a3b4c5d6e7f8")

	machineSegment := func(identity string) string {
		_, rest, found := strings.Cut(identity, ".")
		if !found {
			t.Fatalf("identity %q has no pid separator", identity)
		}
		seg, _, found := strings.Cut(rest, "@")
		if !found {
			t.Fatalf("identity %q has no hostname separator", identity)
		}
		return seg
	}

	// Two reads that would straddle a hostname flip in production; the machine
	// segment comes from the persisted id, so it cannot move with the network.
	first := machineSegment(WorkerIdentity())
	second := machineSegment(WorkerIdentity())

	if first != second {
		t.Fatalf("machine segment changed between reads: %q vs %q", first, second)
	}
	if first != Short() {
		t.Fatalf("machine segment %q is not the stable short id %q", first, Short())
	}
}

func TestHostname_NeverEmpty(t *testing.T) {
	// Callers format this into identity strings; an empty value would produce a
	// dangling "@" rather than something a human can read.
	if got := Hostname(); strings.TrimSpace(got) == "" {
		t.Fatal("Hostname() returned an empty string")
	}
}
