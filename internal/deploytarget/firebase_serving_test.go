package deploytarget

import (
	"encoding/json"
	"math/rand"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// navigationReference is the rule spaNavigationRegex encodes, written the
// obvious way: inside scope, and a last segment that is dot-free or ends in
// an extension that is not a static file's.
func navigationReference(p, scope string) bool {
	if scope != "" {
		if p != scope && !strings.HasPrefix(p, scope+"/") {
			return false
		}
		p = strings.TrimPrefix(p, scope)
		if p == "" {
			return true
		}
	}
	if !strings.HasPrefix(p, "/") {
		return false
	}
	last := p[strings.LastIndex(p, "/")+1:]
	dot := strings.LastIndex(last, ".")
	if dot < 0 {
		return true
	}
	return !slices.Contains(spaStaticExtensions, strings.ToLower(last[dot+1:]))
}

// The incident's paths, by name: a client-side route gets the entry
// document and a missing chunk does not.
func TestSPANavigationRegex_IncidentPaths(t *testing.T) {
	re := regexp.MustCompile(spaNavigationRegex("/index.html"))
	routes := []string{
		"/",
		"/inbox",
		"/project/d70701a6-6f0e-4bd7-9a43-1d1c1c1c1c1c",
		"/project/d70701a6/",
		"/m/chats/abc/workflow",
		// A route param that names a file is still a route: only static
		// extensions are refused, not every dot.
		"/workflow/flows%2Fdeploy.yaml",
		"/workflow/.reliant/workflows/deploy.yaml",
		"/v1.2/release-notes",
		"/x.",
	}
	for _, p := range routes {
		if !re.MatchString(p) {
			t.Errorf("route %q is not served the entry document", p)
		}
	}
	files := []string{
		"/assets/InboxPage-CKsZPmVl.js",
		"/assets/index-CKsZPmVl.js",
		"/assets/does-not-exist-abc123.js",
		"/assets/index-abc.css",
		"/assets/index-abc.js.map",
		"/assets/Inter-Var.woff2",
		"/assets/sql-wasm.wasm",
		"/favicon.ico",
		"/version.json",
		"/forge-assets.json",
		"/robots.txt",
		"/old-page.html",
		"/ASSETS/INDEX-ABC.JS",
		"/deep/nested/logo.PNG",
	}
	for _, p := range files {
		if re.MatchString(p) {
			t.Errorf("missing file %q would be answered with the entry document (HTML, 200) instead of a 404", p)
		}
	}
}

// A fallback document under a mount answers only inside that mount.
func TestSPANavigationRegex_ScopedToTheDocumentsDirectory(t *testing.T) {
	re := regexp.MustCompile(spaNavigationRegex("/admin/index.html"))
	for _, p := range []string{"/admin", "/admin/", "/admin/users/42"} {
		if !re.MatchString(p) {
			t.Errorf("route %q inside /admin is not served /admin/index.html", p)
		}
	}
	for _, p := range []string{"/", "/inbox", "/adminx", "/other/admin", "/admin/assets/app-abc.js"} {
		if re.MatchString(p) {
			t.Errorf("%q matched /admin's fallback", p)
		}
	}
}

// The generated regex equals the reference rule on every path: every
// extension, every proper prefix of one, every one-character extension of
// one, in both cases — then a random corpus over the alphabet the trie
// branches on. Go's regexp is RE2, the dialect Firebase evaluates.
func TestSPANavigationRegex_EqualsTheReferenceRule(t *testing.T) {
	for _, dest := range []string{"/index.html", "/admin/index.html", "/docs/v2/index.html"} {
		scope := strings.TrimSuffix(dest, "/index.html")
		re := regexp.MustCompile(spaNavigationRegex(dest))
		check := func(p string) {
			t.Helper()
			if got, want := re.MatchString(p), navigationReference(p, scope); got != want {
				t.Fatalf("dest %s, path %q: regex=%v reference=%v\nregex: %s", dest, p, got, want, re)
			}
		}

		var exts []string
		for _, e := range spaStaticExtensions {
			for i := 0; i <= len(e); i++ {
				exts = append(exts, e[:i], e[:i]+"x", e[:i]+"2", strings.ToUpper(e[:i]))
			}
			exts = append(exts, e+"s", e+".js", "x"+e)
		}
		for _, e := range exts {
			for _, dir := range []string{"", "/assets", "/a.b", "/assets/x.js"} {
				check(scope + dir + "/name." + e)
				check(scope + dir + "/." + e)
				check(scope + dir + "/" + e)
			}
		}

		rng := rand.New(rand.NewSource(1))
		const alphabet = "/./.jJsScmMaApPwWoOfF2tTxXhHlLnNiIgGeEvVbBdD-_%"
		for range 20000 {
			n := 1 + rng.Intn(14)
			var b strings.Builder
			b.WriteString(scope)
			b.WriteByte('/')
			for range n {
				b.WriteByte(alphabet[rng.Intn(len(alphabet))])
			}
			check(b.String())
		}
	}
}

func TestSPANavigationRegex_IsBounded(t *testing.T) {
	// Firebase stores the regex in the release config; keep it a sane
	// size so a growing extension list is a conscious decision.
	if n := len(spaNavigationRegex("/index.html")); n > 4096 {
		t.Fatalf("navigation regex is %d bytes", n)
	}
}

func decodeFirebaseJSON(t *testing.T, spec FirebaseHostingSpec) map[string]any {
	t.Helper()
	out, err := renderFirebaseJSON("/tmp/stage/public", spec)
	if err != nil {
		t.Fatalf("renderFirebaseJSON: %v", err)
	}
	var doc struct {
		Hosting map[string]any `json:"hosting"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("firebase.json is not JSON: %v\n%s", err, out)
	}
	return doc.Hosting
}

type headerRule struct {
	Source  string `json:"source"`
	Headers []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"headers"`
}

func headerRules(t *testing.T, hosting map[string]any) []headerRule {
	t.Helper()
	b, _ := json.Marshal(hosting["headers"])
	var rules []headerRule
	if err := json.Unmarshal(b, &rules); err != nil {
		t.Fatalf("headers: %v", err)
	}
	return rules
}

// Declared rewrites come first (first match wins, so an explicit rewrite
// beats the fallback), then the generated SPA fallback.
func TestRenderFirebaseJSON_SPAFallbackFollowsDeclaredRewrites(t *testing.T) {
	hosting := decodeFirebaseJSON(t, FirebaseHostingSpec{
		Project: "p", Site: "s",
		Rewrites:    []map[string]any{{"source": "/api/**", "function": "api"}},
		SPAFallback: "/index.html",
		AssetDir:    "assets",
	})
	rewrites, _ := hosting["rewrites"].([]any)
	if len(rewrites) != 2 {
		t.Fatalf("rewrites = %v, want the declared one then the fallback", rewrites)
	}
	if first := rewrites[0].(map[string]any); first["source"] != "/api/**" {
		t.Errorf("first rewrite = %v, want the declared /api/** rewrite", first)
	}
	fallback := rewrites[1].(map[string]any)
	if fallback["destination"] != "/index.html" || fallback["regex"] != spaNavigationRegex("/index.html") {
		t.Errorf("fallback rewrite = %v", fallback)
	}
	if _, glob := fallback["source"]; glob {
		t.Errorf("the fallback must be a regex, not a glob source: %v", fallback)
	}
}

func TestRenderFirebaseJSON_NoFallbackDeclaredRendersNone(t *testing.T) {
	hosting := decodeFirebaseJSON(t, FirebaseHostingSpec{Project: "p", Site: "s"})
	if _, ok := hosting["rewrites"]; ok {
		t.Errorf("rewrites rendered with none declared: %v", hosting["rewrites"])
	}
}

// The default Cache-Control policy: everything revalidates, the hashed
// asset directory is immutable. Firebase keeps the LAST matching rule, so
// the asset rule must come after the catch-all.
func TestRenderFirebaseJSON_DefaultCachePolicy(t *testing.T) {
	for _, tc := range []struct {
		base, assetDir, wantAssets string
	}{
		{"", "assets", "/assets/**"},
		{"/admin", "assets", "/admin/assets/**"},
		{"", "_next/static", "/_next/static/**"},
	} {
		rules := headerRules(t, decodeFirebaseJSON(t, FirebaseHostingSpec{
			Project: "p", Site: "s", BasePath: tc.base, AssetDir: tc.assetDir,
		}))
		if len(rules) != 2 {
			t.Fatalf("%+v: headers = %+v", tc, rules)
		}
		if rules[0].Source != "**" || rules[0].Headers[0].Value != "no-cache" {
			t.Errorf("%+v: first rule = %+v, want ** no-cache", tc, rules[0])
		}
		if rules[1].Source != tc.wantAssets || rules[1].Headers[0].Key != "Cache-Control" ||
			rules[1].Headers[0].Value != "public, max-age=31536000, immutable" {
			t.Errorf("%+v: last rule = %+v, want %s immutable", tc, rules[1], tc.wantAssets)
		}
	}

	// No asset dir: nothing is known to be immutable, so nothing is.
	rules := headerRules(t, decodeFirebaseJSON(t, FirebaseHostingSpec{Project: "p", Site: "s"}))
	if len(rules) != 1 || rules[0].Source != "**" || rules[0].Headers[0].Value != "no-cache" {
		t.Errorf("no asset dir: headers = %+v, want only ** no-cache", rules)
	}
}

// Declared cache_control rules replace the default and keep their
// first-match-wins meaning under Firebase's last-match-wins.
func TestRenderFirebaseJSON_DeclaredCacheRulesReplaceTheDefault(t *testing.T) {
	rules := headerRules(t, decodeFirebaseJSON(t, FirebaseHostingSpec{
		Project: "p", Site: "s", AssetDir: "assets",
		CacheControl: []CacheRuleSpec{
			{Pattern: "assets/**", CacheControl: "public, max-age=600"},
			{Pattern: "**", CacheControl: "public, max-age=0, must-revalidate"},
		},
	}))
	if len(rules) != 2 || rules[0].Source != "/**" || rules[1].Source != "/assets/**" ||
		rules[1].Headers[0].Value != "public, max-age=600" {
		t.Errorf("headers = %+v, want the declared rules reversed (/** then /assets/**)", rules)
	}
}
