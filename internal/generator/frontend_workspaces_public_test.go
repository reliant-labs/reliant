package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUIWebPackageOmitsSignInComponentsWhenNobodySignsIn pins the shared
// packages/ui-web against the --auth-mode none posture: one package serves
// every web frontend of a pnpm workspace, so it leaves login_form (a password
// field) out exactly when none of them signs in, as installCoreComponents
// does for a public frontend's own src/components/ui/. A credential field in
// an unclaimed deploy's bundle is what the hosted content scan flags, and in
// a project nobody signs in to it is dead code.
func TestUIWebPackageOmitsSignInComponentsWhenNobodySignsIn(t *testing.T) {
	t.Parallel()

	// webFrontend writes the markers that make frontends/<name> a Next.js
	// frontend, and native-login.ts when it signs in.
	webFrontend := func(t *testing.T, dir, name string, signsIn bool) {
		t.Helper()
		fe := filepath.Join(dir, "frontends", name)
		writeFile(t, filepath.Join(fe, "next.config.ts"), "export default {};\n")
		if signsIn {
			writeFile(t, filepath.Join(fe, "src", "lib", "auth", "native-login.ts"), "export {};\n")
		}
	}
	mobileFrontend := func(t *testing.T, dir, name string) {
		t.Helper()
		writeFile(t, filepath.Join(dir, "frontends", name, "app.json"), "{}\n")
	}

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string)
		write func(dir string) error
		want  bool
	}{
		{
			name:  "project new --auth-mode none: the first frontend is public",
			write: func(dir string) error { return WriteFrontendWorkspaceFilesForFrontend(dir, "myapp", true, true) },
			want:  false,
		},
		{
			name:  "project new with sign-in: the first frontend signs in",
			write: func(dir string) error { return WriteFrontendWorkspaceFilesForFrontend(dir, "myapp", true, false) },
			want:  true,
		},
		{
			name:  "a public frontend added beside one that signs in",
			setup: func(t *testing.T, dir string) { webFrontend(t, dir, "web", true) },
			write: func(dir string) error { return WriteFrontendWorkspaceFilesForFrontend(dir, "myapp", true, true) },
			want:  true,
		},
		{
			name:  "generate: every web frontend is public",
			setup: func(t *testing.T, dir string) { webFrontend(t, dir, "web", false); webFrontend(t, dir, "admin", false) },
			write: func(dir string) error { return WriteUIWebPackageFiles(dir, "myapp", true) },
			want:  false,
		},
		{
			name: "generate: a public web frontend beside a mobile one",
			setup: func(t *testing.T, dir string) {
				webFrontend(t, dir, "web", false)
				mobileFrontend(t, dir, "mobile")
			},
			write: func(dir string) error { return WriteFrontendWorkspaceFiles(dir, "myapp", true) },
			want:  false,
		},
		{
			name:  "generate: one web frontend signs in",
			setup: func(t *testing.T, dir string) { webFrontend(t, dir, "web", false); webFrontend(t, dir, "admin", true) },
			write: func(dir string) error { return WriteUIWebPackageFiles(dir, "myapp", true) },
			want:  true,
		},
		{
			// Nothing says nobody signs in: the package keeps its whole seed set.
			name:  "generate: no web frontend yet",
			write: func(dir string) error { return WriteFrontendWorkspaceFiles(dir, "myapp", true) },
			want:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if tc.setup != nil {
				tc.setup(t, dir)
			}
			if err := tc.write(dir); err != nil {
				t.Fatal(err)
			}
			uiDir := filepath.Join(dir, "packages", "ui-web", "src")
			_, err := os.Stat(filepath.Join(uiDir, "components", "ui", "login_form.tsx"))
			if got := err == nil; got != tc.want {
				t.Errorf("packages/ui-web has login_form.tsx = %v, want %v", got, tc.want)
			}
			barrel := mustRead(t, filepath.Join(uiDir, "index.ts"))
			if got := strings.Contains(barrel, "LoginForm"); got != tc.want {
				t.Errorf("packages/ui-web/src/index.ts exports LoginForm = %v, want %v:\n%s", got, tc.want, barrel)
			}
			// Every other core component is there either way.
			if _, err := os.Stat(filepath.Join(uiDir, "components", "ui", "button.tsx")); err != nil {
				t.Errorf("packages/ui-web is missing button.tsx: %v", err)
			}
		})
	}
}
