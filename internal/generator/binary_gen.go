package generator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// GenerateBinaryFiles emits the files that make up a non-server
// long-running binary scaffold:
//
//   - cmd/<package>/main.go           thin standalone main (own cmd/<bin>/ tree)
//   - internal/<package>/<package>.go main loop with Start/Stop/Run lifecycle
//   - internal/<package>/contract.go  Deps, Service interface, New(deps)
//   - internal/<package>/<package>_test.go basic lifecycle test
//
// The shape is deliberately opinionated: the scaffold solves the
// boilerplate (signal handling, graceful shutdown, config loading) so the
// user can hand-write only the binary's actual business logic.
//
// Each binary gets its OWN cmd/<bin>/ tree (devspace idiom): the primary
// server binary lives under cmd/<server>/ with the full command tree, and
// each secondary binary added here gets cmd/<package>/main.go — a thin,
// self-contained main rather than a subcommand of the server. At deploy
// time KCL emits one Deployment per binary, each running its own image.
//
// CLI/display name (which may contain hyphens) is translated to a Go-
// package-safe form for the directory and `package` declaration; the
// hyphenated form is preserved on the cobra `Use:` field.
func GenerateBinaryFiles(root, modulePath, binaryName string) error {
	binaryPackage := naming.ServicePackage(binaryName)

	// internal/<package>/ — main loop, contract, test.
	binaryDir := filepath.Join(root, "internal", binaryPackage)
	if err := os.MkdirAll(binaryDir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", binaryDir, err)
	}

	// cmd/<package>/ — the secondary binary's own thin main, in its own tree.
	cmdDir := filepath.Join(root, "cmd", binaryPackage)
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", cmdDir, err)
	}

	data := struct {
		Name    string // display form, may contain hyphens
		Package string // Go-package-safe form
		Module  string
	}{
		Name:    binaryName,
		Package: binaryPackage,
		Module:  modulePath,
	}

	files := []struct {
		template string
		dest     string
	}{
		{"binary/cmd-binary.go.tmpl", filepath.Join(cmdDir, "main.go")},
		{"binary/binary.go.tmpl", filepath.Join(binaryDir, binaryPackage+".go")},
		{"binary/contract.go.tmpl", filepath.Join(binaryDir, "contract.go")},
		{"binary/binary_test.go.tmpl", filepath.Join(binaryDir, binaryPackage+"_test.go")},
		// Pre-emit an empty contract_test.go marker so the
		// `forge generate` internal-package contract-test scaffolder
		// sees it exists and skips the canonical scaffold (which
		// renders `pkg.New(pkg.Deps{})` and doesn't account for the
		// two-result `(Service, error)` shape the binary template
		// uses). The file's `_test` package + leading comment make
		// it clear the user owns the body — they can either grow
		// the binary's contract tests here or stick with the
		// lifecycle test in <name>_test.go.
		{"binary/contract_test.go.tmpl", filepath.Join(binaryDir, "contract_test.go")},
	}
	for _, f := range files {
		content, err := renderBinaryTemplate(f.template, data)
		if err != nil {
			return fmt.Errorf("render %s: %w", f.template, err)
		}
		if err := atomicfile.WriteFile(f.dest, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", f.dest, err)
		}
	}
	return nil
}

// renderBinaryTemplate renders one of the binary-scaffold templates from
// the embedded template FS. Mirrors renderWorkerTemplate shape.
func renderBinaryTemplate(name string, data interface{}) ([]byte, error) {
	tmpl, err := templates.ProjectTemplates().Render(name, data)
	if err != nil {
		return nil, err
	}
	return tmpl, nil
}
