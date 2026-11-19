package templates

import (
	"strings"
	"testing"
)

// The browser half of identity v2: the sign-in methods a LOGIN_POLICY turns
// on. Their behaviour is pinned by the scaffolded vitest suite
// (src/lib/auth/sign-in-methods.test.tsx); this holds the composition — every
// page exists in both browser trees, is public, and the emailed-link pages
// stay inert until a button is pressed.

// signInMethodPages are the pages an emailed link (or the social return)
// opens, by the component that renders them.
var signInMethodPages = map[string]string{
	"/auth/magic":           "MagicLinkForm",
	"/auth/invite":          "AcceptInviteForm",
	"/auth/verify":          "VerifyEmailForm",
	"/auth/social/callback": "SocialCallback",
}

func TestBrowserTreesComposeTheSignInMethodPages(t *testing.T) {
	for _, kind := range browserKinds {
		t.Run(kind, func(t *testing.T) {
			tree := authTreeFor(t, kind)
			for _, f := range []string{
				"src/components/sign-in-methods.tsx", "src/components/magic-link-form.tsx",
				"src/components/accept-invite-form.tsx", "src/components/verify-email-form.tsx",
				"src/components/social-callback.tsx", "src/components/passkeys-panel.tsx",
				"src/lib/auth/sign-in-methods.test.tsx",
			} {
				if _, ok := tree[f]; !ok {
					t.Errorf("%s composed no %s", kind, f)
				}
			}
			guard := renderFrontend(t, tree["src/lib/auth/route-guard.tsx"], kind)
			for path := range signInMethodPages {
				if !strings.Contains(guard, `"`+path+`"`) {
					t.Errorf("the route guard does not list %s as public: whoever opens it has no session", path)
				}
			}
			switch kind {
			case "nextjs":
				for path, comp := range signInMethodPages {
					page, ok := tree["src/app"+path+"/page.tsx"]
					if !ok {
						t.Errorf("no Next.js page for %s", path)
						continue
					}
					if src := renderFrontend(t, page, kind); !strings.Contains(src, "<"+comp) {
						t.Errorf("%s does not render %s", path, comp)
					}
				}
			case "vite-spa":
				routes := renderFrontend(t, tree["src/routes.tsx"], kind)
				for path, comp := range signInMethodPages {
					if !strings.Contains(routes, `path: "`+path+`"`) || !strings.Contains(routes, "<"+comp) {
						t.Errorf("routes.tsx has no %s route rendering %s", path, comp)
					}
				}
				for _, r := range []string{"magicRoute,", "inviteRoute,", "verifyRoute,", "socialCallbackRoute,"} {
					if !strings.Contains(routes, r) {
						t.Errorf("routes.tsx does not add %s to the tree", r)
					}
				}
			}
		})
	}
}

// A mail scanner opens every link in an email. The magic-link and
// verification pages must therefore do nothing on load — no effect that
// posts — or the scanner spends the single-use code.
func TestEmailedLinkPagesAreInertUntilPressed(t *testing.T) {
	for _, kind := range browserKinds {
		tree := authTreeFor(t, kind)
		for _, f := range []string{"src/components/magic-link-form.tsx", "src/components/verify-email-form.tsx"} {
			if src := renderFrontend(t, tree[f], kind); strings.Contains(src, "useEffect") {
				t.Errorf("%s (%s) acts on load: a link scanner would spend the code", f, kind)
			}
		}
	}
}

func TestSignInScreensOfferThePolicysMethods(t *testing.T) {
	screens := map[string]string{"nextjs": "src/app/auth/sign-in/page.tsx", "vite-spa": "src/routes.tsx"}
	for _, kind := range browserKinds {
		tree := authTreeFor(t, kind)
		src := renderFrontend(t, tree[screens[kind]], kind)
		for _, want := range []string{
			"<SignInMethods",
			`const offersPassword = options.methods.includes("password");`,
			// No session before the address is confirmed: an open sign-up says "check your email".
			"if (isVerifySent(created)) {",
			`err.code === "email_unverified"`,
			"<UnverifiedNotice email={email} />",
		} {
			if !strings.Contains(src, want) {
				t.Errorf("%s (%s) lacks %q", screens[kind], kind, want)
			}
		}
	}
}
