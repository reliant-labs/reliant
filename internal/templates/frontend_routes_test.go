// frontend_routes_test.go — a scaffolded component may not point at a route
// forge never emits.
//
// Born red on the auth UI. `src/components/auth/` used to arrive alongside a
// server half that emitted the routes it calls — `/auth/login`,
// `/auth/signup`, `/auth/logout` and the OAuth `/auth/oauth/exchange`. A
// refactor promoted the frontend files to unconditional scaffold and did not
// carry the handlers with them, so every Next.js scaffold was born with a
// login form that 404s, a signup form that 404s, a sign-out that 404s, and a
// callback page that 404s, plus a "Sign in" link to `/login`, a page forge
// does not emit either.
//
// The failure is expensive precisely because it LOOKS finished: a measured
// dogfood run spent 14m26s writing an adapter and hand-minting a JWT to get
// past a form that could never have worked. The pack-era commit that first
// added the login handler said the same thing in its own words — "no handler
// shipped to satisfy it, so every project hand-wrote a stub."
//
// The rule this guard enforces: every same-origin URL a scaffolded template
// fetches or navigates to must be a route forge actually emits. Forge emits
// exactly two shapes — `/` (src/app/page.tsx) and the templated per-entity
// routes the page emitters render. Everything reaching the server otherwise
// goes through the Connect transport, which is why there is no third shape.
package templates

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Route-shaped references. Each pattern captures a URL path in a position
// where the app is genuinely asking the server (or the router) for it, so a
// path named only in prose — `joinBasePath("/billing/success")` in a doc
// comment — is not mistaken for a route the scaffold depends on. Comments are
// stripped before matching regardless; the narrow contexts are the second
// belt.
var routeReferencePatterns = []struct {
	what string
	re   *regexp.Regexp
}{
	// fetch("/auth/login", …) — a literal same-origin request target.
	{"fetch", regexp.MustCompile(`fetch\(\s*"(/[^"]*)"`)},
	// <Link href="/login"> / <a href="/login">
	{"href", regexp.MustCompile(`href=\{?\s*"(/[^"]*)"`)},
	// router.push("/login") / router.replace("/login")
	{"router", regexp.MustCompile(`\.(?:push|replace)\(\s*"(/[^"]*)"`)},
	// TanStack Router: navigate({ to: "/login" })
	{"navigate", regexp.MustCompile(`\bto:\s*"(/[^"]*)"`)},
	// Default props and consts that name an endpoint: loginPath = "/auth/login",
	// signInUrl = "/login", exchangePath = "/auth/oauth/exchange".
	{"endpoint default", regexp.MustCompile(`\b\w*(?:Path|Url|Href|Endpoint)\s*=\s*"(/[^"]*)"`)},
}

// fetchIdentifier matches `fetch(loginPath, …)` — the indirect form, where
// the target is a prop whose default lives elsewhere in the file. Without
// this the guard reads LoginForm's `fetch(loginPath, …)` as targetless.
var fetchIdentifier = regexp.MustCompile(`fetch\(\s*([A-Za-z_$][\w$]*)\s*[,)]`)

// identifierDefault resolves that identifier back to its string default.
func identifierDefault(src, ident string) (string, bool) {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(ident) + `\s*=\s*"([^"]*)"`)
	if m := re.FindStringSubmatch(src); m != nil {
		return m[1], true
	}
	return "", false
}

