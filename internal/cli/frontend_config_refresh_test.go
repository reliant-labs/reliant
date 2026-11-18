package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// THE FIRST-RUN BUG, reproduced end to end.
//
// On a fresh project the order of events is fixed and cannot be
// rearranged: `forge generate` renders config.js while the OIDC client id
// is still the empty stub, then `forge env up` executes the idp-provision job,
// which registers the browser application and publishes the GENERATED id
// into deploy/kcl/dev/idp_identity_gen.k. Nothing re-read that file, so the
// browser was served `"OIDC_CLIENT_ID": ""` — the frontend's no-auth
// posture — and a first sign-in was impossible until the developer happened
// to run `forge generate` a second time.
//
// This test walks exactly that sequence, with the two documents produced
// by the REAL renderer from the env's own KCL — the pre-provision one from
// the empty identity stub, the post-provision one from what the job
// published. It fails against the old behaviour (the stale document
// survives untouched) and passes once the refresh runs after the jobs.
func TestRefreshFrontendRuntimeConfigs_PicksUpTheConvergedClientID(t *testing.T) {
	projectDir := identityProject(t)
	cfg := &config.ProjectConfig{
		Name:      "acme",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "vite"}.WithDir("frontends/web")},
	}
	fc := identityFrontendConfig()

	// What `forge generate` leaves on disk BEFORE any IdP exists: a real
	// document whose client id is empty because that is genuinely all that
	// is known yet.
	stale := renderRuntimeDoc(t, projectDir, fc)
	if strings.Contains(stale, "converged-client-id") {
		t.Fatal("fixture is wrong: the pre-provision document must not already carry the id")
	}
	writeConfigJS(t, projectDir, cfg, stale)

	// The idp-provision job runs and publishes what Zitadel generated.
	writeConvergedIdentity(t, projectDir, "converged-client-id")
	fresh := renderRuntimeDoc(t, projectDir, fc)

	changed, err := writeFrontendRuntimeDocs(cfg, projectDir, map[string]string{"web": fresh})
	if err != nil {
		t.Fatalf("writeFrontendRuntimeDocs: %v", err)
	}
	if changed != 1 {
		t.Errorf("refreshed %d document(s), want 1", changed)
	}

	got := readConfigJS(t, projectDir, cfg)
	if !strings.Contains(got, `"OIDC_CLIENT_ID": "converged-client-id"`) {
		t.Errorf("config.js still carries the pre-provision client id — a first `forge env up` cannot sign in:\n%s", got)
	}
	// The issuer rides the same published file. A refresh that recovered
	// the id but dropped the issuer leaves the frontend half-configured,
	// which its auth provider throws on rather than degrading.
	if !strings.Contains(got, `"OIDC_ISSUER": "http://localhost:8080"`) {
		t.Errorf("config.js lost the issuer:\n%s", got)
	}
}

// IDEMPOTENT. From the second run on, the identity is already published
// and the rendered bytes are identical — so nothing is rewritten and
// nothing is reported. Rewriting an unchanged file would touch its mtime
// on every `forge env up`, which a watching dev server reads as a reason to
// reload the browser.
// The file this compares against is the one the WRITER produced, stamp and
// all — which is the trap. A generated file carries a `forge:hash=` line
// that the renderer does not emit, so a raw byte comparison never matches
// and every `forge env up` reports refreshing a file it did not change. The
// second write below is what makes that visible: it is the real
// second-run-in-a-row, against real on-disk bytes.
func TestRefreshFrontendRuntimeConfigs_IsANoOpWhenAlreadyCurrent(t *testing.T) {
	projectDir := identityProject(t)
	cfg := &config.ProjectConfig{
		Name:      "acme",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "vite"}.WithDir("frontends/web")},
	}
	writeConvergedIdentity(t, projectDir, "converged-client-id")
	current := renderRuntimeDoc(t, projectDir, identityFrontendConfig())

	// First run: the document is genuinely new, so it is written — through
	// the real writer, so what lands on disk is stamped exactly as a
	// `forge env up` leaves it.
	if changed, err := writeFrontendRuntimeDocs(cfg, projectDir, map[string]string{"web": current}); err != nil || changed != 1 {
		t.Fatalf("first write = (%d, %v), want (1, nil)", changed, err)
	}
	if !strings.Contains(readConfigJS(t, projectDir, cfg), "forge:hash=") {
		t.Fatal("fixture assumption broken: the writer no longer stamps config.js, so this test proves nothing")
	}

	// Second run, same values. Nothing may be rewritten.
	before := configJSModTime(t, projectDir, cfg)
	changed, err := writeFrontendRuntimeDocs(cfg, projectDir, map[string]string{"web": current})
	if err != nil {
		t.Fatalf("writeFrontendRuntimeDocs: %v", err)
	}
	if changed != 0 {
		t.Errorf("refreshed %d document(s) when nothing changed, want 0\n"+
			"comparing raw bytes against a STAMPED file never matches, so every run reports a phantom change", changed)
	}
	if after := configJSModTime(t, projectDir, cfg); !after.Equal(before) {
		t.Error("an unchanged document was rewritten; a watching dev server would reload for nothing")
	}
}

