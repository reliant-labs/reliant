package siteaudit

import "net/http"

// RulesVersion names the rule set, severities and scoring tables this package
// applies. It changes whenever a rule, a default severity or a weight does,
// so a stored score is compared only with scores of the same version.
const RulesVersion = "1"

// Input is everything an audit judges. Only Site and Pages are required;
// every other fact that is absent leaves the rules that need it unjudged
// (never failed).
type Input struct {
	// Site is the base URL the audit crawled: scheme, host and base path
	// ("https://example.com", "https://acme.sites.example.dev/docs/").
	Site string
	// Pages are the pages fetched, in any order. A URL fetched twice is
	// judged once (the first).
	Pages []Page
	// Robots is the site's /robots.txt as fetched; nil when it was not.
	Robots *Fetched
	// Sitemaps are the sitemap files fetched (from robots.txt's Sitemap:
	// lines, /sitemap.xml, and a sitemap index's children), decompressed.
	Sitemaps []Fetched
	// LinkResults are the statuses of the link targets the caller checked
	// (internal links that were not crawled as pages, external links, the
	// og:image). A target with no result is not judged.
	LinkResults []LinkResult
	// NotFound is the fetch of a path that cannot exist on the site, used to
	// detect a soft 404; nil when it was not probed.
	NotFound *Page
	// Expect is what the site is meant to be.
	Expect Expect
}

// Page is one fetched page.
type Page struct {
	// URL is the absolute URL that was fetched.
	URL string
	// Status is the HTTP status code; 0 when the fetch failed (see Err).
	Status int
	// Header is the response header (X-Robots-Tag, Location).
	Header http.Header
	// RawHTML is the response body as served.
	RawHTML string
	// RenderedHTML is the DOM serialized after a headless browser ran the
	// page's JavaScript; "" when the page was not rendered.
	RenderedHTML string
	// Axe is axe-core's result for the rendered page; nil when axe did not run.
	Axe *AxeResult
	// Err is why the fetch failed, when it did.
	Err string
}

// Fetched is a fetched text resource: robots.txt or a sitemap.
type Fetched struct {
	// URL is the absolute URL fetched.
	URL string
	// Status is the HTTP status code; 0 when the fetch failed (see Err).
	Status int
	// Body is the response body (a sitemap decompressed).
	Body string
	// Err is why the fetch failed, when it did.
	Err string
}

// LinkResult is the outcome of checking one link target.
type LinkResult struct {
	// URL is the absolute URL checked.
	URL string
	// Status is the final HTTP status code; 0 when the check failed (see Err).
	Status int
	// Err is why the check failed (timeout, DNS, refused), when it did.
	Err string
	// Placeholder reports that the target is forge's scaffolded placeholder
	// image ([IsPlaceholderImage] on its bytes). Only an og:image's result
	// needs it.
	Placeholder bool
}

// Indexing is whether a host is meant to be in search engines.
type Indexing string

const (
	// IndexingIndex expects the host's pages to be indexable. The zero value
	// of Expect.Indexing means the same.
	IndexingIndex Indexing = "index"
	// IndexingNoindex expects the host to keep search engines out (a preview
	// host).
	IndexingNoindex Indexing = "noindex"
)

// Expect is what the audited host is meant to be.
type Expect struct {
	// Indexing is the expected indexing; "" means IndexingIndex.
	Indexing Indexing
	// CanonicalHost is the host canonical tags should name: the prod
	// hostname (its live custom domain, else its platform hostname). "" means
	// the audited host itself.
	CanonicalHost string
}

// Severity is how much a finding matters.
type Severity string

const (
	// SeverityError breaks indexing or a page; release health reads a new one
	// as a regression.
	SeverityError Severity = "error"
	// SeverityWarn is what a site should fix.
	SeverityWarn Severity = "warn"
	// SeverityInfo is advice.
	SeverityInfo Severity = "info"
)

// Category is which score a finding belongs to.
type Category string

const (
	// CategorySEO is search and social-card findings.
	CategorySEO Category = "seo"
	// CategoryAccessibility is axe-core findings.
	CategoryAccessibility Category = "accessibility"
)

