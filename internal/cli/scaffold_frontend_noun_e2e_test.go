//go:build e2e

package cli

import (
	"path/filepath"
	"testing"
	"time"
)

// TestE2EAddFrontendKindsProduceABuildableTree is the `forge scaffold
// frontend` counterpart to TestE2EAddVerbsProduceABuildableTree: it adds one
// frontend of EVERY kind to an existing project and then drives each through
// the toolchain that actually resolves modules.
//
// The existing frontend e2e (TestE2EScaffoldFrontendBuilds) covers `project
// new --frontend web`, which runs a full `forge generate` on its way out. The
// ADD verb used not to (it now does, unless --no-generate stages a frontend
// for a later generate), and the files it writes BEFORE that run are where
// two shipped bugs lived:
//
//  1. The scaffolded src/lib/connect.ts references "@/lib/mock-transport",
//     which only `forge generate` emitted. A freshly added vite-spa failed
//     `tsc` outright (TS2307), and a freshly added Next.js frontend
//     typechecked but failed `next build` ("Module not found: Can't resolve
//     '@/lib/mock-transport'") — webpack resolves the literal require() that
//     tsc types as `any`. Hence BOTH checks below for the Next.js kind.
//  2. The Expo scaffold never declared expo-asset. npm nests it under
//     expo/node_modules, but @expo/metro-config resolves it from the project
//     root and hard-throws, so every scaffolded Expo app failed to bundle:
//     "The required package `expo-asset` cannot be found". Only a real bundle
//     surfaces that — the package is never imported by any source file, so
//     neither tsc nor an import-graph check can see it.
//
// COST: minutes. Three `npm install`s (one per added frontend, run by the add
// verb itself), a Next.js production build, and a full Metro bundle. It is
// `-tags e2e` like every other test in this family, so it is off by default
// and runs in the e2e CI lane. The cheap always-on guard for the same class is
// TestFrontendScaffoldImportsResolve (internal/generator), which resolves
// every import in every scaffolded kind in milliseconds — it catches bug 1 but
// structurally cannot catch bug 2.
func TestE2EAddFrontendKindsProduceABuildableTree(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	requireTool(t, "node", "npm")

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "feadd",
		"--mod", "example.com/feadd",
		"--service", "widget",
	)
	projectDir := filepath.Join(dir, "feadd")
	addCorpusForgePkgReplace(t, projectDir)

	// Each add verb runs its own `npm install`, which dominates the wall time.
	for _, fe := range []struct{ name, kind string }{
		{"web", "web"},
		{"spa", "vite-spa"},
		{"mobile", "mobile"},
	} {
		runCmdTimeout(t, projectDir, 10*time.Minute, forgeBin,
			"scaffold", "frontend", fe.name, "--kind", fe.kind)
	}

	webDir := filepath.Join(projectDir, "frontends", "web")
	spaDir := filepath.Join(projectDir, "frontends", "spa")
	mobileDir := filepath.Join(projectDir, "frontends", "mobile")

	// The generated module connect.ts imports must be on disk before any
	// generate pass — name it explicitly so a failure reads as the invariant
	// it is, not as a wall of tsc output. These are Tier-1 renders, so they
	// carry the _gen suffix every generated file does; connect.ts requires
	// "@/lib/mock-transport_gen", so the suffixed name IS the contract.
	assertPathExistsE2E(t, filepath.Join(webDir, "src", "lib", "mock-transport_gen.ts"))
	assertPathExistsE2E(t, filepath.Join(spaDir, "src", "lib", "mock-transport_gen.ts"))
	assertPathExistsE2E(t, filepath.Join(spaDir, "src", "mocks", "scenarios", "index_gen.ts"))

	// The add verb's own `npm install` is BEST-EFFORT by design — a scaffold
	// must still land when npm is missing or slow, so it warns and carries
	// on. That makes it fine for a user and useless as this test's
	// precondition: on a cold npm cache the install can come up short, and
	// then `npx tsc` fails for want of a compiler rather than for anything
	// the scaffold did. Install explicitly, and build the shared web-runtime
	// once up front so the three parallel installs do not race its prepare.
	prebuildWebRuntimeE2E(t)
	for _, dir := range []string{webDir, spaDir, mobileDir} {
		runCmdTimeout(t, dir, 5*time.Minute, "npm", "install", "--no-audit", "--no-fund", "--prefer-offline")
	}

	// ── the trees compile, straight out of the add verb ──────────────
	runCmdTimeout(t, webDir, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit")
	runCmdTimeout(t, spaDir, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit")
	runCmdTimeout(t, mobileDir, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit")

	// ── …and they bundle, which is a strictly stronger claim ─────────
	// Next.js: webpack statically resolves the literal require() that tsc
	// types as `any`, so this is the only check that sees a missing
	// mock-transport on the Next.js kind.
	runCmdTimeout(t, webDir, 6*time.Minute, "npm", "run", "build")
	// Expo: Metro's config resolves expo-asset from the project root before
	// a single module is transformed. `env CI=1` keeps the CLI non-interactive.
	runCmdTimeout(t, mobileDir, 10*time.Minute,
		"env", "CI=1", "npx", "expo", "export", "--platform", "ios")

	// ── and the generate pass agrees with what the scaffold wrote ────
	runCmd(t, projectDir, forgeBin, "generate")
	runCmdTimeout(t, webDir, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit")
	runCmdTimeout(t, spaDir, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit")
}
