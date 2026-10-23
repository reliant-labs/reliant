// frontend_auth_flow_test.go — the scaffolded sign-in flow is NATIVE, and
// these guards keep it that way in both browser frontend kinds.
//
// ── What changed ──────────────────────────────────────────────────────
//
// Sign-in used to be a browser-side OIDC authorization-code + PKCE flow:
// the browser fetched the discovery document, redirected to the issuer,
// came back to /auth/callback with a code, redeemed it at the token
// endpoint, and held the resulting tokens in JavaScript. Every one of those
// steps is gone.
//
// The browser now POSTs credentials to THIS APP'S OWN API and gets an
// HttpOnly session cookie. The server (internal/app/login_broker.go, over
// pkg/devidp) runs the whole OIDC flow. The browser never contacts the
// identity provider at all.
//
//	sign in  →  POST /auth/login     → cookie set, identity returned
//	sign up  →  POST /auth/register  → cookie set, identity returned
//	who am I →  GET  /auth/session   → identity, never a token
//	sign out →  POST /auth/logout    → cookie cleared
//
// ── Why these are the assertions ──────────────────────────────────────
//
// The property that makes this design worth having is a NEGATIVE one — the
// token is unreachable from script — and a negative property is exactly
// what no functional test catches. A scaffold that quietly grew a token
// endpoint call, a client secret, or a localStorage write would still sign
// users in perfectly; it would just have given the XSS back its prize. So
// the headline guard below is a scan for absence, run over the whole
// composed browser tree rather than over a hand-listed file set.
//
// Every assertion derives from the COMPOSED TEMPLATE TREE — the same
// listing the scaffolder writes from — rather than from a hardcoded file
// list, so a file that stops being emitted fails here instead of at a
// user's first login.
package templates

import (
	"path"
	"regexp"
	"strings"
	"testing"
)

// browserKinds are the frontend kinds that get native sign-in. React Native
// is deliberately absent — see TestReactNativeShipsNoBrowserSignIn.
var browserKinds = []string{"nextjs", "vite-spa"}

// deletedBrowserOIDCModules are the modules the old browser-side flow lived
// in. They are listed by DESTINATION path, and asserted absent, because a
// half-restored flow is worse than either design on its own: a tree that
// ships both native-login.ts and oidc.ts has two ways to become
// authenticated, and only one of them keeps the token away from script.
var deletedBrowserOIDCModules = []string{
	"src/lib/auth/oidc.ts",
	"src/lib/auth/oidc-provider.ts",
	"src/lib/auth/oidc-storage.ts",
	"src/lib/auth/auth-screens.tsx",
}

// authTreeFor returns the composed tree for a kind, keyed by the file's
// DESTINATION path in the generated project — i.e. FrontendTreeFile.Rel with
// any .tmpl suffix stripped, which is exactly the transformation the
// scaffolder applies when it writes. Keying on the destination means a file
// that changes between a plain and a templated form still satisfies these
// assertions, since the generated project is unchanged either way.
//
// Fails loudly on an empty tree, which would make every assertion below
// vacuously true.
func authTreeFor(t *testing.T, kind string) map[string]string {
	t.Helper()
	files, err := ListFrontendTree(kind)
	if err != nil {
		t.Fatalf("list %s tree: %v", kind, err)
	}
	if len(files) == 0 {
		t.Fatalf("%s composed an EMPTY template tree — this guard is now blind", kind)
	}
	out := make(map[string]string, len(files))
	for _, f := range files {
		out[strings.TrimSuffix(f.Rel, ".tmpl")] = f.Path
	}
	return out
}

// renderFrontend renders one template for a kind.
func renderFrontend(t *testing.T, tmplPath, kind string) string {
	t.Helper()
	content, err := FrontendTemplates().Render(tmplPath, FrontendTemplateData{
		FrontendName: "web",
		ProjectName:  "demo",
		Platform:     kind,
		Module:       "example.com/demo",
	})
	if err != nil {
		t.Fatalf("render %s for %s: %v", tmplPath, kind, err)
	}
	return string(content)
}

// isBrowserSource reports whether a destination path is TypeScript that ends
// up in the browser bundle. The scan below is about what the BROWSER runs, so
// KCL, JSON, CSS and Go-side templates are deliberately out of scope: the
// server is exactly where the issuer URL is supposed to live.
func isBrowserSource(rel string) bool {
	if !strings.HasPrefix(rel, "src/") {
		return false
	}
	return strings.HasSuffix(rel, ".ts") || strings.HasSuffix(rel, ".tsx")
}

