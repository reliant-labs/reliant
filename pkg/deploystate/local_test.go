package deploystate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalPutGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewLocal(t.TempDir())

	rec := Record{
		Key:      Key{Env: "prod", Provider: "k8s", Service: "api"},
		Desired:  Desired{Digest: "sha256:aaa", Image: "ghcr.io/x/api", Tag: "v1", Replicas: intp(3)},
		Observed: Observation{Digest: "sha256:aaa", Replicas: intp(3), Ready: intp(3), Serving: true, Measured: true},
		Owned:    []Field{FieldDigest},
	}
	if err := store.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(ctx, rec.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Desired.Digest != rec.Desired.Digest || got.Observed.Digest != rec.Observed.Digest {
		t.Fatalf("round trip lost digests: %+v", got)
	}
	if !got.Observed.Measured || !got.Observed.Serving {
		t.Fatalf("round trip lost the observation flags: %+v", got.Observed)
	}
	if got.Observed.Replicas == nil || *got.Observed.Replicas != 3 {
		t.Fatalf("round trip lost replicas: %+v", got.Observed.Replicas)
	}
	if !got.Owns(FieldDigest) {
		t.Fatalf("round trip lost ownership: %v", got.Owned)
	}
}

// TestLocalNilReplicasStayNil: nil and a pointer to zero mean different
// things ("not applicable" vs "nothing running"), and a JSON round trip
// must not collapse them.
func TestLocalNilReplicasStayNil(t *testing.T) {
	ctx := context.Background()
	store := NewLocal(t.TempDir())

	key := Key{Env: "prod", Provider: "staticsite", Service: "web"}
	if err := store.Put(ctx, Record{
		Key:      key,
		Observed: Observation{Measured: true, Serving: true},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Observed.Replicas != nil {
		t.Fatalf("nil replicas became %v; a static site has no replica concept", *got.Observed.Replicas)
	}

	zero := Record{Key: key, Observed: Observation{Measured: true, Replicas: intp(0)}}
	if err := store.Put(ctx, zero); err != nil {
		t.Fatalf("Put zero: %v", err)
	}
	gotZero, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get zero: %v", err)
	}
	if gotZero.Observed.Replicas == nil {
		t.Fatal("a deliberate zero replica count decoded as nil; that is 'not applicable', not 'scaled to nothing'")
	}
}

func TestLocalGetMissingIsErrNotFound(t *testing.T) {
	ctx := context.Background()
	store := NewLocal(t.TempDir())

	_, err := store.Get(ctx, Key{Env: "prod", Provider: "k8s", Service: "nope"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on a missing record: err = %v, want ErrNotFound", err)
	}
}

// TestLocalPolicyDefaultsToObserveWithNoFile is the guarantee that a
// brand-new project cannot converge. No file on disk, no server, no
// configuration: observe.
func TestLocalPolicyDefaultsToObserveWithNoFile(t *testing.T) {
	ctx := context.Background()
	store := NewLocal(t.TempDir())

	p, err := store.Policy(ctx, "prod")
	if err != nil {
		t.Fatalf("Policy: %v", err)
	}
	if p != PolicyObserve {
		t.Fatalf("Policy with no file = %v, want PolicyObserve", p)
	}
	if p.AllowsConverge() {
		t.Fatal("a project with no policy file allows convergence")
	}
}

func TestLocalPolicyRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewLocal(t.TempDir())

	for _, want := range []Policy{PolicyConverge, PolicyPinned, PolicyObserve} {
		if err := store.SetPolicy(ctx, "prod", want); err != nil {
			t.Fatalf("SetPolicy(%v): %v", want, err)
		}
		got, err := store.Policy(ctx, "prod")
		if err != nil {
			t.Fatalf("Policy: %v", err)
		}
		if got != want {
			t.Fatalf("Policy = %v, want %v", got, want)
		}
	}
}

// TestLocalPolicyIsReReadEveryCall is the mechanism behind "opting out
// must be instant and must not require a deploy". The store is
// constructed ONCE and the file is edited underneath it by hand — the
// same thing an engineer does mid-incident — and the very next call must
// return the new value.
//
// The hand-written file is deliberate: an engineer in an incident edits
// JSON, they do not call SetPolicy.
func TestLocalPolicyIsReReadEveryCall(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewLocal(dir)

	if err := store.SetPolicy(ctx, "prod", PolicyConverge); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	if p, _ := store.Policy(ctx, "prod"); p != PolicyConverge {
		t.Fatalf("setup: Policy = %v, want PolicyConverge", p)
	}

	path := filepath.Join(dir, DirRel, "policy-prod.json")
	if err := os.WriteFile(path, []byte(`{"policy":"pinned"}`), 0o644); err != nil {
		t.Fatalf("hand-edit policy file: %v", err)
	}

	// Same store instance, no reconstruction, no restart.
	got, err := store.Policy(ctx, "prod")
	if err != nil {
		t.Fatalf("Policy after hand-edit: %v", err)
	}
	if got != PolicyPinned {
		t.Fatalf("Policy after hand-edit = %v, want PolicyPinned; the value was cached, "+
			"which means opting out would require a restart", got)
	}
	if got.AllowsConverge() {
		t.Fatal("a pinned environment still permits convergence")
	}
}

// TestLocalPolicyMalformedFileIsAnError: a corrupt policy file must fail
// loudly. Falling back to a default would mean a corrupted pinned
// setting silently re-enables writes.
func TestLocalPolicyMalformedFileIsAnError(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewLocal(dir)

	if err := store.SetPolicy(ctx, "prod", PolicyPinned); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	path := filepath.Join(dir, DirRel, "policy-prod.json")
	if err := os.WriteFile(path, []byte(`{"policy":`), 0o644); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}

	got, err := store.Policy(ctx, "prod")
	if err == nil {
		t.Fatal("a malformed policy file decoded without error")
	}
	if got.AllowsConverge() {
		t.Fatal("a malformed policy file yielded a converge-permitting policy")
	}
}

