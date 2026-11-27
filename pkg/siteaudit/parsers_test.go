package siteaudit

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRobotsAllowed(t *testing.T) {
	const body = `
User-agent: *
Disallow: /private/
Allow: /private/public
Disallow: /*.pdf$
Disallow: /search?

User-agent: Googlebot
User-agent: Bingbot
Disallow: /no-google/
Allow: /no-google/except

User-agent: AdsBot-Google
Disallow: /
`
	r := ParseRobots(body)
	if len(r.Problems) != 0 {
		t.Fatalf("problems: %+v", r.Problems)
	}
	cases := []struct {
		agent, path string
		want        bool
	}{
		{"SomeBot/1.0", "/", true},
		{"SomeBot/1.0", "/private/x", false},
		{"SomeBot/1.0", "/private/public/page", true}, // longer Allow wins
		{"SomeBot/1.0", "/files/a.pdf", false},
		{"SomeBot/1.0", "/files/a.pdf?x=1", true}, // $ anchors the end
		{"SomeBot/1.0", "/search?q=a", false},
		{"SomeBot/1.0", "/robots.txt", true},
		// Googlebot has its own group, so the * group no longer applies.
		{"Googlebot", "/private/x", true},
		{"Mozilla/5.0 (compatible; Googlebot/2.1)", "/no-google/x", false},
		{"Googlebot", "/no-google/except", true},
		{"bingbot/2.0", "/no-google/", false},
		// The most specific token wins: AdsBot-Google, not Googlebot.
		{"AdsBot-Google", "/anything", false},
	}
	for _, c := range cases {
		if got := r.Allowed(c.agent, c.path); got != c.want {
			t.Errorf("Allowed(%q, %q) = %v, want %v", c.agent, c.path, got, c.want)
		}
	}
}

func TestRobotsTieGoesToAllow(t *testing.T) {
	r := ParseRobots("User-agent: *\nDisallow: /page\nAllow: /page\n")
	if !r.Allowed("x", "/page") {
		t.Error("an Allow and a Disallow of the same length: Allow wins")
	}
}

func TestRobotsEmptyDisallowAllowsAll(t *testing.T) {
	r := ParseRobots("User-agent: *\nDisallow:\n")
	if !r.Allowed("x", "/") || len(r.Problems) != 0 {
		t.Errorf("empty Disallow must allow everything, got problems %+v", r.Problems)
	}
}

func TestRobotsProblemsAndSitemaps(t *testing.T) {
	r := ParseRobots("\uFEFFAllow: /early\nUser-agent: *\nDisallow: nope\nFoo: bar\njunk\nSitemap: https://a.example/s.xml\nsitemap: s.xml\r\n")
	var msgs []string
	for _, p := range r.Problems {
		msgs = append(msgs, itoa(p.Line)+":"+p.Message)
	}
	want := []string{
		`1:allow before any user-agent: crawlers ignore it`,
		`3:pattern must start with "/" or "*"`,
		`4:unknown directive "foo"`,
		`5:not a directive: expected "<field>: <value>"`,
		`7:sitemap must be an absolute http(s) URL`,
	}
	if !reflect.DeepEqual(msgs, want) {
		t.Errorf("problems:\n got %q\nwant %q", msgs, want)
	}
	if !reflect.DeepEqual(r.Sitemaps, []string{"https://a.example/s.xml"}) {
		t.Errorf("sitemaps = %q", r.Sitemaps)
	}
}

func TestRobotsMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/", "/x", true},
		{"/a", "/abc", true},
		{"/a$", "/abc", false},
		{"/a$", "/a", true},
		{"*", "/x", true},
		{"/*/b", "/a/b/c", true},
		{"/*/b$", "/a/b/c", false},
		{"/a*b*c$", "/a-b-c", true},
		{"/a*b*c$", "/a-b-cd", false},
		{"*.php$", "/x/index.php", true},
		{"/x", "/y", false},
	}
	for _, c := range cases {
		if got := robotsMatch(c.pattern, c.path); got != c.want {
			t.Errorf("robotsMatch(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestParseSitemap(t *testing.T) {
	s, err := ParseSitemap(`<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc> https://a.example/ </loc></url><url><loc>https://a.example/b</loc></url></urlset>`)
	if err != nil || s.Index || !reflect.DeepEqual(s.Locs, []string{"https://a.example/", "https://a.example/b"}) {
		t.Fatalf("urlset: %+v, %v", s, err)
	}
	s, err = ParseSitemap(`<sitemapindex><sitemap><loc>https://a.example/s1.xml</loc></sitemap></sitemapindex>`)
	if err != nil || !s.Index || len(s.Locs) != 1 {
		t.Fatalf("index: %+v, %v", s, err)
	}
	for _, bad := range []string{"", "not xml", "<rss/>", "<urlset><url></urlset>", "<urlset>", "<sitemapindex>", "<urlset><url><loc>https://a.example/</loc></url>"} {
		if _, err := ParseSitemap(bad); err == nil {
			t.Errorf("ParseSitemap(%q): want an error", bad)
		}
	}
}

func TestExtractHead(t *testing.T) {
	h := ExtractHead(`<!doctype html><html lang="pt-BR"><head>
<title>  One
  title </title><title>Two</title>
<meta name="Description" content="  A   description. ">
<meta name="robots" content="noindex"><meta name="googlebot" content="nofollow">
<meta name="viewport" content="width=device-width">
<link rel="canonical" href="https://a.example/x">
<meta property="og:title" content="OG"><meta name="twitter:card" content="summary">
<link rel="alternate" hreflang="en" href="https://a.example/en">
<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"WebSite"},{"@type":["Organization","Brand"]}]}</script>
</head><body><title>Not in head</title><meta name="description" content="body meta"></body></html>`)
	want := Head{
		Title: "One title", Description: "A description.", Canonical: "https://a.example/x",
		Lang: "pt-BR", Robots: "noindex, nofollow", Viewport: "width=device-width",
		Social:      map[string]string{"og:title": "OG", "twitter:card": "summary"},
		Hreflang:    []Alternate{{Hreflang: "en", Href: "https://a.example/en"}},
		JSONLDTypes: []string{"Brand", "Organization", "WebSite"},
	}
	if !reflect.DeepEqual(h, want) {
		t.Errorf("ExtractHead:\n got %+v\nwant %+v", h, want)
	}
}

func TestDiscoverLinks(t *testing.T) {
	got := DiscoverLinks("https://a.example/docs/page", `<base href="/root/">
<a href="x">x</a><a href="x#frag">dup</a><a href="https://b.example/">b</a>
<a href="mailto:a@b">m</a><a href="javascript:void(0)">j</a><a>no href</a><a href="//c.example/y">c</a>`)
	want := []string{"https://a.example/root/x", "https://b.example/", "https://c.example/y"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiscoverLinks = %q, want %q", got, want)
	}
}

func TestAxeTargetAndSnippets(t *testing.T) {
	var res AxeResult
	long := strings.Repeat("x", 400)
	err := json.Unmarshal([]byte(`{"violations":[{"id":"label","impact":"critical","tags":["wcag2a","cat.forms"],"help":"Form elements must have labels",
"nodes":[{"target":[["#host","input"]],"html":"`+long+`"},{"target":"#plain"}]}]}`), &res)
	if err != nil {
		t.Fatal(err)
	}
	fs := axeFindings("/", &res)
	if len(fs) != 1 {
		t.Fatalf("findings: %+v", fs)
	}
	f := fs[0]
	if f.Severity != SeverityError || f.Category != CategoryAccessibility || f.Ref != "label" || f.Evidence["group"] != string(GroupA11yNamesLabels) || f.Evidence["wcag"] != "wcag2a" {
		t.Errorf("finding: %+v", f)
	}
	if f.Nodes[0].Target != "#host >>> input" || f.Nodes[1].Target != "#plain" {
		t.Errorf("targets: %+v", f.Nodes)
	}
	if n := len([]rune(f.Nodes[0].HTML)); n != SnippetMaxChars {
		t.Errorf("snippet is %d characters, want %d", n, SnippetMaxChars)
	}
}

func TestIsPlaceholderImage(t *testing.T) {
	if !IsPlaceholderImage([]byte("\x89PNG....tEXt" + PlaceholderImageMarker + "....")) {
		t.Error("marker not found")
	}
	if IsPlaceholderImage([]byte("\x89PNG an ordinary image")) {
		t.Error("an ordinary image read as the placeholder")
	}
}
