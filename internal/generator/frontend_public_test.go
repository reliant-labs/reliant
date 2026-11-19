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
			// The session plumbing and the unmounted guard stay: the shell
			// reads the session, and the guard is the seam gating it later
			// re-attaches to.
			for _, rel := range []string{"src/lib/auth/context.tsx", "src/lib/auth/use-auth.ts", "src/lib/auth/route-guard.tsx"} {
				if !fileExistsAt(public, rel) {
					t.Errorf("a public %s frontend is missing %s", label, rel)
				}
			}
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
