package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// TestNavScaffoldRecordsBirthHash pins the ledger write that the nav
// generator's refresh depends on.
//
// nav.tsx and dashboard.tsx are written by the frontend scaffold before any
// entity exists, then kept current by emitScaffoldUntilTouched until the user
// edits them. That refresh is gated on checksums.ScaffoldIsPristine, which
// answers false for a path with NO ledger entry — indistinguishable from a
// path the user has edited. So a scaffold that writes the files without
// recording their birth hash freezes both as "user-owned" from birth: every
// new project ships a sidebar with no entity links and an empty dashboard
// grid, and nothing about it looks broken enough to notice.
//
// The bug this pins was silent in exactly that way, which is why the
// assertion is on the ledger rather than on the rendered file.
func TestNavScaffoldRecordsBirthHash(t *testing.T) {
	t.Parallel()

	// nav.tsx and dashboard.tsx exist only in the nextjs template tree —
	// vite-spa and react-native ship neither, and the nav generator has
	// nothing to refresh for them.
	root := t.TempDir()
	if err := GenerateFrontendFilesWithOptions(
		root, "example.com/app", "app", "fe", 8080, "", FrontendGenOptions{},
	); err != nil {
		t.Fatalf("GenerateFrontendFilesWithOptions: %v", err)
	}

	for _, rel := range []string{
		filepath.Join("frontends", "fe", "src", "components", "nav.tsx"),
		filepath.Join("frontends", "fe", "src", "app", "dashboard.tsx"),
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("%s was not written: %v", rel, err)
		}
		// Pristine straight after birth is the whole contract: it is what
		// lets the first entity's routes reach the sidebar.
		if !checksums.ScaffoldIsPristine(root, rel) {
			t.Errorf("%s is not pristine immediately after scaffold — "+
				"the nav generator will never refresh it, so entities "+
				"added later get pages with no link to them", rel)
		}
	}
}

// TestNavScaffoldStopsRefreshingAfterEdit is the other half of the contract:
// recording a birth hash must not make these files permanently forge-owned.
// One changed byte transfers ownership to the user for good.
func TestNavScaffoldStopsRefreshingAfterEdit(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := GenerateFrontendFilesWithOptions(
		root, "example.com/app", "app", "fe", 8080, "", FrontendGenOptions{},
	); err != nil {
		t.Fatalf("GenerateFrontendFilesWithOptions: %v", err)
	}

	rel := filepath.Join("frontends", "fe", "src", "components", "nav.tsx")
	full := filepath.Join(root, rel)
	body, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("read nav.tsx: %v", err)
	}
	if err := os.WriteFile(full, append(body, []byte("\n// mine now\n")...), 0o644); err != nil {
		t.Fatalf("edit nav.tsx: %v", err)
	}

	if checksums.ScaffoldIsPristine(root, rel) {
		t.Error("nav.tsx still reads as pristine after an edit — forge would " +
			"overwrite a file the user has taken ownership of")
	}
}
