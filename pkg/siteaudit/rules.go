package siteaudit

// Source is what a rule reads, which decides which callers can run it.
type Source string

const (
	// SourceRaw rules judge the raw HTML only, even when a rendered DOM
	// exists (social cards: scrapers do not run JavaScript).
	SourceRaw Source = "raw"
	// SourceDOM rules judge the rendered DOM when the page has one, else the
	// raw HTML. With [SourceRaw] these are what [EvaluateRaw] runs.
	SourceDOM Source = "dom"
	// SourceSite rules need fetched facts beyond the HTML: statuses,
	// headers, robots.txt, sitemaps, link checks, a rendered DOM to compare.
	SourceSite Source = "site"
	// SourceAxe rules come from axe-core's result.
	SourceAxe Source = "axe"
)

// Rule describes one finding code.
type Rule struct {
	// Code is the finding code.
	Code string
	// Category is the score the rule belongs to.
	Category Category
	// Severity is the rule's default severity. A few rules choose per
	// finding: link_broken (error internal, warn external), soft_404 (info
	// for an SPA fallback, warn otherwise), og_tag_missing (info for og:type
	// and og:url), a11y_violation (by axe impact).
	Severity Severity
	// Source is what the rule reads.
	Source Source
	// Audit is the Lighthouse SEO audit (an [SEOAudits] id) a finding fails;
	// "" when the rule is not scored.
	Audit string
	// Fix says how to fix a finding, when the finding has nothing more
	// specific.
	Fix string
}