// stripComments removes // line comments and /* */ blocks so prose examples
// cannot register as route references. It is deliberately naive about string
// literals containing "//" — the only ones in the tree are absolute URLs
// (https://…), and dropping the tail of such a line loses nothing this guard
// wants, since an absolute URL is not a same-origin route.
func stripComments(src string) string {
	var out strings.Builder
	for {
		block := strings.Index(src, "/*")
		if block < 0 {
			break
		}
		end := strings.Index(src[block+2:], "*/")
		if end < 0 {
			src = src[:block]
			break
		}
		out.WriteString(src[:block])
		src = src[block+2+end+2:]
	}
	out.WriteString(src)

	var lines []string
	for _, line := range strings.Split(out.String(), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// emittedRoute reports whether path is a route a forge scaffold serves.
//
//   - "/" is src/app/page.tsx (Next.js) / the index route (Vite).
//   - /auth/sign-in, /auth/forgot and /auth/reset are the native sign-in
//     screens: src/app/auth/*/page.tsx (Next.js) and signInRoute /
//     forgotRoute / resetRoute in routes.tsx (Vite). A public frontend omits
//     them and also omits every reference to them.
//   - Anything whose first segment is a template action is a per-entity route
//     the page emitters render: /{{.EntitySlug}}, /{{.EntitySlug}}/new, and
//     the [id] / [id]/edit pages beneath them.
func emittedRoute(path string) bool {
	if path == "/" {
		return true
	}
	switch path {
	case "/auth/sign-in", "/auth/forgot", "/auth/reset":
		return true
	}
	if strings.HasPrefix(path, "/{{") {
		return true
	}
	return strings.HasPrefix(path, "/oidc/") || strings.HasPrefix(path, "/api/")
}

// TestFrontendTemplates_NoRouteForgeDoesNotEmit walks every root a frontend
// can be composed from, plus the page emitters, and fails on any same-origin
// path the scaffold reaches for that forge does not serve.
func TestFrontendTemplates_NoRouteForgeDoesNotEmit(t *testing.T) {
	t.Parallel()

	roots := []string{
		"shared", "shared-web", "nextjs", "vite-spa", "react-native",
		"pages", "vite-spa-pages", "mocks",
	}

	checked := 0
	for _, root := range roots {
		files, err := listTemplates(filepath.Join("frontend", root), true)
		if err != nil {
			t.Fatalf("list %s: %v", root, err)
		}
		for _, rel := range files {
			ext := filepath.Ext(strings.TrimSuffix(rel, ".tmpl"))
			if ext != ".ts" && ext != ".tsx" {
				continue
			}
			raw, err := FrontendTemplates().Get(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("read %s/%s: %v", root, rel, err)
			}
			checked++
			src := stripComments(string(raw))

			report := func(what, path string) {
				if emittedRoute(path) {
					return
				}
				t.Errorf("%s/%s: %s target %q is a route forge never emits.\n"+
					"    A scaffolded component that points at a missing route ships broken and looks finished.\n"+
					"    Either emit the route, or reach the server through the Connect transport (@/lib/connect),\n"+
					"    or drive the app's own seam (useAuth() from @/lib/auth/context) instead of a URL.",
					root, rel, what, path)
			}

			for _, p := range routeReferencePatterns {
				for _, m := range p.re.FindAllStringSubmatch(src, -1) {
					report(p.what, m[1])
				}
			}
			for _, m := range fetchIdentifier.FindAllStringSubmatch(src, -1) {
				if lit, ok := identifierDefault(src, m[1]); ok && strings.HasPrefix(lit, "/") {
					report("fetch", lit)
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("walked zero TypeScript templates — the tree layout changed and this guard is now blind")
	}
}

// credentialStorage matches a browser/native persistent-storage call.
var credentialStorage = regexp.MustCompile(`\b(?:localStorage|sessionStorage|AsyncStorage)\b`)

// TestFrontendTemplates_SingleCredentialSurface pins the other half of the
// same defect: the scaffold must hold a credential in exactly ONE place.
//
// Born red alongside the route guard. `components/auth/auth-store.ts` cached
// `{token, user}` in localStorage while `lib/auth/` held the AuthProvider the
// app is actually wired through — and `providers.tsx` feeds the Connect
// transport from `useAuth().getToken`, not from the store. Two credential
// caches with no join between them is how a scaffold ends up authenticating
// its UI and not its requests.
//
// The rule: only `src/lib/auth/` — the AuthProvider seam — persists a token.
func TestFrontendTemplates_SingleCredentialSurface(t *testing.T) {
	t.Parallel()

	roots := []string{
		"shared", "shared-web", "nextjs", "vite-spa", "react-native", "mocks",
	}
	for _, root := range roots {
		files, err := listTemplates(filepath.Join("frontend", root), true)
		if err != nil {
			t.Fatalf("list %s: %v", root, err)
		}
		for _, rel := range files {
			ext := filepath.Ext(strings.TrimSuffix(rel, ".tmpl"))
			if ext != ".ts" && ext != ".tsx" {
				continue
			}
			if strings.HasPrefix(filepath.ToSlash(rel), "src/lib/auth/") {
				continue // the seam itself — the one place a credential lives
			}
			raw, err := FrontendTemplates().Get(filepath.Join(root, rel))
			if err != nil {
				t.Fatalf("read %s/%s: %v", root, rel, err)
			}
			src := stripComments(string(raw))
			if !credentialStorage.MatchString(src) {
				continue
			}
			if !strings.Contains(strings.ToLower(src), "token") {
				continue // persists something else (UI preferences, drafts)
			}
			t.Errorf("%s/%s: persists a token outside src/lib/auth/.\n"+
				"    The AuthProvider seam is the scaffold's only credential surface — the Connect\n"+
				"    transport is fed from useAuth().getToken, so a second cache authenticates the UI\n"+
				"    and not the requests. Implement AuthProvider instead.", root, rel)
		}
	}
}
