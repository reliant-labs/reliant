package frontenddeps

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// devBridgeRoot is the shape forge's dev web-runtime bridge writes at a
// project root (generator/frontend_webruntime_devlink.go): a gitignored
// workspace root naming each frontend, with no lockfile until someone runs a
// root install.
const devBridgeRoot = `{"name":"forge-dev-workspace-root","private":true,"workspaces":["frontends/web",".forge-link/*"]}`

const memberManifest = `{"name":"web","version":"0.0.0","private":true}`

const memberLock = `{"name":"web","version":"0.0.0","lockfileVersion":3,"requires":true,"packages":{"":{"name":"web","version":"0.0.0"}}}`

// The bug this pins: a fresh worktree of a bridged project has a tracked
// frontends/web/package-lock.json and the bridge's lockfile-less root, and
// `npm ci` run in the frontend reads the ROOT's lockfile — so it exited
// EUSAGE "no package-lock.json" with the frontend's lockfile right there.
func TestNpmInstallVerb(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"standalone frontend with a lockfile", map[string]string{
			"frontends/web/package.json":      memberManifest,
			"frontends/web/package-lock.json": memberLock,
		}, "ci"},
		{"standalone frontend without a lockfile", map[string]string{
			"frontends/web/package.json": memberManifest,
		}, "install"},
		{"bridge root without a lockfile adopts the frontend", map[string]string{
			"package.json":                    devBridgeRoot,
			"frontends/web/package.json":      memberManifest,
			"frontends/web/package-lock.json": memberLock,
		}, "install"},
		{"bridge root with its own lockfile", map[string]string{
			"package.json":                    devBridgeRoot,
			"package-lock.json":               `{"lockfileVersion":3}`,
			"frontends/web/package.json":      memberManifest,
			"frontends/web/package-lock.json": memberLock,
		}, "ci"},
		{"root package.json that is not a workspace root", map[string]string{
			"package.json":                    `{"name":"tooling","private":true}`,
			"frontends/web/package.json":      memberManifest,
			"frontends/web/package-lock.json": memberLock,
		}, "ci"},
		{"workspace root that does not list the frontend", map[string]string{
			"package.json":                    `{"private":true,"workspaces":["frontends/admin"]}`,
			"frontends/web/package.json":      memberManifest,
			"frontends/web/package-lock.json": memberLock,
		}, "ci"},
		{"glob pattern adopts the frontend", map[string]string{
			"package.json":                    `{"private":true,"workspaces":["frontends/*"]}`,
			"frontends/web/package.json":      memberManifest,
			"frontends/web/package-lock.json": memberLock,
		}, "install"},
		{"negated pattern excludes the frontend", map[string]string{
			"package.json":                    `{"private":true,"workspaces":["frontends/*","!frontends/web"]}`,
			"frontends/web/package.json":      memberManifest,
			"frontends/web/package-lock.json": memberLock,
		}, "ci"},
		{"yarn-classic packages object", map[string]string{
			"package.json":                    `{"private":true,"workspaces":{"packages":["frontends/web"]}}`,
			"frontends/web/package.json":      memberManifest,
			"frontends/web/package-lock.json": memberLock,
		}, "install"},
		{"shrinkwrap at the root counts as a lockfile", map[string]string{
			"package.json":                    devBridgeRoot,
			"npm-shrinkwrap.json":             `{"lockfileVersion":3}`,
			"frontends/web/package.json":      memberManifest,
			"frontends/web/package-lock.json": memberLock,
		}, "ci"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, body := range tc.files {
				writeFileAt(t, root, rel, body)
			}
			fe := filepath.Join(root, "frontends", "web")
			if got := NpmInstallVerb(fe); got != tc.want {
				t.Errorf("NpmInstallVerb = %q, want %q (install root %s)", got, tc.want, NpmInstallRoot(fe))
			}
			// The frozen build/lint install follows the same answer.
			if got := installArgs("npm", fe, true)[0]; got != tc.want {
				t.Errorf("installArgs(npm, frozen) = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNpmInstallVerb_AgreesWithNpm runs the real npm against the bridge's
// layout, so the rule NpmInstallRoot mirrors cannot drift from npm's own:
// `npm ci` in the member must fail (the reason this code exists), and the verb
// NpmInstallVerb picks must succeed. The manifests declare no dependencies,
// so nothing is fetched.
func TestNpmInstallVerb_AgreesWithNpm(t *testing.T) {
	if testing.Short() {
		t.Skip("execs npm; runs in task test")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}
	root := t.TempDir()
	writeFileAt(t, root, "package.json", devBridgeRoot)
	writeFileAt(t, root, "frontends/web/package.json", memberManifest)
	writeFileAt(t, root, "frontends/web/package-lock.json", memberLock)
	fe := filepath.Join(root, "frontends", "web")

	npm := func(verb string) ([]byte, error) {
		cmd := exec.Command("npm", verb, "--no-audit", "--no-fund", "--ignore-scripts", "--offline")
		cmd.Dir = fe
		return cmd.CombinedOutput()
	}
	if out, err := npm("ci"); err == nil {
		t.Fatalf("npm ci in a member of a lockfile-less workspace root succeeded; "+
			"npm no longer reads the root's lockfile and NpmInstallVerb can be simplified:\n%s", out)
	}
	verb := NpmInstallVerb(fe)
	if out, err := npm(verb); err != nil {
		t.Fatalf("npm %s (the verb NpmInstallVerb chose) failed: %v\n%s", verb, err, out)
	}
	// The install wrote the root's lockfile, so `ci` is now runnable — and chosen.
	if got := NpmInstallVerb(fe); got != "ci" {
		t.Fatalf("after a root install, NpmInstallVerb = %q, want ci", got)
	}
	if out, err := npm("ci"); err != nil {
		t.Fatalf("npm ci after the root install failed: %v\n%s", err, out)
	}
}
