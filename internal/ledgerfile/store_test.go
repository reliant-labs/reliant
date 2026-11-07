package ledgerfile

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/pkg/release"
)

// openTest returns a store in a temp home. No t.Setenv anywhere in this
// file: the home is a constructor argument precisely so these tests can run
// in parallel.
func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), "proj-abc123")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func promotion(env, version string) release.Promotion {
	return release.Promotion{
		Env:      env,
		Release:  version,
		Kind:     release.KindPromote,
		Resolved: map[string]string{"api": "sha256:" + strings.Repeat("a", 64)},
	}
}

func ociRelease(version string) release.Release {
	return release.Release{
		Version:   version,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		Artifacts: map[string]release.Artifact{
			"ghcr.io/acme/api": {
				Kind:    release.KindOCI,
				Mode:    release.ModeShared,
				Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("b", 64)},
			},
		},
	}
}

// decideOnly is the admission rule with no CAS: release.Decide alone, which
// is what a plain promote asserts.
func decideOnly(history []release.Promotion, p release.Promotion) (*release.Promotion, error) {
	return release.Decide(history, p)
}

// TestConcurrentAppendersProduceOneLine is the race the retired in-checkout
// backend documented and declined to close: two writers both decide from the
// same history and both append. Under the lock, N concurrent appenders of the
// SAME move must produce exactly ONE line, and every loser must get back the
// winner's entry rather than an error.
func TestConcurrentAppendersProduceOneLine(t *testing.T) {
	t.Parallel()
	s := openTest(t)

	const writers = 16
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		got  []release.Promotion
		errs []error
	)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := s.AppendPromotion(promotion("prod", "v1.0.0"), decideOnly)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			got = append(got, p)
		}()
	}
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("no appender should fail; got %d errors, first: %v", len(errs), errs[0])
	}
	history, err := s.Promotions("prod")
	if err != nil {
		t.Fatalf("Promotions: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("%d concurrent appenders of one move wrote %d lines, want exactly 1 "+
			"(two writers deciding from the same history is the race the lock closes)", writers, len(history))
	}
	// Every caller saw the same entry: the one on disk.
	for _, p := range got {
		if p.ID != history[0].ID {
			t.Fatalf("an appender returned promotion %q but the ledger holds %q", p.ID, history[0].ID)
		}
	}
}

// TestConcurrentDistinctMovesAllLand is the other half: concurrency must not
// LOSE writes either. Distinct releases are distinct moves, so each one is a
// real append and all of them must survive.
func TestConcurrentDistinctMovesAllLand(t *testing.T) {
	t.Parallel()
	s := openTest(t)

	const writers = 8
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// Each goroutine promotes its own env, so no two are the
			// same move and none is an idempotent no-op.
			if _, err := s.AppendPromotion(promotion(fmt.Sprintf("env%d", n), "v1.0.0"), decideOnly); err != nil {
				t.Errorf("append env%d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < writers; i++ {
		env := fmt.Sprintf("env%d", i)
		cur, ok, err := s.CurrentPromotion(env)
		if err != nil || !ok {
			t.Fatalf("%s: CurrentPromotion = (%v, %v, %v), want a promotion", env, cur, ok, err)
		}
	}
}

// TestConcurrentAppendersAcrossProcesses proves the lock is a FILE lock, not
// a mutex: separate processes must serialize too. That is the case the
// in-checkout backend could never have handled, and it is the realistic one
// (two terminals, a CI job beside a laptop).
func TestConcurrentAppendersAcrossProcesses(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("spawns subprocesses; covered in full mode")
	}
	home := t.TempDir()
	helper := buildAppendHelper(t)

	const procs = 8
	var wg sync.WaitGroup
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(helper, home, "proj-abc123", "prod", "v2.0.0")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("helper: %v: %s", err, out)
			}
		}()
	}
	wg.Wait()

	s, err := Open(home, "proj-abc123")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	history, err := s.Promotions("prod")
	if err != nil {
		t.Fatalf("Promotions: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("%d concurrent PROCESSES appending one move wrote %d lines, want exactly 1 "+
			"(an in-process mutex would pass this test while a real second forge still raced)", procs, len(history))
	}
}

