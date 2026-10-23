package deploytarget

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeFirebaseFrontend builds a FirebaseFrontend rooted at projectDir
// with a Next.js-style admin export under /admin plus a sibling Vite SPA
// bundled at the site root — the real reliant-web + admin-web recipe.
func fakeFirebaseFrontend(projectDir, staging string) FirebaseFrontend {
	return FirebaseFrontend{
		Name:      "admin-web",
		Path:      "admin-web",
		DevRunner: "npm",
		BuildEnv: map[string]string{
			"NEXT_PUBLIC_API_URL": "https://api.staging.example.com",
		},
		Spec: FirebaseHostingSpec{
			Project:   "reliant-nonprod-490701",
			Site:      "reliant-staging",
			Target:    "reliant-staging",
			PublicDir: "out",
			BasePath:  "/admin",
			Bundle: []BundleDirSpec{
				{Src: "../reliant-web/dist", Dest: ""},
			},
			SPAFallback: "/index.html",
		},
	}
}

// TestFirebaseDryRunPlan asserts that --dry-run prints the build command,
// the assembled layout (admin-web under /admin, the sibling SPA at root),
// and the exact `firebase deploy` command — without running npm/firebase
// or touching the staging tree.
func TestFirebaseDryRunPlan(t *testing.T) {
	projectDir := t.TempDir()
	staging := filepath.Join(t.TempDir(), "public")

	fake := &fakeRunner{}
	prov := FirebaseProvider{ProjectDir: projectDir, Runner: fake, StagingRoot: staging}

	out := captureStdout(t, func() {
		group := ServiceGroup{
			ProviderID: prov.Name(),
			Frontends:  []FirebaseFrontend{fakeFirebaseFrontend(projectDir, staging)},
			DryRun:     true,
		}
		if err := prov.Deploy(context.Background(), group); err != nil {
			t.Fatalf("dry-run deploy: %v", err)
		}
	})

	// No commands should have been executed in dry-run.
	if len(fake.calls) != 0 {
		t.Fatalf("dry-run executed %d command(s); want 0: %v", len(fake.calls), fake.calls)
	}

	wantSubstrings := []string{
		"firebase deploy plan for frontend \"admin-web\"",
		"npm install",
		"npm run build",
		"NEXT_PUBLIC_API_URL=https://api.staging.example.com",
		"-> /admin",       // public_dir mounted under base_path
		"-> /   (bundle:", // sibling SPA at the site root
		"firebase deploy --project reliant-nonprod-490701 --only hosting:reliant-staging --non-interactive",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(out, s) {
			t.Errorf("dry-run output missing %q\n---\n%s", s, out)
		}
	}
}

