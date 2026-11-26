package siteaudit

import (
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Thresholds the length rules use.
const (
	titleMinChars       = 10
	titleMaxChars       = 60
	descriptionMinChars = 50
	descriptionMaxChars = 160
	// maxFindingsPerCode bounds the per-URL findings one sitemap rule emits;
	// SiteSummary.Sitemap carries the true counts.
	maxFindingsPerCode = 50
	// googlebot is the agent robots.txt is judged for: the audit sees what
	// Google sees.
	googlebot = "Googlebot"
)

// Evaluate judges every fact in the input and returns the report. It never
// fails: a fact it cannot read becomes a finding, and a fact that is absent
// leaves its rules unjudged.
func Evaluate(in Input) Report {
	return evaluate(in, false)
}

// EvaluateRaw judges only what the pages' raw HTML says: the [SourceRaw] and
// [SourceDOM] rules, read from RawHTML. Statuses, headers, rendered DOMs,
// axe results, robots.txt, sitemaps and link checks are ignored, so it suits
// a build directory (`forge lint --seo`): a page with Status 0 is read as a
// 200, since a file has no status.
func EvaluateRaw(in Input) Report {
	pages := make([]Page, len(in.Pages))
	for i, p := range in.Pages {
		status := p.Status
		if status == 0 {
			status = http.StatusOK
		}
		pages[i] = Page{URL: p.URL, Status: status, RawHTML: p.RawHTML}
	}
	return evaluate(Input{Site: in.Site, Pages: pages, Expect: in.Expect}, true)
}

// auditor carries one evaluation's state.
type auditor struct {
	in         Input
	rawOnly    bool
	site       *url.URL
	host       string
	canonHost  string
	rootPath   string
	expect     Indexing
	robots     *Robots
	rootBlock  bool
	statuses   map[string]LinkResult
	findings   []Finding
	seen       map[string]bool
	pages      []*pageState
	pageByURL  map[string]*pageState
	robotsSumm RobotsSummary
	// sitemapFiles, sitemapURLs and sitemapBroken are SiteSummary.Sitemap.
	sitemapFiles  int
	sitemapURLs   int
	sitemapBroken int
}

// pageState is one page being judged.
type pageState struct {
	page      Page
	url       *url.URL
	norm      string
	route     string
	ok        bool // 2xx with HTML judged
	raw       *document
	dom       *document
	head      Head
	indexable bool
	canonical *url.URL
	hasLinks  bool
	hasImages bool
	hreflangs []Alternate
}

func evaluate(in Input, rawOnly bool) Report {
	a := &auditor{
		in:        in,
		rawOnly:   rawOnly,
		statuses:  map[string]LinkResult{},
		seen:      map[string]bool{},
		pageByURL: map[string]*pageState{},
		expect:    in.Expect.Indexing,
	}
	if a.expect == "" {
		a.expect = IndexingIndex
	}
	a.site, _ = url.Parse(in.Site)
	if a.site == nil {
		a.site = &url.URL{}
	}
	a.host = strings.ToLower(a.site.Host)
	a.canonHost = strings.ToLower(in.Expect.CanonicalHost)
	if a.canonHost == "" {
		a.canonHost = a.host
	}
	a.rootPath = a.site.Path
	if a.rootPath == "" {
		a.rootPath = "/"
	}
	if !strings.HasSuffix(a.rootPath, "/") {
		a.rootPath += "/"
	}

	for _, p := range in.Pages {
		n := normURL(p.URL)
		if _, dup := a.pageByURL[n]; dup {
			continue
		}
		ps := &pageState{page: p, norm: n}
		ps.url, _ = url.Parse(p.URL)
		if ps.url == nil {
			ps.url = &url.URL{Path: p.URL}
		}
		ps.route = routeOf(ps.url)
		a.pages = append(a.pages, ps)
		a.pageByURL[n] = ps
		a.statuses[n] = LinkResult{URL: p.URL, Status: p.Status, Err: p.Err}
	}
	for _, lr := range in.LinkResults {
		n := normURL(lr.URL)
		if _, ok := a.statuses[n]; !ok {
			a.statuses[n] = lr
		} else if lr.Placeholder {
			s := a.statuses[n]
			s.Placeholder = true
			a.statuses[n] = s
		}
	}

	a.judgeRobots()
	for _, ps := range a.pages {
		a.judgePage(ps)
	}
	a.judgeDuplicates()
	a.judgeHreflangReciprocity()
	a.judgeIndexingSite()
	a.judgeSitemaps()
	soft := a.judgeNotFound()

	rep := Report{RulesVersion: RulesVersion, Findings: a.findings}
	rep.Routes = a.score()
	rep.Site = a.summary(rep.Routes, soft)
	sortFindings(rep.Findings)
	if rep.Findings == nil {
		rep.Findings = []Finding{}
	}
	if rep.Routes == nil {
		rep.Routes = []RouteReport{}
	}
	return rep
}

// emit records a finding with the rule's default severity and fix, once per
// (code, route, ref). In raw-only mode a site or axe rule is dropped.
func (a *auditor) emit(code, route, ref string, ev map[string]string) {
	a.emitSev(code, route, ref, "", ev)
}

func (a *auditor) emitSev(code, route, ref string, sev Severity, ev map[string]string) {
	r, ok := ruleByCode[code]
	if !ok {
		panic("siteaudit: finding code without a catalog rule: " + code)
	}
	if a.rawOnly && (r.Source == SourceSite || r.Source == SourceAxe) {
		return
	}
	key := code + "\x00" + route + "\x00" + ref
	if a.seen[key] {
		return
	}
	a.seen[key] = true
	if sev == "" {
		sev = r.Severity
	}
	for k, v := range ev {
		if v == "" {
			delete(ev, k)
		}
	}
	if len(ev) == 0 {
		ev = nil
	}
	a.findings = append(a.findings, Finding{
		Code: code, Severity: sev, Category: r.Category, Route: route, Ref: ref,
		Evidence: ev, Fix: r.Fix,
	})
}

func ev(kv ...string) map[string]string {
	m := make(map[string]string, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

// judgeRobots parses and judges robots.txt.
func (a *auditor) judgeRobots() {
	f := a.in.Robots
	if f == nil {
		return
	}
	switch {
	case f.Status >= 200 && f.Status < 300:
		a.robotsSumm.Status = "ok"
	case f.Status >= 400 && f.Status < 500:
		// No robots.txt: everything may be crawled.
		a.robotsSumm.Status = "missing"
		a.emit("robots_missing", "", "/robots.txt", ev("status", itoa(f.Status)))
		return
	default:
		a.robotsSumm.Status = "unreachable"
		a.emit("robots_unreachable", "", "/robots.txt", ev("status", itoa(f.Status), "error", f.Err))
		return
	}
	a.robots = ParseRobots(f.Body)
	a.robotsSumm.Sitemaps = a.robots.Sitemaps
	a.robotsSumm.Problems = len(a.robots.Problems)
	for i, p := range a.robots.Problems {
		if i == maxFindingsPerCode {
			break
		}
		a.emit("robots_invalid", "", "line "+itoa(p.Line), ev("line", p.Text, "problem", p.Message))
	}
	if len(a.robots.Sitemaps) == 0 {
		a.emit("robots_no_sitemap", "", "/robots.txt", nil)
	}
	a.rootBlock = !a.robots.Allowed(googlebot, a.rootPath)
	a.robotsSumm.BlocksRoot = a.rootBlock
	if a.rootBlock && a.expect == IndexingIndex {
		a.emit("indexing_mismatch", "", "robots_txt", ev("expected", string(a.expect), "actual", "robots.txt disallows "+a.rootPath))
	}
}

func (a *auditor) judgePage(ps *pageState) {
	p, route := ps.page, ps.route
	if p.Status == 0 {
		a.emit("fetch_failed", route, "", ev("error", p.Err))
		return
	}
	if p.Status >= 400 {
		a.emit("status_error", route, "", ev("status", itoa(p.Status)))
	}
	if p.Status < 200 || p.Status >= 300 {
		return
	}
	ps.ok = true
	ps.raw = parseDocument(p.RawHTML)
	ps.dom = ps.raw
	if p.RenderedHTML != "" {
		ps.dom = parseDocument(p.RenderedHTML)
	}
	ps.head = ps.dom.head()
	ps.head.Social = ps.raw.head().Social
	ps.head.XRobotsTag = strings.Join(p.Header.Values("X-Robots-Tag"), ", ")
	ps.hreflangs = ps.dom.alternates

	a.judgeTitle(ps)
	a.judgeDescription(ps)
	a.judgeClientRendered(ps)
	a.judgeCanonical(ps)
	a.judgeSocial(ps)
	a.judgeIndexing(ps)
	a.judgeHreflang(ps)
	for i, raw := range ps.dom.jsonld {
		for _, pr := range checkJSONLD(i, raw) {
			a.emit(pr.code, route, pr.ref, ev("problem", pr.msg))
		}
	}
	a.judgeLinks(ps)
	for _, img := range ps.dom.images {
		ps.hasImages = true
		if !img.hidden && !img.hasAlt {
			a.emit("image_alt_missing", route, img.src, nil)
		}
	}
	if ps.dom.lang == "" {
		a.emit("lang_missing", route, "", nil)
	}
	if !ps.dom.hasViewport {
		a.emit("viewport_missing", route, "", nil)
	}
	if p.Axe != nil {
		for _, f := range axeFindings(route, p.Axe) {
			if a.rawOnly {
				break
			}
			a.findings = append(a.findings, f)
		}
	}
}

var placeholderTitles = map[string]bool{
	"vite app": true, "vite + react": true, "vite + react + ts": true, "vite + vue": true,
	"vite + vue + ts": true, "vite + svelte": true, "create next app": true, "react app": true,
	"document": true, "untitled": true,
}

func (a *auditor) judgeTitle(ps *pageState) {
	titles := distinct(ps.dom.titles)
	if ps.head.Title == "" {
		a.emit("title_missing", ps.route, "title", nil)
		return
	}
	if len(titles) > 1 {
		a.emit("title_multiple", ps.route, "title", ev("titles", strings.Join(titles, " | ")))
	}
	if n := utf8.RuneCountInString(ps.head.Title); n < titleMinChars || n > titleMaxChars {
		a.emit("title_length", ps.route, "title", ev("title", ps.head.Title, "length", itoa(n)))
	}
	if placeholderTitles[strings.ToLower(ps.head.Title)] {
		a.emit("title_placeholder", ps.route, "title", ev("title", ps.head.Title))
	} else if raw := ps.raw.head().Title; ps.raw != ps.dom && placeholderTitles[strings.ToLower(raw)] {
		// JavaScript replaces it, but crawlers that do not render, and
		// link previews, show the starter title.
		a.emit("title_placeholder", ps.route, "title", ev("title", raw, "source", "raw_html"))
	}
}

func (a *auditor) judgeDescription(ps *pageState) {
	d := ps.head.Description
	if d == "" {
		a.emit("description_missing", ps.route, "description", nil)
		return
	}
	if n := utf8.RuneCountInString(d); n < descriptionMinChars || n > descriptionMaxChars {
		a.emit("description_length", ps.route, "description", ev("description", d, "length", itoa(n)))
	}
	if IsPlaceholderDescription(d) {
		a.emit("description_placeholder", ps.route, "description", ev("description", d))
	}
}

// IsPlaceholderDescription reports a description that is visibly a
// placeholder: forge's scaffolded "… Replace this description." (design D3)
// or a framework starter's.
func IsPlaceholderDescription(d string) bool {
	l := strings.ToLower(d)
	return strings.Contains(l, "replace this description") || strings.Contains(l, "generated by create next app")
}

// judgeClientRendered names the head tags only the rendered DOM has (D1).
func (a *auditor) judgeClientRendered(ps *pageState) {
	if ps.page.RenderedHTML == "" {
		return
	}
	raw := ps.raw.head()
	check := func(tag, rawV, domV string) {
		if rawV == "" && domV != "" {
			ps.head.ClientRendered = append(ps.head.ClientRendered, tag)
			a.emit("head_client_rendered", ps.route, tag, nil)
		}
	}
	check("title", raw.Title, ps.head.Title)
	check("description", raw.Description, ps.head.Description)
	check("canonical", raw.Canonical, ps.head.Canonical)
}

func (a *auditor) judgeCanonical(ps *pageState) {
	cs := distinct(ps.dom.canonicals)
	if len(cs) == 0 {
		if a.expect == IndexingIndex {
			a.emit("canonical_missing", ps.route, "canonical", nil)
		}
		return
	}
	if len(cs) > 1 {
		a.emit("canonical_conflicting", ps.route, "canonical", ev("canonicals", strings.Join(cs, " | ")))
	}
	raw := cs[0]
	u, err := url.Parse(raw)
	if err != nil || raw == "" {
		a.emit("canonical_invalid", ps.route, "canonical", ev("canonical", raw))
		return
	}
	if !u.IsAbs() {
		a.emit("canonical_relative", ps.route, "canonical", ev("canonical", raw))
		u = ps.url.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		a.emit("canonical_invalid", ps.route, "canonical", ev("canonical", raw))
		return
	}
	ps.canonical = u
	ch := strings.ToLower(u.Host)
	if ch != a.host && ch != a.canonHost {
		a.emit("canonical_foreign_host", ps.route, "canonical",
			ev("canonical", raw, "host", ch, "expected_host", a.canonHost))
		return
	}
	cpath := u.Path
	if cpath == "" {
		cpath = "/"
	}
	if !strings.HasSuffix(cpath, "/") {
		cpath += "/"
	}
	ppath := ps.url.Path
	if ppath == "" {
		ppath = "/"
	}
	if !strings.HasSuffix(ppath, "/") {
		ppath += "/"
	}
	if cpath == a.rootPath && ppath != a.rootPath {
		a.emit("canonical_to_root", ps.route, "canonical", ev("canonical", raw))
	}
}

// ogRequired are the Open Graph tags a link preview needs (warn) and the
// ones it should have (info).
var ogRequired = []struct {
	tag string
	sev Severity
}{
	{"og:title", SeverityWarn},
	{"og:description", SeverityWarn},
	{"og:image", SeverityWarn},
	{"og:type", SeverityInfo},
	{"og:url", SeverityInfo},
}

// judgeSocial judges the raw HTML's Open Graph and Twitter tags (D1).
func (a *auditor) judgeSocial(ps *pageState) {
	social := ps.head.Social
	complete := true
	for _, r := range ogRequired {
		if strings.TrimSpace(social[r.tag]) == "" {
			a.emitSev("og_tag_missing", ps.route, r.tag, r.sev, nil)
			if r.tag != "og:description" {
				complete = false
			}
		}
	}
	ps.head.OGComplete = complete
	if strings.TrimSpace(social["twitter:card"]) == "" {
		a.emit("twitter_card_missing", ps.route, "twitter:card", nil)
	}
	img := strings.TrimSpace(social["og:image"])
	if img == "" {
		return
	}
	u, err := url.Parse(img)
	if err != nil {
		a.emit("og_image_relative", ps.route, "og:image", ev("og:image", img))
		return
	}
	if !u.IsAbs() {
		a.emit("og_image_relative", ps.route, "og:image", ev("og:image", img))
		u = ps.url.ResolveReference(u)
	}
	lr, ok := a.statuses[normURL(u.String())]
	if !ok {
		return
	}
	if broken(lr) {
		a.emit("og_image_unreachable", ps.route, "og:image", ev("url", u.String(), "status", itoa(lr.Status), "error", lr.Err))
	} else if lr.Placeholder {
		a.emit("og_image_placeholder", ps.route, "og:image", ev("url", u.String()))
	}
}

// judgeIndexing compares the page's indexability with the expectation (D5).
func (a *auditor) judgeIndexing(ps *pageState) {
	metaNo := hasNoindex(ps.head.Robots)
	headerNo := xRobotsNoindex(ps.page.Header)
	blocked := a.robots != nil && !a.robots.Allowed(googlebot, ps.route)
	ps.indexable = !metaNo && !headerNo && !blocked
	ps.head.Indexable = ps.indexable
	if a.expect != IndexingIndex {
		return
	}
	if metaNo {
		a.emit("indexing_mismatch", ps.route, "meta_robots", ev("expected", "index", "robots", ps.head.Robots))
	}
	if headerNo {
		a.emit("indexing_mismatch", ps.route, "x_robots_tag", ev("expected", "index", "x_robots_tag", ps.head.XRobotsTag))
	}
	if blocked && !a.rootBlock {
		a.emit("blocked_by_robots", ps.route, "robots_txt", nil)
	}
}

// hasNoindex reads a robots directive list ("noindex, nofollow", "none").
func hasNoindex(content string) bool {
	for _, d := range strings.FieldsFunc(strings.ToLower(content), func(r rune) bool { return r == ',' || r == ' ' }) {
		if d == "noindex" || d == "none" {
			return true
		}
	}
	return false
}

// xRobotsNoindex reads X-Robots-Tag values. A value scoped to a bot
// ("otherbot: noindex") counts only for googlebot; an unscoped one counts.
func xRobotsNoindex(h http.Header) bool {
	for _, v := range h.Values("X-Robots-Tag") {
		scope, rest, scoped := strings.Cut(v, ":")
		if scoped && !strings.ContainsAny(scope, ", ") {
			if !strings.EqualFold(strings.TrimSpace(scope), "googlebot") {
				continue
			}
			v = rest
		}
		if hasNoindex(v) {
			return true
		}
	}
	return false
}

var hreflangCode = regexp.MustCompile(`^(?i)[a-z]{2,3}(-[a-z]{4})?(-([a-z]{2}|[0-9]{3}))?$`)

func (a *auditor) judgeHreflang(ps *pageState) {
	if len(ps.hreflangs) == 0 {
		return
	}
	hasDefault, hasSelf := false, false
	for _, alt := range ps.hreflangs {
		code := alt.Hreflang
		switch {
		case strings.EqualFold(code, "x-default"):
			hasDefault = true
		case !hreflangCode.MatchString(code):
			a.emit("hreflang_invalid", ps.route, code, ev("hreflang", code, "problem", "not a language code (en, en-GB, pt-BR)"))
		case strings.HasSuffix(strings.ToLower(code), "-uk"):
			a.emit("hreflang_invalid", ps.route, code, ev("hreflang", code, "problem", "UK is not a region code: use GB"))
		}
		u, err := url.Parse(alt.Href)
		if err != nil || !u.IsAbs() {
			a.emit("hreflang_invalid", ps.route, alt.Href, ev("hreflang", code, "href", alt.Href, "problem", "href must be an absolute URL"))
			continue
		}
		if normURL(u.String()) == ps.norm || (ps.canonical != nil && normURL(ps.canonical.String()) == normURL(u.String())) {
			hasSelf = true
		}
	}
	if !hasDefault {
		a.emit("hreflang_no_x_default", ps.route, "x-default", nil)
	}
	if !hasSelf {
		a.emit("hreflang_missing_self", ps.route, "hreflang", nil)
	}
}

// judgeHreflangReciprocity checks each alternate that was crawled links back.
func (a *auditor) judgeHreflangReciprocity() {
	for _, ps := range a.pages {
		if !ps.ok {
			continue
		}
		self := map[string]bool{ps.norm: true}
		if ps.canonical != nil {
			self[normURL(ps.canonical.String())] = true
		}
		for _, alt := range ps.hreflangs {
			target := a.pageByURL[normURL(alt.Href)]
			if target == nil || target == ps || !target.ok {
				continue
			}
			back := false
			for _, b := range target.hreflangs {
				if self[normURL(b.Href)] {
					back = true
					break
				}
			}
			if !back {
				a.emit("hreflang_not_reciprocal", ps.route, alt.Href, ev("hreflang", alt.Hreflang, "alternate", target.route))
			}
		}
	}
}

var genericLinkText = map[string]bool{
	"click here": true, "click this": true, "go": true, "here": true, "information": true,
	"learn more": true, "more": true, "more info": true, "more information": true,
	"right here": true, "read more": true, "see more": true, "start": true, "this": true,
	"link": true, "continue": true,
}

func (a *auditor) judgeLinks(ps *pageState) {
	base := ps.dom.resolveBase(ps.url)
	for _, an := range ps.dom.anchors {
		if !an.hasHref {
			if an.onclick && !an.named {
				ps.hasLinks = true
				a.emit("link_not_crawlable", ps.route, "<a onclick>", ev("text", an.text))
			}
			continue
		}
		ps.hasLinks = true
		if strings.HasPrefix(strings.ToLower(an.href), "javascript:") {
			a.emit("link_not_crawlable", ps.route, an.href, ev("text", an.text))
			continue
		}
		target := resolveHTTP(base, an.href)
		if target == nil {
			continue
		}
		tnorm := normURL(target.String())
		if tnorm == ps.norm {
			// A same-page link (a fragment) has nothing to check.
			continue
		}
		if !an.ariaHide {
			l := strings.ToLower(strings.Trim(an.text, " .!?:→»›"))
			switch {
			case an.text == "":
				a.emit("link_text_empty", ps.route, target.String(), nil)
			case genericLinkText[l]:
				a.emit("link_text_generic", ps.route, target.String(), ev("text", an.text))
			}
		}
		lr, ok := a.statuses[tnorm]
		if !ok || !broken(lr) {
			continue
		}
		sev := SeverityWarn
		scope := "external"
		if strings.ToLower(target.Host) == a.host {
			sev, scope = SeverityError, "internal"
		}
		a.emitSev("link_broken", ps.route, target.String(), sev,
			ev("scope", scope, "status", itoa(lr.Status), "error", lr.Err))
	}
}

// broken reports a check that answered 4xx/5xx or failed.
func broken(lr LinkResult) bool {
	return lr.Status >= 400 || (lr.Status == 0 && lr.Err != "")
}

// judgeDuplicates flags titles and descriptions shared by indexable pages.
func (a *auditor) judgeDuplicates() {
	dup := func(code string, val func(*pageState) string) {
		by := map[string][]string{}
		for _, ps := range a.pages {
			if ps.ok && ps.indexable {
				if v := val(ps); v != "" {
					by[v] = append(by[v], ps.route)
				}
			}
		}
		for v, routes := range by {
			if len(routes) < 2 {
				continue
			}
			sort.Strings(routes)
			for _, r := range routes {
				var others []string
				for _, o := range routes {
					if o != r && len(others) < 5 {
						others = append(others, o)
					}
				}
				a.emit(code, r, strings.SplitN(code, "_", 2)[0], ev("value", v, "also_on", strings.Join(others, ", "), "count", itoa(len(routes))))
			}
		}
	}
	dup("title_duplicate", func(ps *pageState) string { return ps.head.Title })
	dup("description_duplicate", func(ps *pageState) string { return ps.head.Description })
}

// judgeIndexingSite applies the host-level indexing rules (D5).
func (a *auditor) judgeIndexingSite() {
	if a.expect == IndexingNoindex {
		var routes []string
		for _, ps := range a.pages {
			if ps.ok && ps.indexable {
				routes = append(routes, ps.route)
			}
		}
		if len(routes) > 0 && !a.rootBlock {
			sort.Strings(routes)
			ex := routes
			if len(ex) > 5 {
				ex = ex[:5]
			}
			a.emit("preview_indexable", "", "host", ev("host", a.host, "indexable_routes", itoa(len(routes)), "examples", strings.Join(ex, ", ")))
		}
		return
	}
	if a.canonHost == a.host {
		return
	}
	for _, ps := range a.pages {
		if !ps.ok || !ps.indexable {
			continue
		}
		if ps.canonical == nil || strings.ToLower(ps.canonical.Host) == a.host {
			a.emit("duplicate_without_canonical", ps.route, "canonical", ev("host", a.host, "canonical_host", a.canonHost))
		}
	}
}

// judgeSitemaps parses and judges every sitemap fetched.
func (a *auditor) judgeSitemaps() {
	if a.in.Robots == nil && len(a.in.Sitemaps) == 0 {
		return
	}
	parsed := 0
	counts := map[string]int{}
	limited := func(code, ref string, e map[string]string) {
		counts[code]++
		if counts[code] <= maxFindingsPerCode {
			a.emit(code, "", ref, e)
		}
	}
	for _, sm := range a.in.Sitemaps {
		if sm.Status < 200 || sm.Status >= 300 {
			a.emit("sitemap_unreachable", "", sm.URL, ev("status", itoa(sm.Status), "error", sm.Err))
			continue
		}
		s, err := ParseSitemap(sm.Body)
		if err != nil {
			a.emit("sitemap_invalid", "", sm.URL, ev("error", err.Error()))
			continue
		}
		parsed++
		if len(s.Locs) > SitemapMaxURLs || s.Bytes > SitemapMaxBytes {
			a.emit("sitemap_too_large", "", sm.URL, ev("urls", itoa(len(s.Locs)), "bytes", itoa(s.Bytes)))
		}
		if s.Index {
			continue
		}
		hostMismatch := 0
		example := ""
		for _, loc := range s.Locs {
			u, err := url.Parse(loc)
			if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
				limited("sitemap_url_invalid", loc, ev("sitemap", sm.URL))
				continue
			}
			a.sitemapURLs++
			h := strings.ToLower(u.Host)
			if h != a.host && h != a.canonHost {
				hostMismatch++
				if example == "" {
					example = loc
				}
				continue
			}
			n := normURL(loc)
			if lr, ok := a.statuses[n]; ok && broken(lr) {
				a.sitemapBroken++
				limited("sitemap_url_broken", loc, ev("sitemap", sm.URL, "status", itoa(lr.Status), "error", lr.Err))
				continue
			}
			if ps := a.pageByURL[n]; ps != nil && ps.ok && (hasNoindex(ps.head.Robots) || xRobotsNoindex(ps.page.Header)) {
				a.sitemapBroken++
				limited("sitemap_url_noindex", loc, ev("sitemap", sm.URL))
				continue
			}
			if a.robots != nil && h == a.host && !a.robots.Allowed(googlebot, routeOf(u)) {
				limited("sitemap_url_blocked", loc, ev("sitemap", sm.URL))
			}
		}
		if hostMismatch > 0 {
			a.emit("sitemap_host_mismatch", "", sm.URL, ev("urls", itoa(hostMismatch), "example", example, "expected_host", a.canonHost))
		}
	}
	a.sitemapFiles = parsed
	if parsed == 0 && len(a.in.Sitemaps) == 0 {
		a.emit("sitemap_missing", "", "/sitemap.xml", nil)
	}
}