func TestLocalListIsScopedAndOrdered(t *testing.T) {
	ctx := context.Background()
	store := NewLocal(t.TempDir())

	put := func(env, provider, service string) {
		t.Helper()
		if err := store.Put(ctx, Record{Key: Key{Env: env, Provider: provider, Service: service}}); err != nil {
			t.Fatalf("Put %s/%s/%s: %v", env, provider, service, err)
		}
	}
	put("prod", "k8s", "web")
	put("prod", "k8s", "api")
	put("prod", "compose", "worker")
	put("staging", "k8s", "api")

	got, err := store.List(ctx, "prod")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List(prod) returned %d records, want 3: %+v", len(got), got)
	}
	want := []string{"compose/worker", "k8s/api", "k8s/web"}
	for i, rec := range got {
		if rec.Key.Env != "prod" {
			t.Fatalf("List(prod) leaked an environment: %+v", rec.Key)
		}
		if got := rec.Key.Provider + "/" + rec.Key.Service; got != want[i] {
			t.Fatalf("List(prod)[%d] = %s, want %s (order must be stable)", i, got, want[i])
		}
	}

	staging, err := store.List(ctx, "staging")
	if err != nil {
		t.Fatalf("List(staging): %v", err)
	}
	if len(staging) != 1 || staging[0].Key.Service != "api" {
		t.Fatalf("List(staging) = %+v, want exactly prod-free staging/k8s/api", staging)
	}
}