// buildAppendHelper compiles a tiny program that performs one append, so the
// cross-process test exercises a genuinely separate process holding the
// flock rather than another goroutine.
func buildAppendHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	program := `package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/pkg/release"
)

func main() {
	s, err := ledgerfile.Open(os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	p := release.Promotion{
		Env:      os.Args[3],
		Release:  os.Args[4],
		Kind:     release.KindPromote,
		Resolved: map[string]string{"api": "sha256:" + strings.Repeat("a", 64)},
	}
	if _, err := s.AppendPromotion(p, release.Decide); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`
	if err := os.WriteFile(src, []byte(program), 0o600); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	bin := filepath.Join(dir, "appender")
	// Built inside the module so the helper resolves the same packages.
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Dir = moduleDir(t)
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS="))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build the cross-process helper here: %v: %s", err, out)
	}
	return bin
}

func moduleDir(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Skipf("cannot locate the module root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestAppendRefusesOnFailedCAS pins that the guard is applied against the
// history read UNDER THE LOCK: a promote asserting a stale current promotion
// is refused and appends nothing.
func TestAppendRefusesOnFailedCAS(t *testing.T) {
	t.Parallel()
	s := openTest(t)

	first, err := s.AppendPromotion(promotion("prod", "v1.0.0"), decideOnly)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}

	refuse := errors.New("CAS failed")
	// An admission rule that asserts the current promotion is "stale-id".
	stale := func(history []release.Promotion, p release.Promotion) (*release.Promotion, error) {
		if existing, err := release.Decide(history, p); err != nil || existing != nil {
			return existing, err
		}
		if len(history) > 0 && history[len(history)-1].ID != "stale-id" {
			return nil, refuse
		}
		return nil, nil
	}
	if _, err := s.AppendPromotion(promotion("prod", "v2.0.0"), stale); !errors.Is(err, refuse) {
		t.Fatalf("a promote whose CAS does not match must be refused, got %v", err)
	}
	history, err := s.Promotions("prod")
	if err != nil {
		t.Fatalf("Promotions: %v", err)
	}
	if len(history) != 1 || history[0].ID != first.ID {
		t.Fatalf("a refused promote must append nothing; history = %+v", history)
	}
}

// TestIdempotentRepromoteAppendsNothing pins release.Decide's rule in this
// store: re-promoting the release an env already runs returns the EXISTING
// entry and writes no line.
func TestIdempotentRepromoteAppendsNothing(t *testing.T) {
	t.Parallel()
	s := openTest(t)

	first, err := s.AppendPromotion(promotion("prod", "v1.0.0"), decideOnly)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	again, err := s.AppendPromotion(promotion("prod", "v1.0.0"), decideOnly)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("a retry must return the existing entry %q, got %q", first.ID, again.ID)
	}
	history, _ := s.Promotions("prod")
	if len(history) != 1 {
		t.Fatalf("a retry must append nothing; %d lines", len(history))
	}
}

// TestRecutRules pins release.CheckRecut through this store: the same
// content is an idempotent retry, different content under one version label
// is a conflict.
func TestRecutRules(t *testing.T) {
	t.Parallel()
	s := openTest(t)

	r := ociRelease("v1.4.0")
	created, err := s.CutRelease(r)
	if err != nil || !created {
		t.Fatalf("first cut = (%v, %v), want created", created, err)
	}

	created, err = s.CutRelease(r)
	if err != nil {
		t.Fatalf("re-cutting the SAME content must be an idempotent retry, got %v", err)
	}
	if created {
		t.Fatal("a re-cut of identical content must report created=false")
	}

	different := ociRelease("v1.4.0")
	different.Artifacts["ghcr.io/acme/api"] = release.Artifact{
		Kind:    release.KindOCI,
		Mode:    release.ModeShared,
		Digests: map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("c", 64)},
	}
	if _, err := s.CutRelease(different); !errors.Is(err, release.ErrReleaseConflict) {
		t.Fatalf("one version label naming two digest sets must be ErrReleaseConflict, got %v", err)
	}

	// The conflict must not have corrupted what was recorded.
	got, err := s.Release("v1.4.0")
	if err != nil || got == nil {
		t.Fatalf("Release = (%v, %v)", got, err)
	}
	if !got.SameContent(r) {
		t.Fatal("a refused re-cut must leave the recorded release unchanged")
	}
}

