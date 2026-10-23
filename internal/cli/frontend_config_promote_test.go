package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

// writeFrontendDescriptor writes the gen/forge_descriptor.json (and the
// go.mod its project-root walk-up keys on) that the deploy path reads its
// config messages from — the same source `forge generate` populates.
func writeFrontendDescriptor(t *testing.T, projectDir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(projectDir, "go.mod"),
		[]byte("module example.com/demo\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	desc := codegen.ForgeDescriptor{Configs: []codegen.ConfigMessage{{
		Name:     "WebConfig",
		Frontend: "web",
		Fields:   testWebFrontendConfig().Fields,
	}}}
	data, err := json.MarshalIndent(desc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(projectDir, "gen", "forge_descriptor.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRenderFrontendRuntimeDocs_PerEnvFromKCL is the deploy-path half of
// the promote contract: an environment's runtime config DOCUMENT is
// produced from that environment's own KCL, and two environments produce
// DIFFERENT documents.
//
// Before the fix, forge generated the KCL runtime projection but nothing
// consumed it at deploy time — so every environment would have shipped
// whatever config.js happened to be inside the built bundle (dev's), which
// is precisely the failure runtime injection was chosen to avoid.
//
// The proto declares `environment` with default "production" and `api_url`
// with default "http://localhost:8080". Each env pins only `environment`,
// to a different value; neither pins `api_url`. That combination proves the
// per-env override AND the proto-default fallback in one render.
func TestRenderFrontendRuntimeDocs_PerEnvFromKCL(t *testing.T) {
	projectDir := frontendConfigProject(t, map[string]string{
		"dev":  "dev",
		"prod": "prod-live",
	})
	writeFrontendDescriptor(t, projectDir)

	devDocs, err := renderFrontendRuntimeDocs(projectDir, "dev")
	if err != nil {
		t.Fatalf("renderFrontendRuntimeDocs(dev): %v", err)
	}
	prodDocs, err := renderFrontendRuntimeDocs(projectDir, "prod")
	if err != nil {
		t.Fatalf("renderFrontendRuntimeDocs(prod): %v", err)
	}

	dev, ok := devDocs["web"]
	if !ok {
		t.Fatalf("no runtime document rendered for frontend web (dev); got keys %v", keysOf(devDocs))
	}
	prod, ok := prodDocs["web"]
	if !ok {
		t.Fatalf("no runtime document rendered for frontend web (prod); got keys %v", keysOf(prodDocs))
	}

	if !strings.Contains(dev, `"NEXT_PUBLIC_ENVIRONMENT": "dev"`) {
		t.Errorf("dev document must carry dev's KCL value; got:\n%s", dev)
	}
	if !strings.Contains(prod, `"NEXT_PUBLIC_ENVIRONMENT": "prod-live"`) {
		t.Errorf("prod document must carry prod's KCL value; got:\n%s", prod)
	}
	// Not the proto default either — prod's value came from prod's KCL.
	if strings.Contains(prod, `"NEXT_PUBLIC_ENVIRONMENT": "production"`) {
		t.Errorf("prod document fell back to the proto default; got:\n%s", prod)
	}
	if dev == prod {
		t.Errorf("two environments must not render the same document; both were:\n%s", dev)
	}

	// Neither env pins api_url, so both inherit the proto default — the
	// fallback half of the contract, on the deploy path too.
	for name, doc := range map[string]string{"dev": dev, "prod": prod} {
		if !strings.Contains(doc, `"NEXT_PUBLIC_API_URL": "http://localhost:8080"`) {
			t.Errorf("%s: an unpinned field must fall back to its proto default; got:\n%s", name, doc)
		}
	}

	// Each document names the environment it was rendered for, so an
	// artifact found in a bucket can be traced back to its source.
	if !strings.Contains(dev, `environment "dev"`) {
		t.Errorf("dev document must identify its environment; got:\n%s", dev)
	}
	if !strings.Contains(prod, `environment "prod"`) {
		t.Errorf("prod document must identify its environment; got:\n%s", prod)
	}
}

// TestFrontendConfigJSNameAgrees pins the one fact the type system cannot:
// the generator emits a <script src=".../config.js"> and the deploy target
// writes the file that src resolves to, but deploytarget deliberately has
// no import on codegen. If these two spellings ever diverge the deployed
// bundle would load a document that isn't there — and would fail at
// runtime, in the browser, only in a deployed environment.
func TestFrontendConfigJSNameAgrees(t *testing.T) {
	if deploytarget.FrontendConfigJSName != codegen.FrontendConfigJSFile {
		t.Errorf("deploytarget.FrontendConfigJSName (%q) must equal codegen.FrontendConfigJSFile (%q)",
			deploytarget.FrontendConfigJSName, codegen.FrontendConfigJSFile)
	}
}

// TestRenderFrontendRuntimeDocs_NoConfigIsNoOp confirms a project that
// annotates no frontend config deploys exactly as before — no document, no
// error, nothing added to the artifact.
func TestRenderFrontendRuntimeDocs_NoConfigIsNoOp(t *testing.T) {
	docs, err := renderFrontendRuntimeDocs(t.TempDir(), "prod")
	if err != nil {
		t.Fatalf("a project with no frontend config must not error: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected no runtime documents, got %v", keysOf(docs))
	}
}