// idpContact is one way a browser could reach an identity provider, paired
// with the sentence a failure should make a reader understand.
type idpContact struct {
	name string
	re   *regexp.Regexp
	why  string
}

// idpContacts are the traces the browser-side OIDC flow leaves behind. Each
// is matched against CODE, with comments stripped: the scaffold's prose
// explains at length what it no longer does, and a guard that fires on its
// own documentation is one people delete.
var idpContacts = []idpContact{
	{
		"an authorization endpoint", regexp.MustCompile(`/authorize\b`),
		"a redirect to the issuer's /authorize puts the whole flow back in the browser",
	},
	{
		"OIDC discovery", regexp.MustCompile(`openid-configuration`),
		"fetching the discovery document means the browser is talking to the issuer directly",
	},
	{
		"a token endpoint", regexp.MustCompile(`token_endpoint|/oauth/token|/oauth2/token`),
		"redeeming a code in the browser lands the access token in JavaScript, which is the one thing this design exists to prevent",
	},
	{
		"a PKCE code challenge", regexp.MustCompile(`code_challenge|code_verifier`),
		"PKCE only exists to protect a browser-side code exchange; its presence means one is happening",
	},
	{
		"an OIDC grant", regexp.MustCompile(`grant_type`),
		"a grant type is a token-endpoint parameter, so the browser is redeeming a credential itself",
	},
	{
		"an OIDC client id", regexp.MustCompile(`\bclient_id\b|\bclientId\b`),
		"the client id is only needed to address the issuer; the server holds it now",
	},
	{
		// Case-insensitive: a hardcoded issuer is at least as likely to
		// arrive spelled `ISSUER_URL` or `oidcIssuer` as `issuer`, and the
		// case-sensitive version of this pattern was verified to miss it.
		"an issuer", regexp.MustCompile(`(?i)\bissuer\b|(?i)issuer_?url`),
		"the browser has no issuer configuration at all — the server does, and it answers `who is signed in?` over this app's own API",
	},
	{
		// The name-based patterns above all assume the code calls the thing
		// what it is. A pasted URL does not, so match the endpoints and the
		// hosted-IdP vendors by shape. Not exhaustive and not meant to be:
		// it catches the copy-paste that skips every other pattern here.
		"an identity provider URL", regexp.MustCompile(`(?i)https?://[^"'\s]*(auth0\.com|okta\.com|zitadel|login\.microsoftonline|accounts\.google\.com|/realms/)`),
		"a provider URL in browser code means the browser is addressing the IdP, whatever the surrounding identifier is called",
	},
}

// TestBrowserNeverContactsTheIdentityProvider is the headline guarantee.
//
// THE BROWSER NEVER TALKS TO THE IDP. Not /authorize, not discovery, not the
// token endpoint, not a hidden iframe. It POSTs credentials to this app's own
// API and receives an HttpOnly cookie; the server does the OIDC.
//
// This is a scan for ABSENCE across the whole composed browser tree, and it
// is written that way on purpose. A regression here does not break sign-in —
// a scaffold that grew a token-endpoint call would log users in perfectly
// well — it silently moves the access token back into JavaScript, where any
// XSS can read it. There is no functional test that can notice that, so the
// static scan IS the test.
func TestBrowserNeverContactsTheIdentityProvider(t *testing.T) {
	t.Parallel()

	for _, kind := range browserKinds {
		tree := authTreeFor(t, kind)

		// The deleted modules must not come back. Half-restoring the old
		// flow gives a tree two ways to authenticate, and only one of them
		// keeps the token out of script.
		for _, rel := range deletedBrowserOIDCModules {
			if _, ok := tree[rel]; ok {
				t.Errorf("%s composed %s — the browser-side OIDC flow was replaced by native sign-in (POST /auth/login + HttpOnly cookie).\n"+
					"    Resurrecting it puts the access token back in JavaScript, which is the exact property the new design buys.", kind, rel)
			}
		}

		// Guard against a scan that has quietly stopped scanning anything.
		scanned := 0
		for rel, tmplPath := range tree {
			if !isBrowserSource(rel) {
				continue
			}
			scanned++
			src := stripCommentsKeepingStrings(renderFrontend(t, tmplPath, kind))
			for _, contact := range idpContacts {
				if m := contact.re.FindString(src); m != "" {
					t.Errorf("%s: %s mentions %s (matched %q) in CODE.\n"+
						"    %s\n"+
						"    The browser POSTs credentials to this app's own API and gets an HttpOnly cookie; the server runs the OIDC flow.",
						kind, rel, contact.name, m, contact.why)
				}
			}
		}
		if scanned == 0 {
			t.Fatalf("%s: scanned NO browser sources — this guard is blind", kind)
		}
	}
}

