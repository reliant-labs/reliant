package siteaudit

import (
	"math"
	"net/http"
	"strings"
	"testing"
)

const goodPage = `<!doctype html><html lang="en"><head>
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Good page — a fine example</title>
<meta name="description" content="A good page with every tag a crawler and a link preview look for, and nothing else.">
<link rel="canonical" href="https://good.example/">
<meta property="og:title" content="Good"><meta property="og:description" content="Good page">
<meta property="og:type" content="website"><meta property="og:url" content="https://good.example/">
<meta property="og:image" content="https://good.example/og.png"><meta name="twitter:card" content="summary_large_image">
</head><body><main><a href="/about">About us</a><img src="/a.png" alt="A"></main></body></html>`

func codes(r Report) map[string]int {
	m := map[string]int{}
	for _, f := range r.Findings {
		m[f.Code]++
	}
	return m
}

func TestCatalogIsConsistent(t *testing.T) {
	seo := map[string]bool{}
	for _, a := range SEOAudits {
		seo[a.ID] = true
	}
	seen := map[string]bool{}
	for _, r := range Catalog() {
		if seen[r.Code] {
			t.Errorf("duplicate rule %s", r.Code)
		}
		seen[r.Code] = true
		if r.Audit != "" && !seo[r.Audit] {
			t.Errorf("rule %s names unknown SEO audit %q", r.Code, r.Audit)
		}
		if r.Fix == "" {
			t.Errorf("rule %s has no fix", r.Code)
		}
		switch r.Severity {
		case SeverityError, SeverityWarn, SeverityInfo:
		default:
			t.Errorf("rule %s has severity %q", r.Code, r.Severity)
		}
	}
	// Every scored SEO audit is reachable by some rule, or the table carries
	// weight nothing can fail.
	scored := map[string]bool{}
	for _, r := range catalog {
		scored[r.Audit] = true
	}
	for _, a := range SEOAudits {
		if !scored[a.ID] {
			t.Errorf("SEO audit %s has no rule that fails it", a.ID)
		}
	}
}

func TestLighthouseWeights(t *testing.T) {
	// is-crawlable alone failing fails the category (< 70), as in Lighthouse.
	var rest float64
	for _, a := range SEOAudits[1:] {
		rest += a.Weight
	}
	if share := SEOAudits[0].Weight / (SEOAudits[0].Weight + rest); math.Abs(share-0.31) > 0.01 {
		t.Errorf("is-crawlable is %.3f of SEO, want about 0.31", share)
	}
	ids := map[string]bool{}
	for _, a := range A11yAudits {
		if ids[a.ID] || a.Weight <= 0 || a.Group == "" {
			t.Errorf("bad a11y audit %+v", a)
		}
		ids[a.ID] = true
	}
}

func TestGoodPageScoresFull(t *testing.T) {
	rep := Evaluate(Input{Site: "https://good.example", Pages: []Page{{URL: "https://good.example/", Status: 200, RawHTML: goodPage}}})
	if len(rep.Findings) != 0 {
		t.Errorf("findings on a good page: %+v", rep.Findings)
	}
	if s := rep.Routes[0].Score.SEO; s == nil || *s != 100 {
		t.Errorf("SEO = %v, want 100", s)
	}
	if rep.Routes[0].Score.A11y != nil {
		t.Error("A11y must be nil without an axe result")
	}
	if rep.RulesVersion != RulesVersion || rep.Site.Indexing.Actual != "index" {
		t.Errorf("report: %+v", rep.Site)
	}
}

func TestSamePageFragmentsDoNotFailLinkText(t *testing.T) {
	for _, href := range []string{"#section", "/#section", "https://good.example/#section"} {
		for _, text := range []string{"", "click here"} {
			t.Run(href+"/"+text, func(t *testing.T) {
				html := strings.Replace(goodPage, `<a href="/about">About us</a>`, `<a href="`+href+`">`+text+`</a>`, 1)
				rep := Evaluate(Input{Site: "https://good.example", Pages: []Page{{URL: "https://good.example/", Status: 200, RawHTML: html}}})
				if c := codes(rep); c["link_text_empty"] != 0 || c["link_text_generic"] != 0 {
					t.Errorf("same-page fragment produced link-text findings: %v", c)
				}
				if s := rep.Routes[0].Score.SEO; s == nil || *s != 100 {
					t.Errorf("SEO = %v, want 100", s)
				}
			})
		}
	}
}

