package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/changesdecl"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// stepChangesAccessors projects changes.yaml into typed accessors: Go
// internal/changes/changes_gen.go when the project is a Go module, and
// src/lib/changes_gen.ts in every web frontend that depends on the web
// runtime. Without changes.yaml (or with one declaring no change) it writes
// nothing and retires the files an earlier declaration produced, unless the
// user disowned or edited them.
//
// An invalid changes.yaml fails the run with every problem's line: the same
// file ships in every env's release, so a declaration generate accepted and
// the release refused would only move the error to the deploy.
func stepChangesAccessors(ctx *pipelineContext) error {
	decl, err := changesdecl.Load(ctx.ProjectDir)
	if err != nil {
		return err
	}
	return generateChangesAccessors(ctx.ProjectDir, ctx.Cfg, decl, ctx.Checksums)
}

func generateChangesAccessors(projectDir string, cfg *config.ProjectConfig, decl *changesdecl.Decl, cs *checksums.FileChecksums) error {
	declared := decl != nil && len(decl.Changes) > 0

	if cfg == nil || cfg.HasGoModule() {
		if !declared {
			codegen.RetireGenerated(projectDir, codegen.ChangesGoFile, cs)
		} else {
			src, err := codegen.GenerateChangesGo(decl)
			if err != nil {
				return err
			}
			if _, err := checksums.WriteDerivedFileOutcome(projectDir, codegen.ChangesGoFile, src, cs, true); err != nil {
				return fmt.Errorf("write %s: %w", codegen.ChangesGoFile, err)
			}
		}
	}

	if cfg == nil {
		return nil
	}
	var ts []byte
	if declared {
		var err error
		if ts, err = codegen.GenerateChangesTS(decl); err != nil {
			return err
		}
	}
	for _, fe := range cfg.Frontends {
		if !changesWebFrontend(fe.Type) {
			continue
		}
		feDir, ok := fe.Dir(projectDir)
		if !ok {
			continue // a cross-repo source pin: no directory here
		}
		rel := filepath.Join(feDir, filepath.FromSlash(codegen.ChangesTSFile))
		if !declared {
			codegen.RetireGenerated(projectDir, rel, cs)
			continue
		}
		if !dependsOnWebRuntime(filepath.Join(projectDir, feDir)) {
			fmt.Printf("  ⏭️  %s: frontend %s does not depend on %s, which the accessors import; add it to its package.json to get %s\n",
				changesdecl.FileName, fe.Name, webRuntimePackage, codegen.ChangesTSFile)
			continue
		}
		if _, err := checksums.WriteDerivedFileOutcome(projectDir, rel, ts, cs, true); err != nil {
			return fmt.Errorf("write %s for %s: %w", codegen.ChangesTSFile, fe.Name, err)
		}
	}
	return nil
}

// changesWebFrontend reports whether a frontend of type feType evaluates
// changes through the web runtime: Next.js and Vite. React Native reads no
// browser snapshot.
func changesWebFrontend(feType string) bool {
	return frontendPlatform(feType) != "react-native"
}

// dependsOnWebRuntime reports whether the frontend at dir declares the web
// runtime in its package.json, so an import of it resolves.
func dependsOnWebRuntime(dir string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var m struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	_, dep := m.Dependencies[webRuntimePackage]
	_, dev := m.DevDependencies[webRuntimePackage]
	return dep || dev
}
