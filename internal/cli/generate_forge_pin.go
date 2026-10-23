package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/generator"
)

// forge.yaml's forge_version, for a project whose go.mod requires forge, is a
// PROJECTION of that require — and `forge generate`, the declarative
// reconcile step, is what converges it.
//
// The pin used to move only under `forge project upgrade`. Upgrading a
// project is `go get github.com/reliant-labs/forge@vX` (that is what the code
// compiles against, what CI installs, and what forgecompat checks), so after
// the go get + generate every such project still claimed the old version in
// forge.yaml. That stale value is not cosmetic: `forge project upgrade` reads
// it as the migration baseline, `forge project audit` reports it, and the
// shared CI install script falls back to it. houndersclub carried v0.1.17 in
// forge.yaml over a go.mod on v0.1.18.
//
// This is not generate "re-pinning" the project. The failure the old
// never-write rule guarded against was generate stamping the RUNNING binary's
// version — a +dirty local build nobody else can fetch — as the pin. The
// value here is go.mod's: a version someone chose with `go get`, that a proxy
// serves, identical for everyone who clones the repo. The binary running
// generate plays no part in it.
//
// A project whose go.mod does NOT require forge (a CLI or library that never
// links it) has nothing to project from: forge.yaml IS the source of truth
// there, and this step leaves it alone.
//
// One case is deliberately left to `forge project upgrade`: a hop that
// crosses a release carrying a codemod or a migration this project needs, or
// one wider than the supported upgrade window. The pin is upgrade's baseline
// for exactly those, and advancing it here would make upgrade skip them.
func stepReconcileForgePin(ctx *pipelineContext) error {
	if ctx.Cfg == nil {
		return nil
	}
	required, ok := goModForgeRequire(ctx.AbsPath)
	if !ok {
		return nil
	}
	current := strings.TrimSpace(ctx.Cfg.ForgeVersion)
	if current == required {
		return nil
	}
	if reason := pinHopNeedsUpgrade(ctx.AbsPath, current, required); reason != "" {
		fmt.Printf("ℹ️  go.mod requires %s %s but forge.yaml pins forge_version %s — left for `%s project upgrade`, "+
			"because %s. Run it to migrate and move the pin.\n",
			forgeModuleRequirePath, required, describeVersion(current), Name(), reason)
		return nil
	}
	configPath := filepath.Join(ctx.AbsPath, defaultProjectConfigFile)
	// Journaled like every other write this run: a generate that fails and
	// rewinds leaves forge.yaml as it found it.
	checksums.RecordPreWrite(ctx.AbsPath, defaultProjectConfigFile)
	if err := generator.SetProjectConfigScalar(configPath, "forge_version", required); err != nil {
		return fmt.Errorf("converge forge.yaml forge_version to go.mod's %s: %w", required, err)
	}
	ctx.Cfg.ForgeVersion = required
	fmt.Printf("📌 forge_version %s → %s (follows go.mod's %s require)\n", describeVersion(current), required, forgeModuleRequirePath)
	return nil
}

// goModForgeRequire returns the forge version the project's go.mod requires,
// read from the FILE (no network, no module cache), or ok=false when there is
// none to follow: no go.mod, no forge require, a replace of forge (the
// require version is then a placeholder, and CI refuses a replaced forge
// anyway), or a version that is not a real, fetchable one.
func goModForgeRequire(projectDir string) (string, bool) {
	path := filepath.Join(projectDir, "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	// Parse, not ParseLax: lax mode is for DEPENDENCY go.mods and drops
	// main-module-only directives, replace among them, so the replace check
	// below never saw one. Lax stays as the fallback for a go.mod carrying a
	// directive this x/mod predates; it still reads the require.
	f, err := modfile.Parse(path, data, nil)
	if err != nil {
		if f, err = modfile.ParseLax(path, data, nil); err != nil {
			return "", false
		}
	}
	for _, r := range f.Replace {
		if r.Old.Path == forgeModuleRequirePath {
			return "", false
		}
	}
	for _, r := range f.Require {
		if r.Mod.Path != forgeModuleRequirePath {
			continue
		}
		v := r.Mod.Version
		if !semver.IsValid(v) || module.IsZeroPseudoVersion(v) || semver.Build(v) != "" {
			return "", false
		}
		return v, true
	}
	return "", false
}

// pinHopNeedsUpgrade reports why moving the pin from → to is
// `forge project upgrade`'s job, or "" when generate may move it.
//
// Only a forward hop can skip anything: moving the pin back (go.mod was
// downgraded) crosses no migration.
func pinHopNeedsUpgrade(projectDir, from, to string) string {
	if !baselineIsUnknown(from) && !baselinePrecedes(from, to) {
		return ""
	}
	if hop := minorHopDistance(from, to); hop > supportedUpgradeWindowMinors {
		return fmt.Sprintf("it spans %d minor releases, more than the %d `forge project upgrade` stages in one step",
			hop, supportedUpgradeWindowMinors)
	}
	if names := pendingUpgradeCodemods(projectDir, from, to); len(names) > 0 {
		return fmt.Sprintf("codemod %s still has to rewrite this project (or run `forge fix %s` yourself)",
			strings.Join(names, ", "), strings.Join(names, "` / `forge fix "))
	}
	metas, err := loadMigrationMetas()
	if err != nil {
		return ""
	}
	state, _ := readMigrationsState(projectDir)
	for _, row := range applicableMigrations(metas, from, projectDir) {
		if _, done := state.Applied[row.Meta.ID]; done {
			continue
		}
		// A migration at or below the new pin would stop applying the
		// moment the pin moved past it.
		if baselinePrecedes(to, row.Meta.Version) {
			continue
		}
		return fmt.Sprintf("this project still needs migration %s", row.Meta.ID)
	}
	return ""
}

// pendingUpgradeCodemods names the upgrade codemods a from → to move
// crosses that would still rewrite something in this project (a dry run of
// each — upgrade_codemod.go). A codemod with nothing to rewrite here does not
// hold the pin back: it decides from the tree, so crossing it is a no-op.
func pendingUpgradeCodemods(projectDir, from, to string) []string {
	var out []string
	for _, c := range upgradeCodemods(from, to) {
		rep, err := c.Run(projectDir, codemod.Options{})
		if err != nil || rep.Changed() {
			out = append(out, c.Name)
		}
	}
	return out
}