// TestBothBrowserKindsShipNativeSignIn pins that each browser kind carries
// every piece the flow needs.
//
// The defect this guards against is the mirror image of the one
// frontend_routes_test.go documents: a scaffold that ships auth UI whose
// supporting module is not emitted. Each tree is internally consistent, so
// the gap only shows up at a user's first login.
func TestBothBrowserKindsShipNativeSignIn(t *testing.T) {
	t.Parallel()

	requiredModules := []string{
		// The browser half of native sign-in: the only file that talks to
		// the API's auth endpoints.
		"src/lib/auth/native-login.ts",
		// What an anonymous visitor sees.
		"src/lib/auth/route-guard.tsx",
		// The app's auth state, and the seam types behind it. The mock must
		// remain reachable: pure-mock frontend development is a supported
		// mode, not a legacy state.
		"src/lib/auth/context.tsx",
		"src/lib/auth/provider.ts",
		"src/lib/auth/session-provider.ts",
	}

	for _, kind := range browserKinds {
		tree := authTreeFor(t, kind)
		for _, rel := range requiredModules {
			if _, ok := tree[rel]; !ok {
				t.Errorf("%s: composed tree has no %s — the sign-in flow is incomplete in this kind", kind, rel)
			}
		}
	}
}

// TestCredentialsGoToTheAppsOwnAPI pins the four endpoints and, more
// importantly, the fetch option that makes them work.
//
// `credentials: "include"` is the assertion worth having. In the dev loop the
// API is on its own port, so every auth call is cross-origin — and under the
// default ("same-origin") the browser SILENTLY DROPS the Set-Cookie. Login
// returns 200, the identity comes back, and the user stays signed out, with
// nothing in the console to explain it. It is the single most confusing way
// this flow can fail, and a one-word edit reintroduces it.
func TestCredentialsGoToTheAppsOwnAPI(t *testing.T) {
	t.Parallel()

	const rel = "src/lib/auth/native-login.ts"

	for _, kind := range browserKinds {
		tree := authTreeFor(t, kind)
		tmplPath, ok := tree[rel]
		if !ok {
			t.Fatalf("%s: no %s in the composed tree", kind, rel)
		}
		src := stripCommentsKeepingStrings(renderFrontend(t, tmplPath, kind))

		// The four paths, read from the code rather than from the prose that
		// tabulates them in the file header.
		for _, endpoint := range []string{"/auth/login", "/auth/register", "/auth/session", "/auth/logout"} {
			if !strings.Contains(src, `"`+endpoint+`"`) && !strings.Contains(src, endpoint+"`") {
				t.Errorf("%s: %s never names %s — the scaffolded server (internal/app/login_broker.go) serves it, so a browser that does not call it has half a flow",
					kind, rel, endpoint)
			}
		}

		// Every request must be resolved against the API origin, not against
		// the page's. They are different hosts in dev, and a bare relative
		// path would 404 against the frontend dev server.
		if !strings.Contains(src, "apiOrigin()") {
			t.Errorf("%s: %s does not resolve its requests against apiOrigin() — in the dev loop the API is on its own port, so a page-relative path hits the Vite/Next dev server instead",
				kind, rel)
		}

		// Every fetch must send and accept cookies. Counted rather than
		// merely present: one uncredentialed call among several is the same
		// silent-failure bug, confined to whichever endpoint it is on.
		fetches := regexp.MustCompile(`\bfetch\(`).FindAllString(src, -1)
		if len(fetches) == 0 {
			t.Fatalf("%s: %s calls fetch() nowhere — either the file no longer talks to the API or this guard has gone blind:\n%s", kind, rel, src)
		}
		includes := regexp.MustCompile(`credentials:\s*"include"`).FindAllString(src, -1)
		if len(includes) != len(fetches) {
			t.Errorf("%s: %s has %d fetch() call(s) but %d `credentials: \"include\"`.\n"+
				"    Every auth call is CROSS-ORIGIN in the dev loop, and the default (\"same-origin\") makes the browser drop the session cookie without a word:\n"+
				"    login returns 200, the identity comes back, and the user stays signed out. The server pairs this with cors_allow_credentials.",
				kind, rel, len(fetches), len(includes))
		}
	}
}

