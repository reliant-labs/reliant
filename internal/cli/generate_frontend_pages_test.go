package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// TestGenerateFrontendPages_PreservesHandEditsOnRegenerate is the
// regression test for the kalshi-trader friction round's blocker:
// `forge generate` unconditionally re-rendered
// `frontends/<fe>/src/app/<slug>/page.tsx` despite every page template
// carrying a canonical Tier-2 "yours:" banner (list page emitted by 'forge
// add page'` banner promising the user that hand-edits would survive.
// Pre-fix, the renderer used a bare `os.WriteFile` with no
// stat-check guard, so the second `forge generate` invocation
// silently clobbered the user's edits.
//
// Fix: emit through `checksums.WriteScaffoldIfMissing` with a stat-check
// pre-guard mirroring `emitScaffoldOnceIfMissing`, so the destination is
// left alone on re-run — forge writes the page once and never overwrites
// it.
func TestGenerateFrontendPages_PreservesHandEditsOnRegenerate(t *testing.T) {
	projectDir := t.TempDir()

	cfg := &config.ProjectConfig{
		Name: "demo",
		Frontends: []config.FrontendConfig{
			{Name: "dashboard", Type: "nextjs"},
		},
	}
	services := []codegen.ServiceDef{
		{
			Name:    "ClinicService",
			Package: "demo.v1",
			Methods: []codegen.Method{
				{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
			},
		},
	}
	entities := []codegen.EntityDef{{Name: "Patient"}}
	cs := &checksums.FileChecksums{}

	// First run: scaffolds the file.
	if err := generateFrontendPages(cfg, services, projectDir, entities, cs); err != nil {
		t.Fatalf("first generateFrontendPages: %v", err)
	}

	pagePath := filepath.Join(projectDir, "frontends", "dashboard", "src", "app", "patients", "page.tsx")
	if _, err := os.Stat(pagePath); err != nil {
		t.Fatalf("expected scaffolded page at %s after first run, got: %v", pagePath, err)
	}

	// Simulate a hand-edit on the scaffolded page.
	const handEdited = `// User hand-edited this page; the next forge generate must not clobber it.
export default function PatientsPage() { return null; }
`
	if err := os.WriteFile(pagePath, []byte(handEdited), 0o644); err != nil {
		t.Fatalf("simulating hand-edit: %v", err)
	}

	// Second run: hand-edit must survive.
	if err := generateFrontendPages(cfg, services, projectDir, entities, cs); err != nil {
		t.Fatalf("second generateFrontendPages: %v", err)
	}

	got, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatalf("read page after re-run: %v", err)
	}
	if string(got) != handEdited {
		t.Errorf("hand-edited page was clobbered by re-run of generateFrontendPages.\nwant:\n%s\ngot:\n%s", handEdited, string(got))
	}
}

// (The old force-preserves and scaffold-reset variants of this test are
// gone: there is a single scaffold tier now — an existing page is NEVER
// overwritten, by any flag. Refresh is delete-then-regenerate. The
// chokepoint-level pin lives in internal/checksums/scaffold_test.go.)
