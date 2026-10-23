// Tests for the Tier-1 stomp-guard scoping helper.
//
// FRICTION 2026-06-02: cp-forge dogfood pass — the stomp guard hard-
// failed on drift to pkg/app/migrate.go for agents whose port targeted
// internal/proxy/, blocking concurrent lane work. These tests pin the
// fix: drift on a path whose emitter step is gated OFF is filtered out
// of the in-scope set and surfaced as a warning instead of an error.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
)

// TestTier1OwnerGateRegistry pins the canonical mapping between
// Tier-1 emitter paths and their owning gate. New emitters added to
// the registry must have a corresponding row here so the table stays
// truthful as a documentation surface.
func TestTier1OwnerGateRegistry(t *testing.T) {
	cases := []struct {
		path        string
		wantMapped  bool
		description string
	}{
		{"pkg/app/migrate.go", false, "migrate.go retired to pkg/migratekit — no entry"},
		{"db/embed_gen.go", true, "db/embed.go is gated on database driver"},
		{"pkg/app/app_gen.go", true, "app_gen.go is gated on any entrypoint"},
		{"internal/handlers/order/helpers_gen_test.go", true, "the per-service test harness is gated on any entrypoint"},
		{"internal/handlers/order/factories_gen_test.go", true, "entity factories are gated on codegen+services"},
		// The retired name-matched DI files (bootstrap.go/wire_gen.go) have
		// no registry entry — they fall through to nil so the cleanup sweep
		// treats stale copies as removable (FORGE_SHAPE_REDESIGN §2).
		{"pkg/app/bootstrap.go", false, "bootstrap.go retired — no entry, swept as stale"},
		{"pkg/app/wire_gen.go", false, "wire_gen.go retired — no entry, swept as stale"},
		// glob entries — exercise the path/filepath.Match wiring.
		{"internal/handlers/billing/handlers_crud_ops_gen.go", true, "internal/handlers/<svc>/handlers_crud_ops_gen.go is gated on codegen+services"},
		{"internal/handlers/users/handlers_crud_ops_gen.go", true, "second svc still matches the same glob"},
		{"pkg/middleware/auth_gen.go", false, "pkg/middleware has no emitter — no entry, fail-closed in-scope"},
		{"frontends/admin/src/hooks/users-hooks.ts", true, "frontend hook glob is gated on frontend+services"},
		// Unknown paths fall through to nil → caller treats as in-scope.
		// This preserves loud-fail behavior for new emitters until they
		// get a registry entry.
		{"cmd/server.go", false, "server.go has no entry — fail-closed by design"},
		{"internal/handlers/billing/something_else.go", false, "non-_crud_gen handler files don't match the glob"},
		{"frontends/admin/src/pages/users.tsx", false, "frontend pages aren't in the hooks glob"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := tier1OwnerGate(tc.path)
			if tc.wantMapped && got == nil {
				t.Errorf("tier1OwnerGate(%q) = nil, want mapped (%s)", tc.path, tc.description)
			}
			if !tc.wantMapped && got != nil {
				t.Errorf("tier1OwnerGate(%q) returned a gate; want nil (%s)", tc.path, tc.description)
			}
		})
	}
}

// TestFilterTier1DriftInScope_GateOffFiltersDrift exercises the actual
// FRICTION case: a project without a database driver should not see
// pkg/app/migrate.go drift block its generate run. The drift is
// classified as out-of-scope, in-scope is empty.
func TestFilterTier1DriftInScope_GateOffFiltersDrift(t *testing.T) {
	// gateMigrateHasDriver returns false when cfg.Database.Driver is
	// empty (cli's gate helper short-circuits to false on a nil cfg).
	ctx := &pipelineContext{
		Cfg: &config.ProjectConfig{
			Name: "cli-only-project",
			// no Database.Driver — gateMigrateHasDriver returns false
		},
	}
	if gateMigrateHasDriver(ctx) {
		t.Fatalf("gateMigrateHasDriver should be false for a cfg with no driver")
	}
	drift := []driftStub{
		{path: "db/embed_gen.go"},
	}
	inScope, outOfScope := filterTier1DriftInScope(ctx, drift, func(d driftStub) string { return d.path })
	if len(inScope) != 0 {
		t.Errorf("inScope = %d entries, want 0 (gated-off emitter shouldn't block stomp guard)", len(inScope))
	}
	if len(outOfScope) != 1 {
		t.Errorf("outOfScope = %d entries, want 1", len(outOfScope))
	}
}

