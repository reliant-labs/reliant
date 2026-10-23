// Package cli — forge KCL module migration.
//
// A project's env main.k files `import forge`. forge supplies that module
// from the binary that is rendering (internal/kclvendor → internal/kclrender),
// so the project's deploy/kcl/kcl.mod declares NO forge dependency and the
// project holds no copy of the module.
//
// Older forge versions declared it — first as an unpublished `kcl-vX.Y.Z`
// git tag, then as `forge = { path = "../../.forge-kcl" }` over a
// project-local copy — and this step migrates those projects forward: it
// removes the declaration (and the marker block forge maintained around it),
// strips the forge entry from kcl.mod.lock, and deletes `.forge-kcl/`. See
// docs/adr/0003-kcl-module-from-the-binary.md.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/kclvendor"
)

// stepSyncForgeKCL migrates a project off every legacy declaration of the
// forge KCL module. Best-effort: failure warns and the pipeline continues
// (--strict promotes to fatal), matching the forge/pkg sync step. A render
// of an unmigrated project refuses on its own with this step named as the
// fix, so a skipped migration cannot render against a stale copy.
func stepSyncForgeKCL(ctx *pipelineContext) error {
	return ctx.warnOrFail("forge KCL module migration", syncForgeKCL(ctx.ProjectDir))
}

// syncForgeKCL implements the migration. Split from the step for direct
// testing. Idempotent: a migrated project is a byte-identical no-op, under
// any forge build.
func syncForgeKCL(projectDir string) error {
	var migrated []string
	for _, modPath := range kclvendor.ManagedKclMods(projectDir) {
		res, err := kclvendor.MigrateKclMod(modPath)
		if err != nil {
			return err
		}
		if res.Warning != "" {
			fmt.Fprintf(os.Stderr, "⚠️  Warning: %s\n", res.Warning)
			continue
		}
		if res.Changed {
			migrated = append(migrated, projectRelPath(projectDir, modPath))
		}
	}
	removed, err := kclvendor.RemoveLegacyVendorDir(projectDir)
	if err != nil {
		return err
	}
	// The retired forge.registry(default) helper: the registry is a literal
	// the env declares, so each call becomes the literal it evaluated to.
	registryMigrated, err := kclvendor.MigrateRegistryHelper(projectDir)
	if err != nil {
		return err
	}
	if len(registryMigrated) > 0 {
		fmt.Printf("  ✅ %s: forge.registry(\"…\") → the literal registry — the registry is declared in the env's KCL, not overridden at render\n",
			strings.Join(registryMigrated, ", "))
	}
	if len(migrated) > 0 {
		fmt.Printf("  ✅ %s no longer declares the forge KCL module — forge supplies it from the running binary\n",
			strings.Join(migrated, ", "))
	}
	if removed {
		fmt.Printf("  🧹 Removed the legacy project-local %s/ (if it was committed: git rm -r --cached %s)\n",
			kclvendor.LegacyVendorDirName, kclvendor.LegacyVendorDirName)
	}
	return nil
}

// projectRelPath renders path relative to projectDir for messages,
// falling back to the absolute path when Rel fails.
func projectRelPath(projectDir, path string) string {
	if rel, err := filepath.Rel(projectDir, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}