// A project with NO frontend has no runtime document, and must not be made
// to render one — this runs on every `forge env up`, including for the
// API-key / worker / CLI projects that never wanted an IdP.
func TestRefreshFrontendRuntimeConfigs_SkipsAProjectWithNoFrontend(t *testing.T) {
	projectDir := identityProject(t)
	// Both entry points: the full refresh (which must not even render) and
	// the writer underneath it.
	if changed, err := refreshFrontendRuntimeConfigs(&config.ProjectConfig{Name: "acme"}, projectDir, "dev", nil); err != nil || changed != 0 {
		t.Errorf("a frontendless project must be a silent no-op, got (%d, %v)", changed, err)
	}
	if changed, err := refreshFrontendRuntimeConfigs(nil, projectDir, "dev", nil); err != nil || changed != 0 {
		t.Errorf("a nil config must be a no-op, got (%d, %v)", changed, err)
	}
	if changed, err := writeFrontendRuntimeDocs(&config.ProjectConfig{Name: "acme"}, projectDir,
		map[string]string{"web": "irrelevant"}); err != nil || changed != 0 {
		t.Errorf("a document for a frontend the project does not declare must be ignored, got (%d, %v)", changed, err)
	}
}

// helpers — the config.js path is the one the frontend actually serves
// (<frontend>/public/config.js), so these compose it the same way
// generateFrontendConfigModules does rather than hard-coding it twice.

func configJSPath(cfg *config.ProjectConfig) string {
	fe := cfg.Frontends[0]
	return filepath.Join(fe.DeclaredDir(), frontendStaticDir(fe.Type), codegen.FrontendConfigJSFile)
}

// renderRuntimeDoc produces the config.js body the same way the deploy and
// generate paths do — the env's own KCL through the generated projection,
// with proto defaults filling anything it does not pin. Going through the
// real renderer is what makes the "stale" and "fresh" documents in these
// tests the actual artifacts rather than hand-written approximations of
// them.
//
// It stops short of renderFrontendRuntimeDocs only because that function
// re-parses the project's compiled proto descriptor, which this fixture
// (deliberately KCL-only) does not have.
func renderRuntimeDoc(t *testing.T, projectDir string, fc codegen.FrontendConfig) string {
	t.Helper()
	values, err := loadFrontendRuntimeConfig(projectDir, "dev", []codegen.FrontendConfig{fc})
	if err != nil {
		t.Fatalf("loadFrontendRuntimeConfig: %v", err)
	}
	encoded, err := json.MarshalIndent(frontendRuntimeValues(fc, values[fc.Frontend]), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return codegen.GenerateFrontendConfigJS(fc.Frontend, "dev", string(encoded))
}

func writeConfigJS(t *testing.T, projectDir string, cfg *config.ProjectConfig, body string) {
	t.Helper()
	full := filepath.Join(projectDir, configJSPath(cfg))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
}

func readConfigJS(t *testing.T, projectDir string, cfg *config.ProjectConfig) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(projectDir, configJSPath(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func configJSModTime(t *testing.T, projectDir string, cfg *config.ProjectConfig) time.Time {
	t.Helper()
	info, err := os.Stat(filepath.Join(projectDir, configJSPath(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime()
}