// TestFilterTier1DriftInScope_GateOnKeepsDriftInScope pins the
// loud-fail path: when the emitter's gate IS true (driver configured),
// drift on its file stays in-scope so the user is forced to confront
// the conflict.
func TestFilterTier1DriftInScope_GateOnKeepsDriftInScope(t *testing.T) {
	ctx := &pipelineContext{
		Cfg: &config.ProjectConfig{
			Name:     "with-driver",
			Database: config.DatabaseConfig{Driver: "postgres"},
			Features: config.FeaturesConfig{}.With(config.FeatureMigrations, true),
		},
	}
	if !gateMigrateHasDriver(ctx) {
		t.Fatalf("gateMigrateHasDriver should be true for a cfg with driver=postgres")
	}
	drift := []driftStub{{path: "pkg/app/migrate.go"}}
	inScope, outOfScope := filterTier1DriftInScope(ctx, drift, func(d driftStub) string { return d.path })
	if len(inScope) != 1 {
		t.Errorf("inScope = %d entries, want 1 (gate-on emitter must still fail loudly)", len(inScope))
	}
	if len(outOfScope) != 0 {
		t.Errorf("outOfScope = %d entries, want 0", len(outOfScope))
	}
}

// TestFilterTier1DriftInScope_UnknownPathStaysInScope pins the
// fail-closed semantics for unregistered emitters: an emitter we don't
// know about keeps its drift in-scope so adding a new Tier-1 emitter
// without registering it here doesn't accidentally silence drift.
func TestFilterTier1DriftInScope_UnknownPathStaysInScope(t *testing.T) {
	ctx := &pipelineContext{} // empty — all gates fall to defaults
	drift := []driftStub{
		{path: "cmd/server.go"}, // not in registry
	}
	inScope, outOfScope := filterTier1DriftInScope(ctx, drift, func(d driftStub) string { return d.path })
	if len(inScope) != 1 {
		t.Errorf("unknown-emitter drift should stay in-scope; inScope = %d, want 1", len(inScope))
	}
	if len(outOfScope) != 0 {
		t.Errorf("unknown-emitter drift should not be out-of-scope; outOfScope = %d, want 0", len(outOfScope))
	}
}

// TestStepCheckTier1Drift_PopulatesPresenceBeforeScoping is the
// regression pin for the silent-stomp ordering bug the fixture corpus
// caught: stepCheckTier1Drift runs BEFORE stepDetectProtoDirs, so the
// presence flags (HasServices/HasWorkers/HasOperators) the scope-filter
// gates consult were all false at guard time. On a FULL generate run
// over a project with services, drift on a Tier-1 pkg/app file was
// classified out-of-scope, the guard waved it through, and the emitter
// then overwrote the user's hand edit with no error.
//
// The fix populates component presence inside stepCheckTier1Drift
// before filtering. This test drives the real step function on a
// synthetic project with proto/services/ and a drifted app_gen.go and
// requires the loud Tier-1 error.
func TestStepCheckTier1Drift_PopulatesPresenceBeforeScoping(t *testing.T) {
	dir := t.TempDir()
	// HasServices is now derived from whether the project actually
	// DEFINES a Connect service (descriptor / proto-source scan), not from
	// the mere existence of a proto/services/ dir — so the fixture must
	// declare a real service to count as "a project with services".
	mustWriteScopeFile(t, filepath.Join(dir, "proto", "services", "api", "v1", "api.proto"),
		"syntax = \"proto3\";\npackage api.v1;\nservice APIService {}\n")
	const rel = "pkg/app/app_gen.go"
	recorded := []byte("package app // as generated\n")
	edited := []byte("package app // hand-edited\n")
	// Hand-edited Tier-1 file: the embedded forge:hash marker still
	// carries the hash of the content forge stamped; the body has since
	// changed, so Verify answers Modified and the stomp guard fires.
	stamped, ok := checksums.StampWithValue(rel, edited, checksums.BodyHash(recorded))
	if !ok {
		t.Fatal("app_gen.go should be stampable")
	}
	mustWriteScopeFile(t, filepath.Join(dir, rel), string(stamped))

	cs := &checksums.FileChecksums{}

	ctx := &pipelineContext{
		ProjectDir: dir,
		AbsPath:    dir,
		Checksums:  cs,
		// Deliberately NOT setting HasServices — the step must derive
		// it from disk itself.
	}
	err := checkTier1DriftAndAdjudicate(t, ctx)
	if err == nil {
		t.Fatalf("stepCheckTier1Drift = nil; want the Tier-1 stomp-guard error (drifted %s on a project with services)", rel)
	}
	if !strings.Contains(err.Error(), rel) {
		t.Errorf("stomp-guard error should name %s; got: %v", rel, err)
	}
	if !ctx.HasServices {
		t.Errorf("stepCheckTier1Drift did not populate ctx.HasServices from disk")
	}
}

// mustWriteScopeFile writes content at path, creating parents — local
// to the scope tests so they stay dependency-free.
func mustWriteScopeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// driftStub is a minimal generic-friendly stand-in for
// checksums.Tier1DriftEntry so this file doesn't import the canonical
// checksums type just for path-extraction tests.
type driftStub struct{ path string }