// TestFirebaseAssembleLayout exercises the real assemble path (no
// dry-run) with a fake runner that no-ops the npm/firebase calls. It
// asserts the staging tree ends up with the admin export under /admin
// and the sibling SPA at the root, and that the firebase deploy command
// was the final exec.
func TestFirebaseAssembleLayout(t *testing.T) {
	projectDir := t.TempDir()
	staging := filepath.Join(t.TempDir(), "public")

	// Pre-create the build outputs the (faked) build would have produced:
	//   admin-web/out/index.html          (the Next.js static export)
	//   reliant-web/dist/index.html       (the sibling Vite SPA)
	writeFile(t, filepath.Join(projectDir, "admin-web", "out", "index.html"), "<admin/>")
	writeFile(t, filepath.Join(projectDir, "admin-web", "out", "assets", "app.js"), "console.log(1)")
	// Bundle src "../reliant-web/dist" is a SIBLING of the project dir.
	writeFile(t, filepath.Join(filepath.Dir(projectDir), "reliant-web", "dist", "index.html"), "<spa/>")

	fake := &fakeRunner{}
	prov := FirebaseProvider{ProjectDir: projectDir, Runner: fake, StagingRoot: staging}

	fe := fakeFirebaseFrontend(projectDir, staging)
	if err := prov.Deploy(context.Background(), ServiceGroup{ProviderID: prov.Name(), Frontends: []FirebaseFrontend{fe}}); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	// Assembled layout: admin export under /admin, SPA at root.
	assertFileContains(t, filepath.Join(staging, "admin", "index.html"), "<admin/>")
	assertFileContains(t, filepath.Join(staging, "admin", "assets", "app.js"), "console.log(1)")
	assertFileContains(t, filepath.Join(staging, "index.html"), "<spa/>")

	// firebase.json + .firebaserc written next to the staging tree.
	// fakeFirebaseFrontend declares an EXPLICIT Target, so firebase.json
	// carries `target` (resolved via .firebaserc) and MUST NOT also carry
	// `site` — the firebase CLI rejects a hosting config with both.
	workdir := filepath.Dir(staging)
	assertFileContains(t, filepath.Join(workdir, "firebase.json"), `"public": "public"`)
	assertFileContains(t, filepath.Join(workdir, "firebase.json"), `"target": "reliant-staging"`)
	assertFileNotContains(t, filepath.Join(workdir, "firebase.json"), `"site"`)
	assertFileContains(t, filepath.Join(workdir, "firebase.json"), `"destination": "/index.html"`)
	assertFileContains(t, filepath.Join(workdir, ".firebaserc"), `"default": "reliant-nonprod-490701"`)
	// With an explicit target, .firebaserc carries the target→site map.
	assertFileContains(t, filepath.Join(workdir, ".firebaserc"), `"reliant-staging"`)

	// The build (install + build) ran in the frontend dir with NODE_ENV
	// + NEXT_PUBLIC_* injected, and the final exec is the firebase deploy.
	joined := strings.Join(fake.calls, "\n")
	if !strings.Contains(joined, "npm install") {
		t.Errorf("expected npm install call; got:\n%s", joined)
	}
	if !strings.Contains(joined, "npm run build") {
		t.Errorf("expected npm run build call; got:\n%s", joined)
	}
	last := fake.calls[len(fake.calls)-1]
	if !strings.Contains(last, "firebase deploy --project reliant-nonprod-490701 --only hosting:reliant-staging --non-interactive") {
		t.Errorf("last call should be firebase deploy; got %q", last)
	}

	// Install and build get DELIBERATELY DIFFERENT env (the devDependency
	// fix): the install call must NOT force NODE_ENV=production (devDeps —
	// typescript, bundlers — are skipped under production, breaking the
	// build with "Cannot find module 'typescript'") and must NOT carry the
	// inline build-time env_vars; the build call must carry
	// NODE_ENV=production + the injected NEXT_PUBLIC_*. The firebase deploy
	// (Run, nil env) carries neither.
	var installEnvSeen, buildEnvSeen map[string]string
	for i, env := range fake.envCalls {
		switch {
		case strings.Contains(fake.calls[i], "npm install"):
			installEnvSeen = env
		case strings.Contains(fake.calls[i], "npm run build"):
			buildEnvSeen = env
		case strings.Contains(fake.calls[i], "firebase deploy"):
			if env != nil {
				t.Errorf("firebase deploy should not carry an env overlay; got %v", env)
			}
		}
	}

	if installEnvSeen == nil {
		t.Fatalf("npm install call did not carry an env overlay; envCalls=%v", fake.envCalls)
	}
	if installEnvSeen["NODE_ENV"] == "production" {
		t.Errorf("npm install must NOT force NODE_ENV=production (it skips devDeps the build needs); got %v", installEnvSeen)
	}
	if _, ok := installEnvSeen["NEXT_PUBLIC_API_URL"]; ok {
		t.Errorf("npm install must NOT carry build-time env_vars; got %v", installEnvSeen)
	}

	if buildEnvSeen == nil {
		t.Fatalf("npm run build call did not carry an env overlay; envCalls=%v", fake.envCalls)
	}
	if buildEnvSeen["NODE_ENV"] != "production" {
		t.Errorf("npm run build must force NODE_ENV=production; got %v", buildEnvSeen)
	}
	if buildEnvSeen["NEXT_PUBLIC_API_URL"] != "https://api.staging.example.com" {
		t.Errorf("npm run build must carry the injected NEXT_PUBLIC_API_URL; got %v", buildEnvSeen)
	}
}

// TestFirebaseTargetDefaultsToSite confirms an unset target falls back to
// the site id in both the deploy command and the rewrites-less config.
func TestFirebaseTargetDefaultsToSite(t *testing.T) {
	projectDir := t.TempDir()
	staging := filepath.Join(t.TempDir(), "public")
	writeFile(t, filepath.Join(projectDir, "web", "dist", "index.html"), "<spa/>")

	fe := FirebaseFrontend{
		Name:      "web",
		Path:      "web",
		DevRunner: "npm",
		Spec: FirebaseHostingSpec{
			Project:   "reliant-labs-475814",
			Site:      "reliant-prod",
			PublicDir: "dist",
		},
	}
	fake := &fakeRunner{}
	prov := FirebaseProvider{ProjectDir: projectDir, Runner: fake, StagingRoot: staging}
	if err := prov.Deploy(context.Background(), ServiceGroup{ProviderID: prov.Name(), Frontends: []FirebaseFrontend{fe}}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	// public_dir at the root (no base_path), no /admin mount.
	assertFileContains(t, filepath.Join(staging, "index.html"), "<spa/>")
	last := fake.calls[len(fake.calls)-1]
	if !strings.Contains(last, "--only hosting:reliant-prod") {
		t.Errorf("target should default to site id; got %q", last)
	}
	// No explicit target → firebase.json carries `site` (deploy by site
	// id) and MUST NOT carry `target`; the firebase CLI rejects both.
	// .firebaserc carries only the default project (no orphan target map).
	workdir := filepath.Dir(staging)
	assertFileContains(t, filepath.Join(workdir, "firebase.json"), `"site": "reliant-prod"`)
	assertFileNotContains(t, filepath.Join(workdir, "firebase.json"), `"target"`)
	assertFileContains(t, filepath.Join(workdir, ".firebaserc"), `"default": "reliant-labs-475814"`)
	assertFileNotContains(t, filepath.Join(workdir, ".firebaserc"), `"targets"`)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(b), want) {
		t.Errorf("%s does not contain %q\n---\n%s", path, want, string(b))
	}
}

func assertFileNotContains(t *testing.T, path, unwanted string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.Contains(string(b), unwanted) {
		t.Errorf("%s unexpectedly contains %q\n---\n%s", path, unwanted, string(b))
	}
}