var catalog = []Rule{
	// Title and description.
	{"title_missing", CategorySEO, SeverityError, SourceDOM, "document-title", "Give the page a <title> that names it."},
	{"title_multiple", CategorySEO, SeverityWarn, SourceDOM, "", "Keep one <title> in the head; crawlers take the first."},
	{"title_length", CategorySEO, SeverityInfo, SourceDOM, "", "Keep the title between 10 and 60 characters so search results show it whole."},
	{"title_duplicate", CategorySEO, SeverityWarn, SourceDOM, "", "Give each page its own title; search engines treat identical titles as duplicate pages."},
	{"title_placeholder", CategorySEO, SeverityInfo, SourceDOM, "", "Replace the starter title with the page's real title."},
	{"description_missing", CategorySEO, SeverityWarn, SourceDOM, "meta-description", "Add <meta name=\"description\" content=\"…\"> summarising the page in one or two sentences."},
	{"description_length", CategorySEO, SeverityInfo, SourceDOM, "", "Keep the description between 50 and 160 characters."},
	{"description_duplicate", CategorySEO, SeverityInfo, SourceDOM, "", "Give each page its own description."},
	{"description_placeholder", CategorySEO, SeverityInfo, SourceDOM, "", "Replace the scaffolded description with a real one."},
	// Canonical.
	{"canonical_missing", CategorySEO, SeverityWarn, SourceDOM, "", "Add <link rel=\"canonical\" href=\"https://<prod host>/<path>\"> naming the page's preferred URL."},
	{"canonical_conflicting", CategorySEO, SeverityWarn, SourceDOM, "canonical", "Keep one canonical link; search engines ignore conflicting ones."},
	{"canonical_relative", CategorySEO, SeverityWarn, SourceDOM, "canonical", "Make the canonical an absolute URL (https://host/path)."},
	{"canonical_invalid", CategorySEO, SeverityWarn, SourceDOM, "canonical", "Make the canonical a valid absolute http(s) URL."},
	{"canonical_foreign_host", CategorySEO, SeverityError, SourceDOM, "canonical", "Point the canonical at this site's production host: as written it hands the page's ranking to another site."},
	{"canonical_to_root", CategorySEO, SeverityWarn, SourceDOM, "canonical", "Point the canonical at this page, not the home page: search engines would drop the page as a duplicate of /."},
	{"head_client_rendered", CategorySEO, SeverityWarn, SourceSite, "", "Put the tag in the served HTML: only Google runs JavaScript, other crawlers and link previews see the HTML as served."},
	// Social cards (raw HTML).
	{"og_tag_missing", CategorySEO, SeverityWarn, SourceRaw, "", "Add the Open Graph tag to the served HTML so link previews in Slack, X, LinkedIn and Facebook show the page."},
	{"twitter_card_missing", CategorySEO, SeverityInfo, SourceRaw, "", "Add <meta name=\"twitter:card\" content=\"summary_large_image\"> for a large preview on X."},
	{"og_image_relative", CategorySEO, SeverityWarn, SourceRaw, "", "Make og:image an absolute https URL: scrapers do not resolve relative ones."},
	{"og_image_unreachable", CategorySEO, SeverityWarn, SourceSite, "", "Serve the og:image at the URL the tag names; link previews show no image when it fails."},
	{"og_image_placeholder", CategorySEO, SeverityInfo, SourceSite, "", "Replace the scaffolded og.png with a 1200×630 image for this site."},
	// Indexing.
	{"indexing_mismatch", CategorySEO, SeverityError, SourceDOM, "is-crawlable", "Remove the noindex (meta robots, X-Robots-Tag) or the robots.txt Disallow: this host is meant to be in search results."},
	{"preview_indexable", CategorySEO, SeverityInfo, SourceSite, "", "This host is meant to stay out of search results and is indexable; the platform is expected to send noindex here."},
	{"duplicate_without_canonical", CategorySEO, SeverityWarn, SourceSite, "", "This non-production host is indexable: add a canonical to the production URL so search engines do not index it as a duplicate."},
	{"blocked_by_robots", CategorySEO, SeverityInfo, SourceSite, "is-crawlable", "robots.txt disallows this page; remove the Disallow if it should be in search results."},
	// robots.txt.
	{"robots_unreachable", CategorySEO, SeverityError, SourceSite, "robots-txt", "Serve /robots.txt (or a 404): while it fails with a 5xx, Google stops crawling the site."},
	{"robots_missing", CategorySEO, SeverityInfo, SourceSite, "", "Add a /robots.txt with \"User-agent: *\", \"Allow: /\" and a Sitemap: line."},
	{"robots_invalid", CategorySEO, SeverityWarn, SourceSite, "robots-txt", "Fix the robots.txt line; crawlers ignore lines they cannot parse."},
	{"robots_no_sitemap", CategorySEO, SeverityInfo, SourceSite, "", "Add \"Sitemap: https://<host>/sitemap.xml\" to robots.txt."},
	// Sitemaps.
	{"sitemap_missing", CategorySEO, SeverityInfo, SourceSite, "", "Publish /sitemap.xml listing the site's pages and name it in robots.txt."},
	{"sitemap_unreachable", CategorySEO, SeverityWarn, SourceSite, "", "Serve the sitemap robots.txt names, or remove its Sitemap: line."},
	{"sitemap_invalid", CategorySEO, SeverityWarn, SourceSite, "", "Make the sitemap well-formed XML with a <urlset> or <sitemapindex> root."},
	{"sitemap_too_large", CategorySEO, SeverityWarn, SourceSite, "", "Split the sitemap: one file holds at most 50,000 URLs and 50 MB; list the parts in a sitemap index."},
	{"sitemap_url_invalid", CategorySEO, SeverityWarn, SourceSite, "", "List absolute http(s) URLs in <loc>."},
	{"sitemap_host_mismatch", CategorySEO, SeverityWarn, SourceSite, "", "List this site's own URLs: search engines ignore sitemap URLs on another host."},
	{"sitemap_url_broken", CategorySEO, SeverityWarn, SourceSite, "", "Remove the URL from the sitemap or serve it: it answers with an error."},
	{"sitemap_url_noindex", CategorySEO, SeverityWarn, SourceSite, "", "Remove the noindex page from the sitemap, or drop its noindex."},
	{"sitemap_url_blocked", CategorySEO, SeverityWarn, SourceSite, "", "Remove the URL from the sitemap, or allow it in robots.txt."},
	// hreflang.
	{"hreflang_invalid", CategorySEO, SeverityWarn, SourceDOM, "hreflang", "Use a valid language code (en, en-GB, pt-BR, x-default) and an absolute href."},
	{"hreflang_not_reciprocal", CategorySEO, SeverityWarn, SourceDOM, "", "Link the alternate back: search engines ignore hreflang pairs that are not reciprocal."},
	{"hreflang_no_x_default", CategorySEO, SeverityInfo, SourceDOM, "", "Add <link rel=\"alternate\" hreflang=\"x-default\" href=\"…\"> for visitors no language matches."},
	{"hreflang_missing_self", CategorySEO, SeverityInfo, SourceDOM, "", "Include the page itself among its hreflang alternates."},
	// Structured data.
	{"jsonld_invalid", CategorySEO, SeverityWarn, SourceDOM, "", "Fix the JSON-LD block: it is not valid JSON, so search engines ignore it."},
	{"jsonld_no_context", CategorySEO, SeverityWarn, SourceDOM, "", "Add \"@context\": \"https://schema.org\" to the JSON-LD block."},
	{"jsonld_missing_property", CategorySEO, SeverityWarn, SourceDOM, "", "Add the property: without it the type is not eligible for rich results."},
	// Links and images.
	{"link_broken", CategorySEO, SeverityError, SourceSite, "", "Fix or remove the link: its target answers with an error."},
	{"link_not_crawlable", CategorySEO, SeverityWarn, SourceDOM, "crawlable-anchors", "Give the link a real href; crawlers do not follow javascript: links or click handlers."},
	{"link_text_generic", CategorySEO, SeverityWarn, SourceDOM, "link-text", "Describe the target in the link text instead of \"click here\" or \"read more\"."},
	{"link_text_empty", CategorySEO, SeverityWarn, SourceDOM, "", "Give the link text, an aria-label, or an image with alt text."},
	{"image_alt_missing", CategorySEO, SeverityWarn, SourceDOM, "image-alt", "Add alt text describing the image, or alt=\"\" when it is decorative."},
	// Document basics.
	{"lang_missing", CategorySEO, SeverityWarn, SourceDOM, "", "Add a lang attribute to <html> (<html lang=\"en\">)."},
	{"viewport_missing", CategorySEO, SeverityWarn, SourceDOM, "", "Add <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">."},
	{"status_error", CategorySEO, SeverityError, SourceSite, "http-status-code", "Serve the page with a 2xx status, or stop linking to it."},
	{"fetch_failed", CategorySEO, SeverityError, SourceSite, "", "Make the page reachable: the fetch failed."},
	{"soft_404", CategorySEO, SeverityWarn, SourceSite, "", "Answer unknown paths with a 404 (ship a 404.html) so search engines do not index error pages."},
	// Accessibility.
	{"a11y_violation", CategoryAccessibility, SeverityError, SourceAxe, "", "Fix the elements axe names; the help link explains the rule."},
}

var ruleByCode = func() map[string]Rule {
	m := make(map[string]Rule, len(catalog))
	for _, r := range catalog {
		m[r.Code] = r
	}
	return m
}()

// Catalog returns every rule, in a stable order.
func Catalog() []Rule {
	return append([]Rule(nil), catalog...)
}

// LookupRule returns the rule for a finding code.
func LookupRule(code string) (Rule, bool) {
	r, ok := ruleByCode[code]
	return r, ok
}