func TestEvaluateRawIgnoresFetchedFacts(t *testing.T) {
	in := Input{
		Site: "https://good.example",
		Pages: []Page{{URL: "https://good.example/", RawHTML: goodPage,
			Header: http.Header{"X-Robots-Tag": {"noindex"}}, Axe: &AxeResult{Violations: []AxeRule{{ID: "label", Impact: "critical"}}}}},
		Robots:      &Fetched{Status: 503},
		LinkResults: []LinkResult{{URL: "https://good.example/about", Status: 404}},
		NotFound:    &Page{Status: 200},
	}
	raw := EvaluateRaw(in)
	if len(raw.Findings) != 0 {
		t.Errorf("EvaluateRaw judged fetched facts: %+v", raw.Findings)
	}
	in.Pages[0].Status = 200
	got := codes(Evaluate(in))
	for _, c := range []string{"indexing_mismatch", "robots_unreachable", "link_broken", "a11y_violation", "soft_404"} {
		if got[c] != 1 {
			t.Errorf("Evaluate: want one %s, got %v", c, got)
		}
	}
	for _, r := range Catalog() {
		if r.Source == SourceSite || r.Source == SourceAxe {
			if codes(raw)[r.Code] > 0 {
				t.Errorf("EvaluateRaw emitted site rule %s", r.Code)
			}
		}
	}
}

func TestRawOnlySocialRules(t *testing.T) {
	// The rendered DOM has every OG tag; the served HTML has none. Link
	// previews read the served HTML, so the tags are missing (D1).
	rendered := goodPage
	raw := `<html lang="en"><head><title>Good page — a fine example</title></head><body></body></html>`
	rep := Evaluate(Input{Site: "https://good.example", Pages: []Page{{URL: "https://good.example/", Status: 200, RawHTML: raw, RenderedHTML: rendered}}})
	c := codes(rep)
	if c["og_tag_missing"] != 5 || c["twitter_card_missing"] != 1 {
		t.Errorf("social rules must judge raw HTML: %v", c)
	}
	if c["head_client_rendered"] != 2 { // description and canonical
		t.Errorf("head_client_rendered: %v", c)
	}
	if c["title_missing"] != 0 || c["description_missing"] != 0 {
		t.Errorf("title and description judge the rendered DOM: %v", c)
	}
}

func TestExpectNoindexIsNotPenalised(t *testing.T) {
	page := Page{URL: "https://p.example/", Status: 200, RawHTML: goodPage, Header: http.Header{"X-Robots-Tag": {"noindex"}}}
	rep := Evaluate(Input{Site: "https://p.example", Pages: []Page{page}, Expect: Expect{Indexing: IndexingNoindex, CanonicalHost: "good.example"}})
	if c := codes(rep); c["indexing_mismatch"] != 0 || c["preview_indexable"] != 0 {
		t.Errorf("noindex by design: %v", c)
	}
	if s := rep.Routes[0].Score.SEO; s == nil || *s != 100 {
		t.Errorf("SEO = %v, want 100 for a noindex preview", s)
	}
	rep = Evaluate(Input{Site: "https://good.example", Pages: []Page{page}})
	if c := codes(rep); c["indexing_mismatch"] != 1 {
		t.Errorf("prod noindex must be an error: %v", c)
	}
	if s := rep.Routes[0].Score.SEO; s == nil || *s >= 70 {
		t.Errorf("SEO = %v, want a failing score when prod is noindex", s)
	}
}

func TestXRobotsScope(t *testing.T) {
	cases := map[string]bool{
		"noindex":                   true,
		"none":                      true,
		"googlebot: noindex":        true,
		"otherbot: noindex":         false,
		"nofollow":                  false,
		"noindex, nofollow":         true,
		"unavailable_after: 25 Jun": false,
	}
	for v, want := range cases {
		if got := xRobotsNoindex(http.Header{"X-Robots-Tag": {v}}); got != want {
			t.Errorf("X-Robots-Tag %q: noindex = %v, want %v", v, got, want)
		}
	}
}

func TestFindingKeysAreUnique(t *testing.T) {
	// Code, Route and Ref identify a finding across audits (release health
	// diffs them), so one report never repeats a key.
	for _, dir := range []string{"export-site", "spa-shell", "broken-sitemap", "hreflang-pairs", "jsonld"} {
		in, _ := loadCase(t, "testdata/"+dir)
		seen := map[string]bool{}
		for _, f := range Evaluate(in).Findings {
			k := strings.Join([]string{f.Code, f.Route, f.Ref}, "|")
			if seen[k] {
				t.Errorf("%s: duplicate finding key %s", dir, k)
			}
			seen[k] = true
		}
	}
}

// The report carries the release the caller named, and none when it named
// none.
func TestReportCarriesItsRelease(t *testing.T) {
	in := Input{Site: "https://example.com", Pages: []Page{{URL: "https://example.com/", Status: 200, RawHTML: "<html><head><title>Home page here</title></head></html>"}}}
	if r := Evaluate(in); r.Release != "" {
		t.Errorf("release = %q, want none", r.Release)
	}
	in.Release = "v1.8.2"
	if r := Evaluate(in); r.Release != "v1.8.2" {
		t.Errorf("release = %q", r.Release)
	}
}
