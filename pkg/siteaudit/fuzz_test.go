package siteaudit

import (
	"os"
	"path/filepath"
	"testing"
)

// seedFiles adds every testdata file matching pattern to the corpus.
func seedFiles(f *testing.F, pattern string) {
	f.Helper()
	files, _ := filepath.Glob(pattern)
	for _, p := range files {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(string(b))
		}
	}
}

// FuzzHTML: no document makes the HTML reader or the page rules panic, and a
// raw evaluation of one page always reports that page.
func FuzzHTML(f *testing.F) {
	seedFiles(f, "testdata/*/*.html")
	f.Add(`<a href="%zz">x</a><link rel=canonical href="::"><link rel=alternate hreflang href>`)
	f.Add(`<script type="application/ld+json">[{"@type":[1,{}],"itemListElement":"x","mainEntity":[null]}]</script>`)
	f.Fuzz(func(t *testing.T, src string) {
		_ = ExtractHead(src)
		_ = DiscoverLinks("https://f.example/a/", src)
		rep := EvaluateRaw(Input{Site: "https://f.example", Pages: []Page{{URL: "https://f.example/", RawHTML: src}}})
		if len(rep.Routes) != 1 {
			t.Fatalf("routes = %d, want 1", len(rep.Routes))
		}
		rep = Evaluate(Input{Site: "https://f.example", Pages: []Page{
			{URL: "https://f.example/", Status: 200, RawHTML: src, RenderedHTML: src},
			{URL: "https://f.example/b", Status: 200, RawHTML: src},
		}})
		for _, r := range rep.Routes {
			if s := r.Score.SEO; s != nil && (*s < 0 || *s > 100) {
				t.Fatalf("SEO score %d out of range", *s)
			}
		}
	})
}

// FuzzRobots: no robots.txt makes the parser or the matcher panic, and
// /robots.txt is always allowed.
func FuzzRobots(f *testing.F) {
	seedFiles(f, "testdata/*/robots.txt")
	f.Add("User-agent: *\nDisallow: /*a*b$\nAllow: $\n")
	f.Add("user-agent:\ndisallow:*\n\x00:\n")
	f.Fuzz(func(t *testing.T, body string) {
		r := ParseRobots(body)
		for _, p := range []string{"/", "/a/b", "/x$y", "", "/robots.txt?x"} {
			_ = r.Allowed("Googlebot", p)
		}
		if !r.Allowed("Googlebot", "/robots.txt") {
			t.Fatal("/robots.txt must always be allowed")
		}
	})
}

// FuzzSitemap: no body makes the sitemap parser panic.
func FuzzSitemap(f *testing.F) {
	seedFiles(f, "testdata/*/*.xml")
	f.Fuzz(func(t *testing.T, body string) {
		s, err := ParseSitemap(body)
		if err == nil && s == nil {
			t.Fatal("nil sitemap without an error")
		}
	})
}