// TestRPCTransportCarriesTheSessionCookie is the other half of the test
// above: signing in is useless if the RPCs that follow do not carry the
// session. Each real-backend Connect transport must be built on a fetch that
// sends credentials. The Next.js tree used the browser default
// ("same-origin"), so in the dev loop — API on its own port — every RPC after
// a successful sign-in went out without the cookie and answered 401.
func TestRPCTransportCarriesTheSessionCookie(t *testing.T) {
	t.Parallel()

	const rel = "src/lib/connect.ts"
	for _, kind := range browserKinds {
		tree := authTreeFor(t, kind)
		tmplPath, ok := tree[rel]
		if !ok {
			t.Fatalf("%s: no %s in the composed tree", kind, rel)
		}
		src := stripCommentsKeepingStrings(renderFrontend(t, tmplPath, kind))

		transports := regexp.MustCompile(`createConnectTransport\(\{`).FindAllStringIndex(src, -1)
		if len(transports) == 0 {
			t.Fatalf("%s: %s builds no Connect transport — this guard has gone blind:\n%s", kind, rel, src)
		}
		for _, loc := range transports {
			body := src[loc[1]:]
			body = body[:strings.Index(body, "})")]
			if !strings.Contains(body, "fetch: fetchWithSession") {
				t.Errorf("%s: a transport in %s is not built on fetchWithSession, so its RPCs drop the session cookie cross-origin:\n%s",
					kind, rel, body)
			}
		}
		if !regexp.MustCompile(`fetchWithSession[^=]*=[^;]*credentials:\s*"include"`).MatchString(src) {
			t.Errorf("%s: %s has no fetchWithSession that sends credentials: \"include\"", kind, rel)
		}
	}
}

// TestTheSessionTokenIsUnreachableFromScript pins the property the whole
// design exists for.
//
// The session cookie is HttpOnly, so no script can read it — that is what
// makes the token something an XSS cannot steal. The guard is that the
// browser half never tries to hold one itself: no persistent store, no
// in-memory token, no Authorization header. The cookie rides along
// automatically, so any code that reaches for a token is code that has
// started keeping a second, script-readable credential beside the one that
// is protected.
func TestTheSessionTokenIsUnreachableFromScript(t *testing.T) {
	t.Parallel()

	const rel = "src/lib/auth/native-login.ts"

	forbidden := []struct {
		name string
		re   *regexp.Regexp
		why  string
	}{
		{
			"a persistent store", regexp.MustCompile(`localStorage|sessionStorage|document\.cookie`),
			"anything written here is readable by script, which is the exact exposure the HttpOnly cookie removes",
		},
		{
			"an Authorization header", regexp.MustCompile(`(?i)authorization\s*:|Bearer `),
			"the session cookie is attached automatically; a bearer header means the code got a token from somewhere it should not have one",
		},
		{
			"a token field", regexp.MustCompile(`access_token|refresh_token|id_token`),
			"the server keeps the tokens; the browser is only ever told WHO is signed in",
		},
	}

	for _, kind := range browserKinds {
		tree := authTreeFor(t, kind)
		tmplPath, ok := tree[rel]
		if !ok {
			t.Fatalf("%s: no %s in the composed tree", kind, rel)
		}
		src := stripCommentsKeepingStrings(renderFrontend(t, tmplPath, kind))

		for _, f := range forbidden {
			if m := f.re.FindString(src); m != "" {
				t.Errorf("%s: %s contains %s (matched %q).\n    %s", kind, rel, f.name, m, f.why)
			}
		}

		// And the positive half: what the browser DOES get back is an
		// identity, for rendering. If this ever disappears the file has
		// stopped being the thing these negatives are guarding.
		if !strings.Contains(src, "interface Identity") {
			t.Errorf("%s: %s no longer declares an Identity — the API answers with who is signed in, never with a token, and that shape is what the rest of the app reads",
				kind, rel)
		}
	}
}

