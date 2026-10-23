//go:build e2e

package cli

import (
	"path/filepath"
	"testing"
	"time"
)

// TestE2EFrontendAuthPagesBuildAndTest is the frontend gate for the password
// requirements and reset screens, for both browser kinds.
//
// The Go broker is proven against a real identity provider in
// TestE2ENativeSignInPasswordPolicyAndResetAgainstZitadel. This is the half a
// Go test cannot see: that the screens it serves TYPE-CHECK, that the Vite ones
// bundle, and that the
// scaffolded vitest suite — including password.test.tsx, which pins the
// checklist and the rejection mapping — is green.
func TestE2EFrontendAuthPagesBuildAndTest(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	requireTool(t, "node", "npm")

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "authfe", "--mod", "example.com/authfe", "--service", "widget")
	projectDir := filepath.Join(dir, "authfe")
	addCorpusForgePkgReplace(t, projectDir)
	for _, fe := range []struct{ name, kind string }{{"web", "web"}, {"spa", "vite-spa"}} {
		runCmdTimeout(t, projectDir, 10*time.Minute, forgeBin, "scaffold", "frontend", fe.name, "--kind", fe.kind,
			// A project whose dev env declares no identity scaffolds a PUBLIC
			// frontend (no sign-in routes); the screens under test are the
			// native ones.
			"--auth-mode", "native")
	}
	webDir := filepath.Join(projectDir, "frontends", "web")
	spaDir := filepath.Join(projectDir, "frontends", "spa")

	for _, f := range []string{
		"src/components/password-requirements.tsx",
		"src/components/forgot-password-form.tsx",
		"src/components/reset-password-form.tsx",
		"src/lib/auth/password.test.tsx",
	} {
		assertPathExistsE2E(t, filepath.Join(webDir, filepath.FromSlash(f)))
		assertPathExistsE2E(t, filepath.Join(spaDir, filepath.FromSlash(f)))
	}
	assertPathExistsE2E(t, filepath.Join(webDir, "src", "app", "auth", "forgot", "page.tsx"))
	assertPathExistsE2E(t, filepath.Join(webDir, "src", "app", "auth", "reset", "page.tsx"))

	prebuildWebRuntimeE2E(t)
	for _, d := range []string{webDir, spaDir} {
		runCmdTimeout(t, d, 5*time.Minute, "npm", "install", "--no-audit", "--no-fund", "--prefer-offline")
	}

	for _, d := range []string{webDir, spaDir} {
		runCmdTimeout(t, d, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit")
	}
	for _, d := range []string{webDir, spaDir} {
		runCmdTimeout(t, d, 5*time.Minute, "npm", "test")
	}

	// The Next.js static export (`npm run build`) is not asserted here: it is
	// covered by TestE2EScaffoldFrontendBuilds, and the three auth routes are
	// static by construction (no dynamic segment; /auth/reset reads its query in
	// the browser), which `forge lint --static-export` checks.
	// Vite: one bundle serving every route.
	runCmdTimeout(t, spaDir, 5*time.Minute, "npm", "run", "build")
}
