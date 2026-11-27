// Package siteaudit is the rule set that judges a static site's SEO and
// accessibility: given the pages a crawler fetched (raw HTML, status,
// headers, and optionally the rendered DOM and an axe-core result), the
// site's robots.txt and sitemaps, the status of the links it checked, and
// what the site is expected to be (indexed or not, which host is canonical),
// it produces findings and a Lighthouse-compatible score per route.
//
// It is pure: no I/O, no clock, no control-plane types. Callers fetch the
// facts, this package decides. A sitemap served gzipped is decompressed by
// the caller; an og:image is fetched by the caller, which reports whether it
// is forge's scaffolded placeholder with [IsPlaceholderImage].
//
// ONE DEFINITION, THREE CALLERS. The control plane's site auditor crawls each
// hosted static site after a promotion and runs [Evaluate]; `forge site
// audit` runs [Evaluate] over a local build or a URL; `forge lint --seo` runs
// [EvaluateRaw] over built HTML files. Release health compares two [Report]s
// of the same env (the promotion's and the previous release's) on their route
// scores and error findings. So every type and function a caller needs is
// exported on purpose: [Evaluate], [EvaluateRaw], the parsers the crawler
// shares ([ParseRobots], [ParseSitemap], [DiscoverLinks], [ExtractHead]), the
// rule catalog ([Catalog]) and the scoring tables ([SEOAudits],
// [A11yAudits]). Keep them exported.
//
// THE RULES (Site insights design, card F1).
//
//   - Which DOM a rule judges (D1): the social-card rules (og:*, twitter:*)
//     judge the RAW HTML only, because Slack, X, LinkedIn and Facebook do not
//     run JavaScript. Everything else judges the rendered DOM when the page
//     has one (Google renders), else the raw HTML; and a title, description
//     or canonical present only in the rendered DOM is a
//     head_client_rendered warning, because most crawlers other than Google
//     do not render.
//   - Indexing (D5): Expect.Indexing is what the host should be. A page that
//     should be indexed but sends noindex (meta robots or X-Robots-Tag), or a
//     robots.txt that disallows "/", is an indexing_mismatch error. A host
//     expected to be noindex (a preview) that is indexable is a platform
//     defect, reported as preview_indexable (info) and never scored against
//     the site; noindex by design is not penalised. An indexable host that is
//     not the canonical host, whose pages carry no canonical to another host,
//     is duplicate_without_canonical.
//   - Severity: error is what breaks indexing or a page (title missing,
//     noindex where indexing is expected, a canonical to a foreign host, an
//     internal link or page that 4xx/5xxs, robots.txt that 5xxs, a critical
//     axe violation); release health reads new error findings as a
//     regression. warn is what a site should fix; info is advice.
//   - Scoring: per route, Lighthouse-compatible (not Lighthouse). SEO is the
//     weighted share of Lighthouse's SEO audits the page passes, each judged
//     by these rules; accessibility is the weighted share of Lighthouse's
//     accessibility audits (axe rule ids) axe found applicable that passed.
//     Not-applicable audits leave the denominator, as in Lighthouse. The
//     weight tables are versioned with [RulesVersion]: two scores are
//     comparable only under the same version.
//   - Output is deterministic: routes sort by route, findings by route,
//     category, code and ref, so two reports of the same facts are equal.
package siteaudit