// Finding is one problem the audit found.
type Finding struct {
	// Code names the rule ([Catalog] lists every code).
	Code string `json:"code"`
	// Severity is error, warn or info.
	Severity Severity `json:"severity"`
	// Category is seo or accessibility.
	Category Category `json:"category"`
	// Route is the route the finding is on; "" for a site-wide finding.
	Route string `json:"route,omitempty"`
	// Ref is what the finding is about within the route: a tag, a URL, a
	// property, an axe rule id. Code, Route and Ref identify a finding across
	// two audits.
	Ref string `json:"ref,omitempty"`
	// Evidence is what was observed, keyed by name.
	Evidence map[string]string `json:"evidence,omitempty"`
	// Nodes are the elements an axe violation found (at most 20, each
	// snippet at most 300 characters).
	Nodes []Node `json:"nodes,omitempty"`
	// Fix says how to fix it.
	Fix string `json:"fix"`
}

// Node is one element an axe violation names.
type Node struct {
	// Target is the element's CSS selector (shadow and frame paths joined by
	// " >>> ").
	Target string `json:"target"`
	// HTML is the element's outer HTML, truncated to 300 characters.
	HTML string `json:"html,omitempty"`
	// Summary is axe's failure summary for the element.
	Summary string `json:"summary,omitempty"`
}

// RouteScore is a route's scores, 0-100. A nil score was not evaluated:
// SEO for a page with no HTML to judge, A11y for a page axe did not run on.
type RouteScore struct {
	SEO  *int `json:"seo"`
	A11y *int `json:"a11y"`
}

// Report is the audit's result.
type Report struct {
	// RulesVersion is the [RulesVersion] that produced the report.
	RulesVersion string `json:"rules_version"`
	// Site is the site-wide summary.
	Site SiteSummary `json:"site"`
	// Routes are the judged pages, sorted by route.
	Routes []RouteReport `json:"routes"`
	// Findings are every finding, sorted by route, category, code and ref.
	Findings []Finding `json:"findings"`
}

// SiteSummary is what the audit learned about the whole site.
type SiteSummary struct {
	// URL is Input.Site.
	URL string `json:"url"`
	// Host is the audited host.
	Host string `json:"host"`
	// Score is the mean of the scores of the routes that answered 2xx.
	Score RouteScore `json:"score"`
	// Indexing is the expected and actual indexing of the host.
	Indexing IndexingSummary `json:"indexing"`
	// Robots summarizes robots.txt.
	Robots RobotsSummary `json:"robots"`
	// Sitemap summarizes the sitemaps.
	Sitemap SitemapSummary `json:"sitemap"`
	// NotFound is how an unknown path answered: "not_found" (a real 4xx),
	// "soft_404" (2xx), "error", or "" when it was not probed.
	NotFound string `json:"not_found,omitempty"`
}

// IndexingSummary is the expected and the actual indexing of the host.
type IndexingSummary struct {
	// Expected is "index" or "noindex".
	Expected Indexing `json:"expected"`
	// Actual is "index", "noindex" (the home page says so) or "blocked"
	// (robots.txt disallows "/"); "" when the home page was not judged.
	Actual string `json:"actual,omitempty"`
}

// RobotsSummary summarizes robots.txt.
type RobotsSummary struct {
	// Status is "ok", "missing" (4xx), "unreachable" (5xx or a failed
	// fetch), or "" when it was not fetched.
	Status string `json:"status,omitempty"`
	// Sitemaps are its Sitemap: lines.
	Sitemaps []string `json:"sitemaps,omitempty"`
	// Problems counts its syntax problems.
	Problems int `json:"problems,omitempty"`
	// BlocksRoot reports that it disallows "/" for Googlebot.
	BlocksRoot bool `json:"blocks_root,omitempty"`
}

// SitemapSummary summarizes the sitemaps.
type SitemapSummary struct {
	// Files is how many sitemap files parsed.
	Files int `json:"files"`
	// URLs is how many page URLs they list.
	URLs int `json:"urls"`
	// Broken is how many listed URLs answered 4xx/5xx or are noindex.
	Broken int `json:"broken,omitempty"`
}

// RouteReport is one judged page.
type RouteReport struct {
	// Route is the page's path (and query), e.g. "/blog/".
	Route string `json:"route"`
	// URL is the URL fetched.
	URL string `json:"url"`
	// Status is the HTTP status.
	Status int `json:"status"`
	// Rendered reports that the page's rendered DOM was judged.
	Rendered bool `json:"rendered,omitempty"`
	// Score is the route's scores.
	Score RouteScore `json:"score"`
	// Head is what the page's head says.
	Head Head `json:"head"`
}