// TestCanonicalJSONMatchesPkgRelease is the "a file line IS what ImportLedger
// accepts" property. A line must round-trip to an equal record, and must be
// byte-identical to the encoding pkg/release's own consumers produce.
func TestCanonicalJSONMatchesPkgRelease(t *testing.T) {
	t.Parallel()
	s := openTest(t)

	// A note with HTML-significant bytes: json.Marshal escapes <, > and &
	// to \u003c-style sequences, which protojson does not. If the store
	// used the default encoder, this line would differ from the wire's.
	p := promotion("prod", "v1.0.0")
	p.Note = `rollback <urgent> & manual: a>b`
	written, err := s.AppendPromotion(p, decideOnly)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(s.Dir(), "promotions", "prod.jsonl"))
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	line := strings.TrimSpace(string(raw))

	if strings.Contains(line, `\u003c`) || strings.Contains(line, `\u0026`) {
		t.Fatalf("the line is HTML-escaped, so it is not the canonical encoding the wire uses:\n%s", line)
	}
	if !strings.Contains(line, "<urgent>") {
		t.Fatalf("the note's literal bytes must survive:\n%s", line)
	}

	want, err := canonicalJSON(written)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	if line != string(want) {
		t.Fatalf("the line on disk is not the canonical encoding of the record:\n got %s\nwant %s", line, want)
	}

	// And it decodes back to an equal, valid record.
	history, err := s.Promotions("prod")
	if err != nil {
		t.Fatalf("Promotions: %v", err)
	}
	if len(history) != 1 || history[0].Note != p.Note || history[0].ID != written.ID {
		t.Fatalf("round trip lost data: %+v", history)
	}
}

// TestMalformedLineIsAnError pins the rule that a ledger with an unreadable
// line is not a ledger whose newest line can be trusted as "current".
// Silently skipping it would answer "never promoted" for a promoted env.
func TestMalformedLineIsAnError(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	if _, err := s.AppendPromotion(promotion("prod", "v1.0.0"), decideOnly); err != nil {
		t.Fatalf("append: %v", err)
	}
	path := filepath.Join(s.Dir(), "promotions", "prod.jsonl")
	if err := os.WriteFile(path, []byte("{not json}\n"), 0o600); err != nil {
		t.Fatalf("corrupt the log: %v", err)
	}
	if _, err := s.Promotions("prod"); err == nil {
		t.Fatal("a malformed ledger line must be an error, never a skipped line")
	}
	if _, _, err := s.CurrentPromotion("prod"); err == nil {
		t.Fatal("CurrentPromotion must surface the malformed line rather than report 'never promoted'")
	}
}

// TestEnvNameCannotEscapeTheLedger pins the containment guard: a hostile env
// name must not write outside the ledger directory.
func TestEnvNameCannotEscapeTheLedger(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	s, err := Open(home, "proj")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.AppendPromotion(promotion("../../escaped", "v1.0.0"), decideOnly); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "escaped.jsonl")); err == nil {
		t.Fatal("an env name traversed out of the ledger directory")
	}
	entries, err := os.ReadDir(filepath.Join(s.Dir(), "promotions"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("the log must be inside the ledger; ReadDir = (%v, %v)", entries, err)
	}
}
