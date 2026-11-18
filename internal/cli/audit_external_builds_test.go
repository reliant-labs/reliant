package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/cli/audittype"
)

// TestAuditExternalBuilds_NoServicesIsOK pins the "category always
// present" contract: a project with zero services declaring build_cmd
// still gets the external_builds key, with status=ok and a "0
// services" summary. Sub-agents can rely on the key being there
// regardless of feature state.
func TestAuditExternalBuilds_NoServicesIsOK(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "deploy", "kcl", "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy", "kcl", "dev", "main.k"), []byte("// stub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, `{
  "output": {
    "workloads": [
      {
        "name": "api",
        "kind": "service",
        "image": "r/api",
        "runtime": {
          "type": "cluster",
          "cluster": "c",
          "namespace": "n"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`))

	entities, err := RenderKCL(t.Context(), dir, "dev")
	if err != nil {
		t.Fatalf("RenderKCL: %v", err)
	}
	cat := collectExternalBuildEntries(entities, []string{"dev"}, dir)
	if cat.Status != audittype.StatusOK {
		t.Errorf("status = %q, want ok", cat.Status)
	}
	if !strings.Contains(cat.Summary, "0 service") {
		t.Errorf("summary = %q, want '0 service'", cat.Summary)
	}
	// Additive contract: services key must exist even when empty so
	// `jq '.external_builds.details.services | length'` is always 0
	// rather than `null`.
	if _, ok := cat.Details["services"]; !ok {
		t.Error("services key missing from details")
	}
}

// TestAuditExternalBuilds_PresentCwdNoConflictIsOK is the happy path:
// service declares build_cmd, build_cwd resolves to an existing dir,
// no build_env collisions → status=ok.
func TestAuditExternalBuilds_PresentCwdNoConflictIsOK(t *testing.T) {
	dir := t.TempDir()
	// Sibling repo dir that resolves cleanly relative to projectDir.
	if err := os.MkdirAll(filepath.Join(dir, "sibling"), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := `{
  "output": {
    "workloads": [
      {
        "name": "gw",
        "kind": "service",
        "image": "r/my-gw",
        "build": {
          "type": "shell",
          "cmd": "docker build .",
          "cwd": "sibling"
        },
        "runtime": {
          "type": "cluster",
          "cluster": "c",
          "namespace": "n"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, fixture))
	entities, _ := RenderKCL(t.Context(), dir, "dev")
	cat := collectExternalBuildEntries(entities, nil, dir)
	if cat.Status != audittype.StatusOK {
		t.Errorf("status = %q, want ok; details=%v", cat.Status, cat.Details)
	}
	svcs, _ := cat.Details["services"].([]externalBuildEntry)
	if len(svcs) != 1 {
		t.Fatalf("want 1 service entry; got %d", len(svcs))
	}
	if !svcs[0].CwdExists {
		t.Errorf("cwd_exists = false, want true (sibling dir was created)")
	}
	if svcs[0].ResolvedCwd != filepath.Join(dir, "sibling") {
		t.Errorf("resolved_cwd = %q, want %q", svcs[0].ResolvedCwd, filepath.Join(dir, "sibling"))
	}
}

// TestAuditExternalBuilds_MissingCwdWarns covers the dominant
// surfaced finding: build_cwd points at a sibling dir that doesn't
// exist on this machine. Build-side semantics are skip-with-warn, but
// audit calls it out so the user knows why their build skipped.
func TestAuditExternalBuilds_MissingCwdWarns(t *testing.T) {
	dir := t.TempDir()
	// Intentionally do NOT create dir/missing-sibling — we want the
	// stat to fail.
	fixture := `{
  "output": {
    "workloads": [
      {
        "name": "gw",
        "kind": "service",
        "image": "r/gw",
        "build": {
          "type": "shell",
          "cmd": "docker build .",
          "cwd": "missing-sibling"
        },
        "runtime": {
          "type": "cluster",
          "cluster": "c",
          "namespace": "n"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, fixture))
	entities, _ := RenderKCL(t.Context(), dir, "dev")
	cat := collectExternalBuildEntries(entities, nil, dir)
	if cat.Status != audittype.StatusWarn {
		t.Errorf("status = %q, want warn", cat.Status)
	}
	if cnt, _ := cat.Details["missing_cwd_count"].(int); cnt != 1 {
		t.Errorf("missing_cwd_count = %v, want 1", cat.Details["missing_cwd_count"])
	}
	svcs, _ := cat.Details["services"].([]externalBuildEntry)
	if len(svcs) != 1 || svcs[0].CwdExists {
		t.Errorf("expected one entry with CwdExists=false; got %+v", svcs)
	}
}

// A declared env key named after one of the old substitution tokens is NOT a
// conflict any more, and audit must not warn about it.
//
// It used to: a built-in token shadowed the user's key, so declaring IMAGE or
// TAG was a footgun worth surfacing. With substitution gone there are no
// built-ins, so the declared env map is the ONLY source — a key called
// TARGETARCH is exactly how a project keeps a `${TARGETARCH}` spelling in its
// command working, which is the migration path the lint rule's remediation
// recommends. Warning on it would flag the fix as the problem.
func TestAuditExternalBuilds_TokenNamedEnvKeyIsNotAConflict(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sib"), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := `{
  "output": {
    "workloads": [
      {
        "name": "gw",
        "kind": "service",
        "image": "r/gw",
        "build": {
          "type": "shell",
          "cmd": "docker build .",
          "cwd": "sib",
          "env": {
            "IMAGE": "oops",
            "CGO_ENABLED": "0",
            "TAG": "v1"
          }
        },
        "runtime": {
          "type": "cluster",
          "cluster": "c",
          "namespace": "n"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, fixture))
	entities, _ := RenderKCL(t.Context(), dir, "dev")
	cat := collectExternalBuildEntries(entities, nil, dir)
	if cat.Status != audittype.StatusOK {
		t.Errorf("status = %q, want ok — a token-named env key is how you keep the spelling, not a conflict", cat.Status)
	}
	if v, present := cat.Details["conflict_count"]; present {
		t.Errorf("the collision count must be gone from the audit details; got %v", v)
	}
	svcs, _ := cat.Details["services"].([]externalBuildEntry)
	if len(svcs) != 1 {
		t.Fatalf("want 1 service; got %d", len(svcs))
	}
	// The keys are still reported, as plain information.
	want := []string{"CGO_ENABLED", "IMAGE", "TAG"}
	if got := svcs[0].BuildEnvKeys; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("build_env_keys = %v, want %v (sorted)", got, want)
	}
}

// TestAuditExternalBuilds_StateReadAggregatesEnvs writes per-env
// state files for two envs and confirms the audit entry aggregates
// both LastBuilds rows in env order.
func TestAuditExternalBuilds_StateReadAggregatesEnvs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write build-state files for two envs via the buildtarget API
	// so the on-disk shape matches production exactly.
	for _, env := range []string{"dev", "prod"} {
		if err := buildtarget.WriteState(dir, env, buildtarget.State{
			Service:  "gw",
			Image:    "ghcr.io/acme/gw",
			Tag:      env + "-tag",
			PushedAt: "2026-01-01T00:00:00Z",
		}); err != nil {
			t.Fatalf("WriteState %s: %v", env, err)
		}
	}
	fixture := `{
  "output": {
    "workloads": [
      {
        "name": "gw",
        "kind": "service",
        "image": "r/gw",
        "build": {
          "type": "shell",
          "cmd": "docker build .",
          "cwd": "src"
        },
        "runtime": {
          "type": "cluster",
          "cluster": "c",
          "namespace": "n"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, fixture))
	entities, _ := RenderKCL(t.Context(), dir, "dev")
	cat := collectExternalBuildEntries(entities, []string{"dev", "prod"}, dir)
	if cat.Status != audittype.StatusOK {
		t.Errorf("status = %q, want ok; details=%v", cat.Status, cat.Details)
	}
	svcs, _ := cat.Details["services"].([]externalBuildEntry)
	if len(svcs) != 1 || len(svcs[0].LastBuilds) != 2 {
		t.Fatalf("want 1 service with 2 last_builds; got %+v", svcs)
	}
	if svcs[0].LastBuilds[0].Env != "dev" || svcs[0].LastBuilds[0].Tag != "dev-tag" {
		t.Errorf("dev state mismatch: %+v", svcs[0].LastBuilds[0])
	}
	if svcs[0].LastBuilds[1].Env != "prod" || svcs[0].LastBuilds[1].Tag != "prod-tag" {
		t.Errorf("prod state mismatch: %+v", svcs[0].LastBuilds[1])
	}
	if cnt, _ := cat.Details["state_count"].(int); cnt != 2 {
		t.Errorf("state_count = %v, want 2", cat.Details["state_count"])
	}
}

// TestAuditExternalBuilds_JSONShape_Golden pins the JSON output shape
// the additive-extension contract sub-agents read against. We
// build a fixture with one service and assert the marshalled output
// has exactly the documented keys. Order-sensitive on the services
// slice (sort by service name); insensitive on map keys (jq doesn't
// care about object key order).
//
// Golden value is asserted via key-presence + value spot-check rather
// than byte-for-byte string equality so future additive fields (new
// audit details, more diagnostics) don't break the test.
func TestAuditExternalBuilds_JSONShape_Golden(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := `{
  "output": {
    "workloads": [
      {
        "name": "gw",
        "kind": "service",
        "image": "r/gw",
        "build": {
          "type": "shell",
          "cmd": "docker build .",
          "cwd": "src",
          "env": {
            "CGO_ENABLED": "0"
          }
        },
        "runtime": {
          "type": "cluster",
          "cluster": "c",
          "namespace": "n"
        },
        "spec": {
          "kind": "service"
        }
      }
    ]
  }
}`
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", writeKCLFixture(t, fixture))
	entities, _ := RenderKCL(t.Context(), dir, "dev")
	cat := collectExternalBuildEntries(entities, nil, dir)

	// Marshal and re-decode through a generic shape so we can assert
	// the JSON contract without coupling to the concrete Go type.
	data, err := json.Marshal(cat)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
		Details struct {
			Services []map[string]any `json:"services"`
		} `json:"details"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Status != "ok" {
		t.Errorf("status = %q, want ok", decoded.Status)
	}
	if len(decoded.Details.Services) != 1 {
		t.Fatalf("want 1 service; got %d", len(decoded.Details.Services))
	}
	s := decoded.Details.Services[0]
	for _, k := range []string{"service", "image", "build_cwd", "resolved_cwd", "cwd_exists", "build_env_keys"} {
		if _, ok := s[k]; !ok {
			t.Errorf("service entry missing required key %q; got keys=%v", k, mapKeys(s))
		}
	}
	if s["service"] != "gw" {
		t.Errorf("service = %v, want gw", s["service"])
	}
}

// TestAuditExternalBuilds_NoCfgIsError pins the contract for a
// non-forge project: nil cfg → status=error, no panic. Mirrors
// auditIngress's no-forge.yaml branch.
func TestAuditExternalBuilds_NoCfgIsError(t *testing.T) {
	cat := auditExternalBuilds(nil, t.TempDir())
	if cat.Status != audittype.StatusError {
		t.Errorf("status = %q, want error", cat.Status)
	}
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
