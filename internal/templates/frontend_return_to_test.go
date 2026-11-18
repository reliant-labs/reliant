package templates

import (
	"strings"
	"testing"
)

// The sign-in screens' returnTo is attacker-supplied. A prefix check
// ("starts with /, not //") let `/%5Cevil.com` and `/%09/evil.com` through:
// the browser's URL parser turns both into `//evil.com`. Both screens must go
// through safeReturnTo, which parses the value and keeps it only on the page's
// own origin (pinned by src/lib/auth/return-to.test.ts).
func TestSignInScreensGuardReturnToByOrigin(t *testing.T) {
	screens := map[string]string{"nextjs": "src/app/auth/sign-in/page.tsx", "vite-spa": "src/routes.tsx"}
	for _, kind := range browserKinds {
		tree := authTreeFor(t, kind)
		for _, f := range []string{"src/lib/auth/return-to.ts", "src/lib/auth/return-to.test.ts"} {
			if _, ok := tree[f]; !ok {
				t.Errorf("%s composed no %s", kind, f)
			}
		}
		src := renderFrontend(t, tree[screens[kind]], kind)
		if !strings.Contains(src, "const returnTo = safeReturnTo(requested);") {
			t.Errorf("%s (%s) does not guard returnTo with safeReturnTo", screens[kind], kind)
		}
		if strings.Contains(src, `!requested.startsWith("//")`) {
			t.Errorf("%s (%s) still has the prefix-only returnTo check", screens[kind], kind)
		}
		helper := renderFrontend(t, tree["src/lib/auth/return-to.ts"], kind)
		if !strings.Contains(helper, `url.origin !== window.location.origin || url.pathname.startsWith("//")`) {
			t.Errorf("return-to.ts (%s) no longer refuses another origin and a // path", kind)
		}
	}
}
