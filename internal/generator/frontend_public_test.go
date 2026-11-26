package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPublicFrontendOmitsSignInFlow pins what a public frontend (`forge
// project new --auth-mode none`, `forge scaffold frontend --auth-mode none`)
// leaves out: the sign-in screens and every form, hook and test only they use.
// A guarded (native) tree of the same kind must emit each of those files, so
// signInOnlyFiles cannot name a path that no longer exists and silently
// filter nothing.
func TestPublicFrontendOmitsSignInFlow(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"", "vite-spa"} {
		label := kind
		if label == "" {
			label = "web(default)"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			native := scaffoldFrontendTree(t, kind, false)
			public := scaffoldFrontendTree(t, kind, true)

			for rel := range signInOnlyFiles {
				if !fileExistsAt(native, rel) {
					t.Errorf("signInOnlyFiles names %s, which a native %s frontend does not emit: drop it from the list", rel, label)
				}
				if fileExistsAt(public, rel) {
					t.Errorf("a public %s frontend emitted %s, part of the sign-in flow it has no use for", label, rel)
				}
			}
			if _, err := os.Stat(filepath.Join(public, "src", "app", "auth")); err == nil {
				t.Errorf("a public %s frontend emitted src/app/auth/: it has no sign-in screens", label)
			}
			if _, err := os.Stat(filepath.Join(public, "src", "app", "account")); err == nil {
				t.Errorf("a public %s frontend emitted src/app/account/: it has no signed-in person", label)
			}
			// The session plumbing and the unmounted guard stay: the shell
			// reads the session, and the guard is the seam gating it later
			// re-attaches to.
			for _, rel := range []string{"src/lib/auth/context.tsx", "src/lib/auth/use-auth.ts", "src/lib/auth/route-guard.tsx"} {
				if !fileExistsAt(public, rel) {
					t.Errorf("a public %s frontend is missing %s", label, rel)
				}
			}
			assertPublicTreeNeverCallsAuth(t, label, public)
		})
	}

	// No RouteGuard is mounted in a public Next.js tree.
	public := scaffoldFrontendTree(t, "", true)
	providers, err := os.ReadFile(filepath.Join(public, "src", "app", "providers.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(providers), "<RouteGuard") {
		t.Error("a public Next.js frontend mounts the RouteGuard in providers.tsx")
	}
}

// assertPublicTreeNeverCallsAuth pins that a public frontend has no client of
// the API's sign-in routes: no source file names /auth/session or
// /auth/logout (the session provider asked the first twice per page load,
// and a project with no sign-in serves neither — every load logged two 404s),
// and none renders a password field. The tests and the unmounted route
// guard, whose sign-in paths are strings in a list nothing mounts, are
// exempt.
func assertPublicTreeNeverCallsAuth(t *testing.T, label, dir string) {
	t.Helper()
	err := filepath.WalkDir(filepath.Join(dir, "src"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		if strings.Contains(rel, ".test.") || rel == "src/lib/auth/route-guard.tsx" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for n, line := range strings.Split(string(b), "\n") {
			// Prose may mention the routes; only code calls them.
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
				continue
			}
			for _, banned := range []string{"/auth/session", "/auth/logout", `type="password"`, "native-login"} {
				if strings.Contains(line, banned) {
					t.Errorf("a public %s frontend's %s:%d names %q: it has no sign-in to call", label, rel, n+1, banned)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestEnsureCoreComponentsKeepsAPublicFrontendFreeOfSignInComponents pins the
// generate lane: a public frontend (no src/lib/auth/native-login.ts) was
// scaffolded without login_form, and `forge generate`'s component install
// must not hand it one after all, while a frontend that signs in still gets it.
func TestEnsureCoreComponentsKeepsAPublicFrontendFreeOfSignInComponents(t *testing.T) {
	t.Parallel()
	for _, signsIn := range []bool{false, true} {
		fe := filepath.Join(t.TempDir(), "frontends", "web")
		if signsIn {
			if err := os.MkdirAll(filepath.Join(fe, "src", "lib", "auth"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fe, "src", "lib", "auth", "native-login.ts"), []byte("export {};\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := EnsureCoreComponentsIn(fe, ""); err != nil {
			t.Fatal(err)
		}
		if got := fileExistsAt(fe, "src/components/ui/login_form.tsx"); got != signsIn {
			t.Errorf("signs in = %v: login_form.tsx installed = %v, want %v", signsIn, got, signsIn)
		}
		if !fileExistsAt(fe, "src/components/ui/button.tsx") {
			t.Errorf("signs in = %v: the other core components were not installed", signsIn)
		}
	}
}

func scaffoldFrontendTree(t *testing.T, kind string, public bool) string {
	t.Helper()
	root := t.TempDir()
	if err := GenerateFrontendFilesWithOptions(root, "example.com/app", "app", "fe", 8080, kind, FrontendGenOptions{Public: public}); err != nil {
		t.Fatalf("GenerateFrontendFilesWithOptions(kind=%q, public=%v): %v", kind, public, err)
	}
	return filepath.Join(root, "frontends", "fe")
}

func fileExistsAt(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil
}
