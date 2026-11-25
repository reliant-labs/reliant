package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
)

// generateCRDKCL projects the project's CRD Go types (api/<version>/*_types.go)
// into deploy/kcl/lib/crd_gen.k — the KCL module that installs those CRDs on a
// cluster, listed in an Environment's `additional_manifests`.
//
// This closes a duplicate-declaration hole. A CRD used to be written twice: as
// the Go types the controller reads and writes, and as a hand-authored KCL
// dict that is what the cluster actually installs. The two drifting is SILENT
// and permanent — the API server prunes any CR field the installed schema does
// not declare, so a field added to the Go type but missing from the KCL
// applies cleanly, reports success, and reads back as a zero value forever.
// Deriving the KCL from the Go types means there is no second declaration to
// drift, and no hand-maintained drift gate to keep honest.
//
// A project with no api/ directory — which is most of them — generates nothing
// and reports nothing.
func generateCRDKCL(projectDir string, kclDirAbs string, cs *checksums.FileChecksums) error {
	absProject, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}

	// Deepcopy FIRST: a deleted type leaves a deepcopy file that no longer
	// compiles, and the CRD projection below loads the same packages.
	wrote, err := codegen.GenerateAPIDeepCopy(absProject)
	if err != nil {
		return fmt.Errorf("api deepcopy: %w", err)
	}
	for _, rel := range wrote {
		fmt.Printf("  ✅ Generated %s\n", rel)
	}

	docs, err := codegen.LoadCRDsFromGoTypes(absProject)
	if err != nil {
		return fmt.Errorf("project CRD types: %w", err)
	}

	outPath := filepath.Join(kclDirAbs, "lib", codegen.CRDKCLModule+".k")

	if len(docs) == 0 {
		// No CRDs. Remove a previously generated module rather than leaving a
		// stale one behind: its lambdas would still render, so a cluster would
		// keep installing CRDs for types the project no longer declares.
		// Journaled: a run that fails later restores the module.
		if err := checksums.RemoveJournaled(outPath); err != nil {
			return fmt.Errorf("remove stale %s: %w", outPath, err)
		}
		return nil
	}

	body, err := codegen.GenerateCRDKCL(docs, codegen.ControllerToolsVersion)
	if err != nil {
		return fmt.Errorf("render CRD KCL module: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create deploy/kcl/lib: %w", err)
	}

	rel, err := filepath.Rel(absProject, outPath)
	if err != nil {
		return fmt.Errorf("relativize %s: %w", outPath, err)
	}
	if _, err := checksums.WriteGeneratedFile(absProject, rel, []byte(body), cs, true); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}

	fmt.Printf("  ✅ Generated %s (%d CRD(s) from api/ Go types)\n", rel, len(docs))
	return nil
}
