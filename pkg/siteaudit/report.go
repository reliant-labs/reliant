package siteaudit

import (
	"net/url"
	"sort"
	"strings"
)

// judgeNotFound reads how the site answers a path that cannot exist, and
// returns SiteSummary.NotFound.
func (a *auditor) judgeNotFound() string {
	nf := a.in.NotFound
	if nf == nil {
		return ""
	}
	switch {
	case nf.Status >= 400 && nf.Status < 500:
		return "not_found"
	case nf.Status >= 200 && nf.Status < 300:
	default:
		return "error"
	}
	// An unknown path that serves the home page is an SPA's index.html
	// fallback, a choice (design D3, O7): info. Anything else is a page that
	// says "not found" with a 200: warn.
	sev, mode := SeverityWarn, "error page served with 200"
	for _, ps := range a.pages {
		if ps.ok && ps.route == a.rootPath && strings.TrimSpace(ps.page.RawHTML) == strings.TrimSpace(nf.RawHTML) {
			sev, mode = SeverityInfo, "SPA fallback: unknown paths serve index.html with 200"
			break
		}
	}
	route := "/"
	if u, err := url.Parse(nf.URL); err == nil {
		route = routeOf(u)
	}
	a.emitSev("soft_404", "", route, sev, ev("status", itoa(nf.Status), "mode", mode))
	return "soft_404"
}

// score computes each route's scores from its facts and findings.
func (a *auditor) score() []RouteReport {
	failed := map[string]map[string]bool{} // route → failed audit ids; "" is site-wide
	for _, f := range a.findings {
		r, ok := ruleByCode[f.Code]
		if !ok || r.Audit == "" {
			continue
		}
		if failed[f.Route] == nil {
			failed[f.Route] = map[string]bool{}
		}
		failed[f.Route][r.Audit] = true
	}
	robotsApplies := a.robotsSumm.Status == "ok" || a.robotsSumm.Status == "unreachable"
	var out []RouteReport
	for _, ps := range a.pages {
		rr := RouteReport{Route: ps.route, URL: ps.page.URL, Status: ps.page.Status,
			Rendered: ps.ok && ps.page.RenderedHTML != "", Head: ps.head}
		if ps.page.Status != 0 {
			applies := map[string]bool{
				"is-crawlable":      true,
				"http-status-code":  true,
				"document-title":    ps.ok,
				"meta-description":  ps.ok,
				"link-text":         ps.hasLinks,
				"crawlable-anchors": ps.hasLinks,
				"robots-txt":        robotsApplies,
				"image-alt":         ps.hasImages,
				"hreflang":          len(ps.hreflangs) > 0,
				"canonical":         ps.ok && len(ps.dom.canonicals) > 0,
			}
			outcomes := map[string]auditOutcome{}
			for _, au := range SEOAudits {
				if !applies[au.ID] {
					continue
				}
				outcomes[au.ID] = auditPass
				if failed[ps.route][au.ID] || failed[""][au.ID] {
					outcomes[au.ID] = auditFail
				}
			}
			// A host meant to stay out of search is not penalised for it (D5).
			if a.expect == IndexingNoindex {
				outcomes["is-crawlable"] = auditPass
			}
			rr.Score.SEO = weighted(SEOAudits, outcomes)
		}
		if ps.ok && !a.rawOnly {
			rr.Score.A11y = a11yScore(ps.page.Axe)
		}
		out = append(out, rr)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Route < out[j].Route })
	return out
}

func (a *auditor) summary(routes []RouteReport, notFound string) SiteSummary {
	s := SiteSummary{
		URL:      a.in.Site,
		Host:     a.host,
		Indexing: IndexingSummary{Expected: a.expect},
		Robots:   a.robotsSumm,
		Sitemap:  SitemapSummary{Files: a.sitemapFiles, URLs: a.sitemapURLs, Broken: a.sitemapBroken},
		NotFound: notFound,
	}
	var seo, a11y []*int
	for _, r := range routes {
		// The site's score is its pages': a URL that answered an error is
		// already a finding (status_error, link_broken), not a page.
		if r.Status < 200 || r.Status >= 300 {
			continue
		}
		seo = append(seo, r.Score.SEO)
		a11y = append(a11y, r.Score.A11y)
	}
	s.Score = RouteScore{SEO: meanScore(seo), A11y: meanScore(a11y)}
	if a.rootBlock {
		s.Indexing.Actual = "blocked"
	} else {
		for _, ps := range a.pages {
			if ps.ok && ps.route == a.rootPath {
				s.Indexing.Actual = string(IndexingIndex)
				if !ps.indexable {
					s.Indexing.Actual = string(IndexingNoindex)
				}
				break
			}
		}
	}
	return s
}

var categoryOrder = map[Category]int{CategorySEO: 0, CategoryAccessibility: 1}

// sortFindings orders findings by route (site-wide first), category, code
// and ref.
func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Route != b.Route {
			return a.Route < b.Route
		}
		if a.Category != b.Category {
			return categoryOrder[a.Category] < categoryOrder[b.Category]
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Ref < b.Ref
	})
}

// normURL is the key a URL is matched by: scheme and host lower-cased, the
// fragment dropped, an empty path read as "/". An unparsable URL is its own key.
func normURL(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return s
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment, u.RawFragment = "", ""
	if u.Path == "" && u.Host != "" {
		u.Path = "/"
		u.RawPath = ""
	}
	return u.String()
}

// routeOf is a URL's path (and query): the route findings are keyed by.
func routeOf(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

// distinct returns the non-empty values in first-seen order.
func distinct(vs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range vs {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