// TestBothBrowserKindsRouteTheSignInPath pins that each kind actually SERVES
// the sign-in screen, in whatever way that kind expresses a route.
//
// There is exactly ONE auth route now. /auth/callback belonged to the browser
// redirect flow — the issuer sent the browser back there with a code — and
// with the exchange moved server-side there is nothing for it to do.
//
// The two mechanisms are genuinely different, so this checks each on its own
// terms rather than grepping for a shared string:
//
//   - Next.js App Router: a `page.tsx` at the path's directory.
//   - Vite + TanStack Router: a `createRoute({... path: "<p>" ...})` entry in
//     the scaffolded routes.tsx.
func TestBothBrowserKindsRouteTheSignInPath(t *testing.T) {
	t.Parallel()

	const signInPath = "/auth/sign-in"

	// ── Next.js: the route IS the file path. ──
	nextTree := authTreeFor(t, "nextjs")
	rel := path.Join("src", "app", strings.TrimPrefix(signInPath, "/"), "page.tsx")
	if _, ok := nextTree[rel]; !ok {
		t.Errorf("nextjs serves no %s: expected the App Router page %s.\n"+
			"    The route guard redirects anonymous visitors there, so a scaffold that does not emit it bounces them to a 404.",
			signInPath, rel)
	}

	// ── Vite: the route is an entry in routes.tsx. ──
	viteTree := authTreeFor(t, "vite-spa")
	routesRel := path.Join("src", "routes.tsx")
	routesTmpl, ok := viteTree[routesRel]
	if !ok {
		t.Fatalf("vite-spa composed no %s — the router entry point is gone", routesRel)
	}
	routes := renderFrontend(t, routesTmpl, "vite-spa")

	// Derived from the router's own declaration form, so a route mentioned
	// only in a comment does not count.
	declared := regexp.MustCompile(`path:\s*"` + regexp.QuoteMeta(signInPath) + `"`)
	if !declared.MatchString(stripComments(routes)) {
		t.Errorf("vite-spa serves no %s: %s declares no `path: %q` route.\n"+
			"    The Next.js scaffold serves it, so a Vite project's guard would redirect into a 404 where a Next.js one works.",
			signInPath, routesRel, signInPath)
	}

	// A route declared but never added to the tree is not served.
	if !strings.Contains(routes, "signInRoute") {
		t.Fatalf("vite-spa routes.tsx does not reference signInRoute — a route that is declared but not added to the route tree is not served")
	}
	addChildren := regexp.MustCompile(`(?s)addChildren\(\[(.*?)\]`).FindStringSubmatch(routes)
	if addChildren == nil {
		t.Fatal("vite-spa routes.tsx has no addChildren([...]) call — cannot verify the sign-in route is mounted")
	}
	if !strings.Contains(addChildren[1], "signInRoute") {
		t.Error("vite-spa: signInRoute is declared but never passed to addChildren — the route exists in the file and not in the app")
	}
}

