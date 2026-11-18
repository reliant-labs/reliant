package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// Every scaffold-once file a frontend scaffold writes is pristine until its
// author edits it. `forge lint`'s format lane skips a pristine scaffold, and
// a file with no birth record reads as edited from birth — so a fresh
// project's first report-only lint under CI failed on the 31 template files
// prettier would re-wrap (TestE2EFreshScaffoldLintExitsZero). Only nav.tsx
// and dashboard.tsx used to be recorded.
func TestFrontendScaffold_EveryScaffoldFileIsBornPristine(t *testing.T) {
	for _, kind := range []string{"", "vite-spa", "mobile"} {
		t.Run("kind="+kind, func(t *testing.T) {
			root := t.TempDir()
			checksums.ResetScaffoldLedgerCache()
			t.Cleanup(checksums.ResetScaffoldLedgerCache)
			if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: app\nmodule_path: example.com/app\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := GenerateFrontendFilesWithOptions(root, "example.com/app", "app", "web", 8080, kind, FrontendGenOptions{}); err != nil {
				t.Fatal(err)
			}
			src := filepath.Join(root, "frontends", "web", "src")
			checked := 0
			err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				content, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				if IsForgeGenerated(content) {
					return nil
				}
				rel, _ := filepath.Rel(root, p)
				checked++
				if !checksums.ScaffoldIsPristine(root, rel) {
					t.Errorf("%s: scaffolded but not pristine in the birth ledger, so forge lint treats forge's own bytes as the author's", rel)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if checked < 10 {
				t.Fatalf("checked %d scaffold files under %s: the walk saw too little to mean anything", checked, src)
			}
		})
	}
}
