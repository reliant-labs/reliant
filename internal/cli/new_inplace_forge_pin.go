package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/forgecompat"
)

// `forge project new --in-place` into a module whose go.mod ALREADY requires
// forge. reliant, which embeds forge, is the case that found this.
//
// forge.yaml's forge_version is a projection of go.mod's forge require (see
// generate_forge_pin.go). A fresh scaffold writes both files; an in-place one
// keeps the user's go.mod, and used to stamp forge.yaml with the RUNNING
// binary's version regardless — reliant got v0.1.44-0.20261010080729-… over a
// go.mod pinning v0.1.44-0.20261007193810-e75c74afb46e. The first generate
// would have converged it, but that generate refused the project before
// reaching the reconcile step, so the disagreement is what the user kept.
//
// The pin is the user's decision, and the scaffold adopts it: forge.yaml
// records exactly what generate's reconcile step would (goModForgeRequire),
// and a --force that replaces go.mod requires the same version again. What the
// scaffold does NOT do is move the pin. When this forge cannot generate into
// the forge the module resolves — the verdicts generate's own compatibility
// check reaches (forgecompat.Decide) — project new refuses before writing
// anything and names the forge to run instead. Scaffolding anyway would leave
// a half-adopted tree whose first generate refuses for the same reason.

// adoptedForge describes how the go.mod an --in-place scaffold keeps gets forge.
type adoptedForge struct {
	// pin is go.mod's forge require exactly as generate's reconcile step
	// reads it — what forge.yaml's forge_version records. "" when there is
	// nothing to follow (forge is replaced, or the require names a version
	// no proxy serves); forge.yaml then records this binary, as before.
	pin string
	// resolved and local are how the toolchain resolves forge in the module
	// (forgecompat.ResolveProjectForge): a version, or a directory (a replace
	// or a go.work `use`). known is false when the toolchain could not say.
	resolved string
	local    bool
	known    bool
}

// inspectAdoptedForge reports how the module at targetPath gets forge, or
// ok=false when there is nothing to adopt: no go.mod, or one that does not
// require forge (the scaffold's own pin rules then apply unchanged).
//
// The toolchain is consulted only for a service scaffold, the one kind whose
// generated code imports forge and so the only one the compatibility verdict
// is about.
func inspectAdoptedForge(targetPath, kind string) (adoptedForge, bool) {
	if !goModRequiresForge(targetPath) {
		return adoptedForge{}, false
	}
	a := adoptedForge{}
	a.pin, _ = goModForgeRequire(targetPath)
	if kind == config.ProjectKindService {
		a.resolved, a.local, a.known = resolveAdoptedForge(targetPath)
	}
	return a, true
}

// resolveAdoptedForge asks the toolchain how forge resolves in a module (one
// `go list -m`). A variable so tests can answer without a module graph.
var resolveAdoptedForge = forgecompat.ResolveProjectForge

// goModRequiresForge reports whether dir's go.mod requires forge at all —
// whatever the version, and whether or not it is replaced.
func goModRequiresForge(dir string) bool {
	path := filepath.Join(dir, "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	f, err := modfile.ParseLax(path, data, nil)
	if err != nil {
		return false
	}
	for _, r := range f.Require {
		if r.Mod.Path == forgeModuleRequirePath {
			return true
		}
	}
	return false
}

// adoptedForgeResolution is the pure decision behind
// checkScaffoldCanResolveForge for a module that already requires forge.
//
// It replaces scaffoldForgeResolution's question for that case. That check
// asks whether a NEW go.mod can be given a forge require at all, and an
// unreleased binary cannot write one. An adopted go.mod already has its
// require, so the question that remains is generate's: can this binary
// generate code against the forge the module resolves? linked is --link-forge,
// which bridges the module to this binary's own checkout; linkableRoot is that
// checkout ("" when there is none); args is the invocation to hand back with
// the pinned forge.
func adoptedForgeResolution(kind string, a adoptedForge, linked bool, binaryVersion, rawBuildVersion, linkableRoot string, args []string) error {
	if kind != config.ProjectKindService || !a.known {
		// No generated code imports forge, or the toolchain cannot say how
		// forge resolves — generate's own check stays silent there too, and
		// its validate step is the backstop.
		return nil
	}
	const ctx = " project new --in-place (forge version)"
	runPinned := "run the forge the module pins, installing nothing:\n    " +
		forgecompat.RunPinnedCommand(a.resolved, args)
	bridge := ""
	if linkableRoot != "" {
		bridge = fmt.Sprintf("\n  or re-run with --link-forge to compile against the checkout this forge was built from "+
			"(%s; a gitignored go.work)", linkableRoot)
	}
	switch forgecompat.Decide(binaryVersion, rawBuildVersion, a.resolved, a.local || linked) {
	case forgecompat.UnreleasableNoBridge:
		return cliutil.UserErr(Name()+ctx,
			fmt.Sprintf("this forge (%s) is an unreleased build no module proxy can serve, and this module's go.mod "+
				"already resolves %s to %s. The code this forge generates may call forge APIs that version lacks, "+
				"so the first generate would refuse the project. The pin is yours and forge will not move it. "+
				"Nothing was written", rawBuildVersion, forgeModuleRequirePath, a.resolved),
			"", runPinned+bridge)
	case forgecompat.StalePin:
		return cliutil.UserErr(Name()+ctx,
			fmt.Sprintf("this forge is %s but this module's go.mod already resolves %s to %s — older than the "+
				"forge generating into it, so the generated code may call forge APIs that version lacks, and the "+
				"first generate would refuse the project. The pin is yours and forge will not move it. "+
				"Nothing was written", binaryVersion, forgeModuleRequirePath, a.resolved),
			"", runPinned+fmt.Sprintf("\n  or move the pin deliberately first: go get %s@%s && go mod tidy",
				forgeModuleRequirePath, binaryVersion)+bridge)
	case forgecompat.BehindPin:
		return cliutil.UserErr(Name()+ctx,
			fmt.Sprintf("this forge is %s, OLDER than the %s %s this module's go.mod already requires. forge's "+
				"KCL schemas are embedded in the binary, so an older forge renders the project against schemas "+
				"the pin is entitled to be newer than. Nothing was written",
				binaryVersion, forgeModuleRequirePath, a.resolved),
			"", runPinned+fmt.Sprintf("\n  or set %s=1 to proceed anyway", forgecompat.SkewOverrideEnv))
	}
	return nil
}

// announceAdoptedForgePin tells the user which forge_version the scaffold is
// recording, when it is not this binary's.
func announceAdoptedForgePin(a adoptedForge) {
	if a.pin == "" {
		return
	}
	fmt.Printf("📌 forge_version: %s — adopting the %s require this module's go.mod already has (this forge is %s)\n",
		a.pin, forgeModuleRequirePath, buildinfo.Version())
}