// stripCommentsKeepingStrings removes // and /* */ comments while leaving
// string literals intact.
//
// The package's own stripComments cannot be used here: it is documented as
// naive about "//" inside a string, which is harmless for the route guard it
// was written for (an absolute URL is not a same-origin route) but fatal for
// THESE guards — it truncates `"https://tenant.auth0.com/oidc"` at the "//",
// deleting the very thing being looked for. Verified: with stripComments, a
// hardcoded issuer URL passed this test.
func stripCommentsKeepingStrings(src string) string {
	var out strings.Builder
	var quote byte // 0 when not inside a string
	for i := 0; i < len(src); i++ {
		c := src[i]

		if quote != 0 {
			out.WriteByte(c)
			switch {
			case c == '\\' && i+1 < len(src):
				// Escape: consume the next byte so an escaped quote does not
				// end the literal.
				i++
				out.WriteByte(src[i])
			case c == quote:
				quote = 0
			}
			continue
		}

		switch {
		case c == '"' || c == '\'' || c == '`':
			quote = c
			out.WriteByte(c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			out.WriteByte('\n')
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++ // land on '/', loop's i++ moves past it
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// TestReactNativeShipsNoBrowserSignIn pins the deliberate omission.
//
// Native sign-in is a COOKIE flow: the server's answer is a Set-Cookie the
// browser stores and re-attaches on its own. React Native does not share that
// model — there is no cookie jar attached to fetch by default and no
// same-origin to be same as — so shipping native-login.ts into an RN tree
// would produce code that typechecks (RN's tsconfig includes the DOM lib) and
// silently never authenticates: every call returns 200 and the next one is
// anonymous.
//
// The route guard is excluded for the same reason plus a second one: it is
// written against a web router's pathname.
func TestReactNativeShipsNoBrowserSignIn(t *testing.T) {
	t.Parallel()

	tree := authTreeFor(t, "react-native")

	excluded := append([]string{
		"src/lib/auth/native-login.ts",
		"src/lib/auth/route-guard.tsx",
	}, deletedBrowserOIDCModules...)
	for _, rel := range excluded {
		if _, ok := tree[rel]; ok {
			t.Errorf("react-native composed %s — it is written against the browser's cookie and routing model, neither of which exists on this platform", rel)
		}
	}

	// It must still have a working provider seam, or the RN scaffold has no
	// auth at all.
	for _, rel := range []string{
		"src/lib/auth/provider.ts",
		"src/lib/auth/session-provider.ts",
		"src/lib/auth/context.tsx",
	} {
		if _, ok := tree[rel]; !ok {
			t.Errorf("react-native composed no %s — the AuthProvider seam is missing entirely", rel)
		}
	}
}

// TestAuthContextReadsIdentityFromTheServer pins the shape of the app's auth
// state, per platform.
//
// On the web there is one question — "who is signed in?" — and one way to
// answer it: ask this app's own API, because the cookie that holds the answer
// is unreadable to script. The context therefore exposes an IDENTITY and no
// token. A `getToken` here would be a promise the design cannot keep: the
// only honest implementation returns null, and every caller that believed it
// would ship broken.
//
// React Native keeps its own branch (the mock), and must NOT reach for
// native-login.ts — a stale import of a module this tree does not emit is a
// build failure in a generated project.
func TestAuthContextReadsIdentityFromTheServer(t *testing.T) {
	t.Parallel()

	const rel = "src/lib/auth/context.tsx"

	for _, kind := range append(append([]string{}, browserKinds...), "react-native") {
		tree := authTreeFor(t, kind)
		tmplPath, ok := tree[rel]
		if !ok {
			t.Fatalf("%s: no %s in the composed tree", kind, rel)
		}
		src := renderFrontend(t, tmplPath, kind)
		code := stripCommentsKeepingStrings(src)

		importsNativeLogin := strings.Contains(code, `from "./native-login"`)

		// The context value is the same on every platform: what a component
		// reads must not depend on which tree it was scaffolded into.
		for _, member := range []string{"identity", "isAuthenticated", "isLoading", "refresh", "logout"} {
			if !strings.Contains(code, member) {
				t.Errorf("%s %s does not expose %q — the context value is the app's whole auth surface, and it is the same on every platform so a component can be moved between them",
					kind, rel, member)
			}
		}

		// No token, anywhere, on any platform. The web cannot read the
		// HttpOnly cookie, and React Native ships a mock whose token is a
		// fixture no server accepts — so a getToken on this context would be
		// a null-returning method that callers build on.
		if m := regexp.MustCompile(`\bgetToken\b`).FindString(code); m != "" {
			t.Errorf("%s %s exposes getToken. The session cookie is HttpOnly, so there is no token to return — the only implementation is one that answers null, and every caller that trusts it is broken by construction.",
				kind, rel)
		}

		if kind == "react-native" {
			if importsNativeLogin {
				t.Errorf("react-native %s imports ./native-login, which is not emitted for this platform — the generated project will not build", rel)
			}
			if !strings.Contains(code, "createSessionAuthProvider") {
				t.Errorf("react-native %s must fall back to the mock provider — it is the only implementation this tree ships", rel)
			}
			continue
		}

		if !importsNativeLogin {
			t.Errorf("%s %s does not import ./native-login, so it has no way to ask the server who is signed in", kind, rel)
		}
		if !strings.Contains(code, "fetchIdentity") {
			t.Errorf("%s %s must read its identity from fetchIdentity() (GET /auth/session). The cookie is HttpOnly, so the server is the ONLY thing that can answer the question at all.",
				kind, rel)
		}
		// The fixture provider is reachable ONLY behind the mock-mode gate.
		// Ungated, a fixture identity renders a signed-in UI over a real
		// server that will 401 every RPC.
		if n := strings.Count(code, "createSessionAuthProvider()"); n != 1 || !mockGatedFixture.MatchString(code) {
			t.Errorf("%s %s must construct the fixture provider exactly once, gated on isMockMode() (`isMockMode() ? createSessionAuthProvider() : null`); found %d construction(s). Against a real backend the identity is the server's answer, never a fixture.",
				kind, rel, n)
		}
	}
}

// mockGatedFixture is the one sanctioned way the web auth context reaches
// the fixture session: constructed only when the bundle runs in mock mode.
var mockGatedFixture = regexp.MustCompile(`isMockMode\(\)\s*\?\s*createSessionAuthProvider\(\)\s*:\s*null`)

// TestMockModeSignsInTheFixtureUser pins the mock-mode half of the web auth
// context: with MOCK_API set ("true" or "hybrid") the identity is the
// fixture user and GET /auth/session is never sent.
//
// The regression it guards: the context used to ask the server in every
// mode. Pure mock has no server, so the request failed, the context read
// signed-out, and the route guard bounced every page to a sign-in form that
// posts to a server that is not there. Two agents worked around it by
// injecting window.__FORGE_CONFIG__ and stubbing /auth/session from devtools.
//
// Behavior is proven by the scaffolded Vitest suite this also requires
// (src/lib/auth/context.test.tsx), which runs in every browser frontend's
// `task test`. This test pins its PRESENCE plus the static shape it covers,
// so neither the guard nor the code can quietly drop out of the scaffold.
func TestMockModeSignsInTheFixtureUser(t *testing.T) {
	t.Parallel()

	for _, kind := range browserKinds {
		tree := authTreeFor(t, kind)

		ctxSrc := stripCommentsKeepingStrings(renderFrontend(t, tree["src/lib/auth/context.tsx"], kind))
		// The mock branch must come BEFORE the server round trip in refresh,
		// or GET /auth/session is still sent first.
		mockAt := strings.Index(ctxSrc, "if (mockSession)")
		fetchAt := strings.Index(ctxSrc, "fetchIdentity()")
		if mockAt < 0 || fetchAt < 0 || mockAt > fetchAt {
			t.Errorf("%s context.tsx must answer from the fixture session (`if (mockSession)`) before it calls fetchIdentity() — otherwise mock mode still sends GET /auth/session", kind)
		}

		spSrc := stripCommentsKeepingStrings(renderFrontend(t, tree["src/lib/auth/session-provider.ts"], kind))
		if !strings.Contains(spSrc, "export function isMockMode()") {
			t.Errorf("%s session-provider.ts must export isMockMode() — the context's only way to know it runs in mock mode", kind)
		}
		// Hybrid renders the fixture user but must never hand its token to a
		// server: only pure mock ("true") gives the token out.
		if !strings.Contains(spSrc, `isPureMock() ? MOCK_TOKEN : null`) {
			t.Errorf("%s session-provider.ts must hand out the fixture token in PURE mock only — in hybrid a forwarded request would carry a credential no server accepts", kind)
		}

		testTmpl, ok := tree["src/lib/auth/context.test.tsx"]
		if !ok {
			t.Fatalf("%s composed no src/lib/auth/context.test.tsx — the behavioral guard for mock-mode sign-in is missing", kind)
		}
		for _, hasTypedConfig := range []bool{true, false} {
			content, err := FrontendTemplates().Render(testTmpl, FrontendTemplateData{Platform: kind, HasTypedConfig: hasTypedConfig})
			if err != nil {
				t.Fatalf("render %s context.test.tsx (typed config %v): %v", kind, hasTypedConfig, err)
			}
			src := string(content)
			for _, mode := range []string{`"true"`, `"hybrid"`} {
				if !strings.Contains(src, mode) {
					t.Errorf("%s context.test.tsx does not exercise MOCK_API=%s", kind, mode)
				}
			}
			if hasTypedConfig && !strings.Contains(src, `vi.mock("@/lib/config_gen"`) {
				t.Errorf("%s context.test.tsx must set the mode through the typed config module the app reads", kind)
			}
		}
	}
}