func TestLocalListMissingDirIsEmptyNotError(t *testing.T) {
	ctx := context.Background()
	store := NewLocal(t.TempDir())

	got, err := store.List(ctx, "prod")
	if err != nil {
		t.Fatalf("List on a project that has never deployed: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("List = %+v, want empty", got)
	}
}

// TestLocalReadsLegacyDeployState covers the project that has been
// deploying for months and turns reconciliation on today. The older
// per-(provider, env, service) file must be readable, and must surface
// as UNKNOWN — forge shipped this, but nobody has looked at it.
func TestLocalReadsLegacyDeployState(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewLocal(dir)

	stateDir := filepath.Join(dir, DirRel)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := legacyDeployState{
		Image:      "ghcr.io/x/api",
		Tag:        "v7",
		DeployedAt: "2026-02-01T10:00:00Z",
	}
	data, _ := json.Marshal(legacy)
	// The EXACT filename forge's existing providers write.
	if err := os.WriteFile(filepath.Join(stateDir, "external-prod-api.json"), data, 0o644); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	got, err := store.Get(ctx, Key{Env: "prod", Provider: "external", Service: "api"})
	if err != nil {
		t.Fatalf("Get with only a legacy file present: %v", err)
	}
	if got.Desired.Tag != "v7" || got.Desired.Image != "ghcr.io/x/api" {
		t.Fatalf("legacy fallback lost the deploy record: %+v", got.Desired)
	}
	if got.Observed.Measured {
		t.Fatal("legacy fallback claimed an observation; nobody has looked at this target")
	}
	if state := got.Evaluate(time.Now(), DefaultStabilityWindow); state != StateUnknown {
		t.Fatalf("legacy fallback evaluates to %v, want StateUnknown", state)
	}
}

// TestLocalReconcileRecordWinsOverLegacy: once reconciliation has
// written a record, the legacy file must not shadow it.
func TestLocalReconcileRecordWinsOverLegacy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewLocal(dir)

	key := Key{Env: "prod", Provider: "external", Service: "api"}
	stateDir := filepath.Join(dir, DirRel)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, _ := json.Marshal(legacyDeployState{Image: "old", Tag: "v1", DeployedAt: "2026-01-01T00:00:00Z"})
	if err := os.WriteFile(filepath.Join(stateDir, "external-prod-api.json"), data, 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	if err := store.Put(ctx, Record{
		Key:      key,
		Desired:  Desired{Tag: "v9"},
		Observed: Observation{Measured: true, Serving: true},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Desired.Tag != "v9" {
		t.Fatalf("legacy file shadowed the reconcile record: tag = %q, want v9", got.Desired.Tag)
	}
	if !got.Observed.Measured {
		t.Fatal("legacy file shadowed the reconcile record's observation")
	}
}

// TestLocalDoesNotWriteLegacyFile: Local must never touch the file
// forge's non-cluster providers read back as ${LAST_TAG}. Corrupting it
// loses the only record of what External and Compose deployed.
func TestLocalDoesNotWriteLegacyFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewLocal(dir)

	stateDir := filepath.Join(dir, DirRel)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacyPath := filepath.Join(stateDir, "external-prod-api.json")
	original, _ := json.Marshal(legacyDeployState{Image: "ghcr.io/x/api", Tag: "v7", DeployedAt: "2026-02-01T10:00:00Z"})
	if err := os.WriteFile(legacyPath, original, 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	if err := store.Put(ctx, Record{
		Key:     Key{Env: "prod", Provider: "external", Service: "api"},
		Desired: Desired{Tag: "v99"},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	after, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy after Put: %v", err)
	}
	if string(after) != string(original) {
		t.Fatalf("Put rewrote the legacy deploy-state file:\n before: %s\n  after: %s", original, after)
	}
}

// TestLocalPathTraversalIsContained: a service name carrying separators
// must not escape the state directory.
func TestLocalPathTraversalIsContained(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewLocal(dir)

	evil := Key{Env: "prod", Provider: "k8s", Service: "../../../etc/passwd"}
	if err := store.Put(ctx, Record{Key: evil}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, DirRel))
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one file in the state dir, got %d", len(entries))
	}
	for _, e := range entries {
		if filepath.Dir(e.Name()) != "." {
			t.Fatalf("write escaped the state dir: %q", e.Name())
		}
	}
	// And it must still be readable under the same key.
	if _, err := store.Get(ctx, evil); err != nil {
		t.Fatalf("Get after a sanitized Put: %v", err)
	}
}

func TestLocalRejectsIncompleteKeys(t *testing.T) {
	ctx := context.Background()
	store := NewLocal(t.TempDir())

	if err := store.Put(ctx, Record{Key: Key{Env: "prod", Provider: "k8s"}}); err == nil {
		t.Fatal("Put accepted a key with no service")
	}
	if _, err := store.Get(ctx, Key{Env: "prod"}); err == nil {
		t.Fatal("Get accepted a key with no provider or service")
	}
	if _, err := store.List(ctx, ""); err == nil {
		t.Fatal("List accepted an empty environment")
	}
	if _, err := store.Policy(ctx, ""); err == nil {
		t.Fatal("Policy accepted an empty environment")
	}
}

// TestLocalHonoursContextCancellation: a cancelled pass must not keep
// writing.
func TestLocalHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewLocal(t.TempDir())

	key := Key{Env: "prod", Provider: "k8s", Service: "api"}
	if err := store.Put(ctx, Record{Key: key}); !errors.Is(err, context.Canceled) {
		t.Errorf("Put on a cancelled context: err = %v, want context.Canceled", err)
	}
	if _, err := store.Get(ctx, key); !errors.Is(err, context.Canceled) {
		t.Errorf("Get on a cancelled context: err = %v, want context.Canceled", err)
	}
	if _, err := store.Policy(ctx, "prod"); !errors.Is(err, context.Canceled) {
		t.Errorf("Policy on a cancelled context: err = %v, want context.Canceled", err)
	}
}

// TestLocalWriteIsAtomicForReaders: a reader must never see a partial
// file. The temp-file-and-rename is what buys this; a direct
// os.WriteFile truncates first and leaves a window in which a concurrent
// reader gets an empty or half-written document.
func TestLocalWriteIsAtomicForReaders(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewLocal(dir)

	key := Key{Env: "prod", Provider: "k8s", Service: "api"}
	if err := store.Put(ctx, Record{Key: key, Desired: Desired{Tag: "v1"}}); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	done := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			if _, err := store.Get(ctx, key); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}()
	for i := 0; i < 200; i++ {
		if err := store.Put(ctx, Record{Key: key, Desired: Desired{Tag: "v2"}}); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}
	<-done
	select {
	case err := <-errs:
		t.Fatalf("a concurrent reader saw a partial file: %v", err)
	default:
	}
}
