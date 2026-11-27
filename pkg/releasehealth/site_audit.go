package releasehealth

// SITE AUDITS (Site insights, design D2). A hosted static frontend is audited
// for search and accessibility after each promotion (pkg/siteaudit, run by
// the control plane's site auditor). The audit is about the release's own
// bytes as served, not about a traffic window, so it is judged on its own
// terms: against the same env's audit of the PREVIOUS release, so a site that
// was always imperfect is never blamed for it.
//
//   - seo_regressed: the site's SEO score, or one route's, dropped by more
//     than seo_max_score_drop points (default 10), or an error-severity SEO
//     finding appeared that the previous audit did not have (the env became
//     noindex, robots.txt disallows /, a canonical names another host, an
//     internal link answers 404).
//   - a11y_regressed: the same for the accessibility score
//     (a11y_max_score_drop) and a new critical axe violation.
//
// A finding is new when no finding of the previous audit has its code, route
// and ref (siteaudit.Finding identifies a finding by the three), and the
// previous audit judged its category at its scope: a route that axe did not
// run on before has no a11y baseline, so its violations are not new. A
// site-wide finding, or one on a route the previous audit did not crawl (a
// page the release added, whose error is the release's doing), needs the
// previous audit to have judged the category somewhere: its site-level score
// for it. An audit that ran no axe at all is no a11y baseline. Each new error
// carries the scope and score it was judged against (BaselineScope,
// BaselineScore), and Evaluate holds it to that. A score drop is compared
// only where the previous audit has a score for that category: site-wide, or
// on that route.
//
// Each frontend yields at most two findings per code: one for the score drop
// (Ref SiteAuditRefScore; Value the largest drop in points, Threshold the
// limit) and one for the new errors (Ref SiteAuditRefNewErrors; Value their
// count, Threshold 1). Both warn unless fail_on lists the code; a warn never
// changes the verdict.
//
// No previous audit (the env's first audited release, or the previous one's
// audit never finished) is no comparison: no finding. An audit of the judged
// release that has not finished, failed or is missing is not evaluated: the
// report's not_evaluated names site_audit, and the verdict is unchanged, so a
// static site's INCONCLUSIVE stays about its traffic. Two audits of different
// siteaudit.RulesVersion are not compared either: a rule change is not the
// release's.

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/siteaudit"
)

// NotEvaluatedSiteAudit is the not_evaluated entry of a report with a site
// audit that has not finished, failed or is missing.
const NotEvaluatedSiteAudit = "site_audit"

// Site-audit states ([SiteAuditFacts.State]).
const (
	// SiteAuditDone: the audit finished and its report was read.
	SiteAuditDone = "done"
	// SiteAuditPending: the audit is queued or running.
	SiteAuditPending = "pending"
	// SiteAuditFailed: the audit ran and could not finish.
	SiteAuditFailed = "failed"
	// SiteAuditMissing: the release has no audit (none was queued, or it
	// was skipped).
	SiteAuditMissing = "missing"
)

// The Refs of the site-audit findings.
const (
	// SiteAuditRefScore is the Ref of a finding about a score drop.
	SiteAuditRefScore = "score"
	// SiteAuditRefNewErrors is the Ref of a finding about new error-severity
	// audit findings.
	SiteAuditRefNewErrors = "new_errors"
)

// siteAuditListMax is how many routes or new errors one finding's message
// names before "and N more".
const siteAuditListMax = 5

// SiteAuditFacts are one static frontend's site audit of the judged release,
// with the same env's audit of the previous release. [CompareSiteAudits]
// builds them from the two reports.
type SiteAuditFacts struct {
	// Frontend is the static frontend audited; its findings' Service.
	Frontend string `json:"frontend"`
	// Release is the release the audit audited; "" when unknown. An audit of
	// a release other than the report's subject is not evaluated.
	Release string `json:"release,omitempty"`
	// State is SiteAuditDone, SiteAuditPending, SiteAuditFailed or
	// SiteAuditMissing; any state but done is not evaluated.
	State string `json:"state"`
	// Reason says why an audit that is not done was not evaluated, when
	// CompareSiteAudits knows (a report of another release than the one
	// named).
	Reason string `json:"reason,omitempty"`
	// RulesVersion is siteaudit.RulesVersion of the audit.
	RulesVersion string `json:"rules_version,omitempty"`
	// SEO and A11y are the site-wide scores (0-100); nil was not evaluated.
	SEO  *int `json:"seo,omitempty"`
	A11y *int `json:"a11y,omitempty"`
	// Routes are the scores of each audited route, beside the previous
	// audit's for the same route.
	Routes []SiteAuditRoute `json:"routes,omitempty"`
	// Previous is the same env's audit of the previous release; nil when
	// there is none to compare with, which yields no finding.
	Previous *SiteAuditBaseline `json:"previous,omitempty"`
	// NewErrors are the error-severity audit findings the previous audit
	// did not have.
	NewErrors []SiteAuditFinding `json:"new_errors,omitempty"`
}

// SiteAuditRoute is one route's scores in both audits; nil was not evaluated
// (or, for the previous scores, not crawled).
type SiteAuditRoute struct {
	Route        string `json:"route"`
	SEO          *int   `json:"seo,omitempty"`
	A11y         *int   `json:"a11y,omitempty"`
	PreviousSEO  *int   `json:"previous_seo,omitempty"`
	PreviousA11y *int   `json:"previous_a11y,omitempty"`
}

// SiteAuditBaseline is the audit a release's is compared with. A nil score
// says the previous audit judged that category on no page: there is no
// baseline for it, so neither a score drop nor a new error of that category
// is a finding, whatever NewErrors holds.
type SiteAuditBaseline struct {
	// Release is the release it audited.
	Release string `json:"release,omitempty"`
	// RulesVersion is siteaudit.RulesVersion of that audit.
	RulesVersion string `json:"rules_version,omitempty"`
	// SEO and A11y are its site-wide scores.
	SEO  *int `json:"seo,omitempty"`
	A11y *int `json:"a11y,omitempty"`
}

// SiteAuditFinding names one audit finding: siteaudit.Finding's identity
// (Code, Route, Ref) and its category.
type SiteAuditFinding struct {
	Code     string             `json:"code"`
	Category siteaudit.Category `json:"category"`
	// Route is "" for a site-wide finding.
	Route string `json:"route,omitempty"`
	Ref   string `json:"ref,omitempty"`
	// BaselineScope is where the previous audit judged the finding's
	// category, which is what makes its absence there mean something:
	// SiteAuditScopeRoute (its route's score) or SiteAuditScopeSite (the
	// site-level score: a site-wide finding, or a route the previous audit
	// did not crawl). BaselineScore is the previous score for the category
	// at that scope. A new error with no BaselineScore has no baseline and
	// is never a finding.
	BaselineScope string `json:"baseline_scope,omitempty"`
	BaselineScore *int   `json:"baseline_score,omitempty"`
}

// Where a new error's baseline was judged ([SiteAuditFinding.BaselineScope]).
const (
	SiteAuditScopeRoute = "route"
	SiteAuditScopeSite  = "site"
)

// CompareSiteAudits builds the facts of frontend's audit of release
// (current) against the same env's audit of previousRelease (previous). A
// nil current is SiteAuditMissing (set State to SiteAuditPending or
// SiteAuditFailed when that is what the caller knows); a nil previous is no
// baseline, so the facts can yield no finding. Two audits of different rule
// versions are not compared: the previous one is named, its scores are not
// joined and no error is new.
//
// A report that states its release (siteaudit.Report.Release) must be of
// the release it is passed as: current of release, previous of
// previousRelease. A mismatched pair is never compared, since it could
// invent a regression or hide one: the facts are SiteAuditMissing (not
// evaluated), with Reason naming the mismatch.
func CompareSiteAudits(frontend, release string, current *siteaudit.Report, previousRelease string, previous *siteaudit.Report) SiteAuditFacts {
	f := SiteAuditFacts{Frontend: frontend, Release: release, State: SiteAuditMissing}
	if current == nil {
		return f
	}
	if current.Release != "" && current.Release != release {
		f.Reason = fmt.Sprintf("the audit passed as %s's is of release %s", nameOr(release, "the judged release"), current.Release)
		return f
	}
	if previous != nil && previous.Release != "" && previous.Release != previousRelease {
		f.Reason = fmt.Sprintf("the audit passed as the previous release %s's is of release %s", nameOr(previousRelease, "(unnamed)"), previous.Release)
		return f
	}
	f.State, f.RulesVersion = SiteAuditDone, current.RulesVersion
	f.SEO, f.A11y = cloneInt(current.Site.Score.SEO), cloneInt(current.Site.Score.A11y)
	prevRoutes := map[string]siteaudit.RouteScore{}
	comparable := previous != nil && previous.RulesVersion == current.RulesVersion
	if previous != nil {
		f.Previous = &SiteAuditBaseline{Release: previousRelease, RulesVersion: previous.RulesVersion,
			SEO: cloneInt(previous.Site.Score.SEO), A11y: cloneInt(previous.Site.Score.A11y)}
		if comparable {
			for _, r := range previous.Routes {
				prevRoutes[r.Route] = r.Score
			}
		}
	}
	for _, r := range current.Routes {
		sr := SiteAuditRoute{Route: r.Route, SEO: cloneInt(r.Score.SEO), A11y: cloneInt(r.Score.A11y)}
		if p, ok := prevRoutes[r.Route]; ok {
			sr.PreviousSEO, sr.PreviousA11y = cloneInt(p.SEO), cloneInt(p.A11y)
		}
		f.Routes = append(f.Routes, sr)
	}
	if !comparable {
		return f
	}
	type key struct{ code, route, ref string }
	had := map[key]bool{}
	for _, pf := range previous.Findings {
		had[key{pf.Code, pf.Route, pf.Ref}] = true
	}
	for _, cf := range current.Findings {
		if cf.Severity != siteaudit.SeverityError || had[key{cf.Code, cf.Route, cf.Ref}] {
			continue
		}
		scope, base := baselineOf(cf, previous, prevRoutes)
		if base == nil {
			continue
		}
		f.NewErrors = append(f.NewErrors, SiteAuditFinding{Code: cf.Code, Category: cf.Category, Route: cf.Route, Ref: cf.Ref,
			BaselineScope: scope, BaselineScore: cloneInt(base)})
	}
	return f
}

// baselineOf is the scope and the score at which the previous audit judged
// f's category, so that the finding's absence from it means something:
//
//   - a finding on a route the previous audit crawled: that route's score
//     for the category (axe may not have run on it);
//   - a site-wide finding, or one on a route the release added: the
//     previous audit's site-level score for the category, which is nil when
//     it judged the category on no page at all.
//
// A nil score at that scope is no baseline, and the finding is not new: an
// audit that never ran axe cannot say a violation was absent. A category
// this package does not know has no baseline either.
func baselineOf(f siteaudit.Finding, previous *siteaudit.Report, prevRoutes map[string]siteaudit.RouteScore) (string, *int) {
	if f.Route != "" {
		if s, ok := prevRoutes[f.Route]; ok {
			return SiteAuditScopeRoute, categoryScore(s.SEO, s.A11y, f.Category)
		}
	}
	return SiteAuditScopeSite, categoryScore(previous.Site.Score.SEO, previous.Site.Score.A11y, f.Category)
}

// categoryScore is the score of category among seo and a11y; nil for a
// category that has none.
func categoryScore(seo, a11y *int, category siteaudit.Category) *int {
	switch category {
	case siteaudit.CategorySEO:
		return seo
	case siteaudit.CategoryAccessibility:
		return a11y
	}
	return nil
}

func cloneInt(p *int) *int {
	if p == nil {
		return nil
	}
	n := *p
	return &n
}

// siteAuditCategory is one category's figures in a SiteAuditFacts.
type siteAuditCategory struct {
	code     string
	label    string // "SEO", "accessibility"
	category siteaudit.Category
	limit    int
	score    func(SiteAuditRoute) (cur, prev *int)
	site     func(SiteAuditFacts) (cur, prev *int)
}

func (t Thresholds) siteAuditCategories() []siteAuditCategory {
	return []siteAuditCategory{
		{code: release.HealthSEORegressed, label: "SEO", category: siteaudit.CategorySEO, limit: t.SEOMaxScoreDrop,
			score: func(r SiteAuditRoute) (*int, *int) { return r.SEO, r.PreviousSEO },
			site: func(a SiteAuditFacts) (*int, *int) {
				if a.Previous == nil {
					return a.SEO, nil
				}
				return a.SEO, a.Previous.SEO
			}},
		{code: release.HealthA11yRegressed, label: "accessibility", category: siteaudit.CategoryAccessibility, limit: t.A11yMaxScoreDrop,
			score: func(r SiteAuditRoute) (*int, *int) { return r.A11y, r.PreviousA11y },
			site: func(a SiteAuditFacts) (*int, *int) {
				if a.Previous == nil {
					return a.A11y, nil
				}
				return a.A11y, a.Previous.A11y
			}},
	}
}

// siteAuditFindings judges audits for the judged subject s: the findings (no
// policy yet), and whether any audit was not evaluated.
func siteAuditFindings(audits []SiteAuditFacts, t Thresholds, s Subject) (out []Finding, notEvaluated bool) {
	for _, a := range audits {
		if a.State != SiteAuditDone || (s.Release != "" && a.Release != "" && a.Release != s.Release) {
			notEvaluated = true
			continue
		}
		p := a.Previous
		if p == nil || (p.RulesVersion != "" && a.RulesVersion != "" && p.RulesVersion != a.RulesVersion) {
			continue // nothing to compare with
		}
		since := " since " + nameOr(p.Release, "the previous release")
		for _, c := range t.siteAuditCategories() {
			if f, ok := c.scoreFinding(a, since); ok {
				out = append(out, f)
			}
			if f, ok := c.newErrorsFinding(a, since); ok {
				out = append(out, f)
			}
		}
	}
	return out, notEvaluated
}

type scoreDrop struct {
	where     string
	cur, prev int
}

func (c siteAuditCategory) scoreFinding(a SiteAuditFacts, since string) (Finding, bool) {
	var drops []scoreDrop
	if cur, prev := c.site(a); cur != nil && prev != nil && *prev-*cur > c.limit {
		drops = append(drops, scoreDrop{"site", *cur, *prev})
	}
	var routes []scoreDrop
	for _, r := range a.Routes {
		if cur, prev := c.score(r); cur != nil && prev != nil && *prev-*cur > c.limit {
			routes = append(routes, scoreDrop{r.Route, *cur, *prev})
		}
	}
	// The largest drops first; a route's name breaks a tie.
	sort.SliceStable(routes, func(i, j int) bool {
		if di, dj := routes[i].prev-routes[i].cur, routes[j].prev-routes[j].cur; di != dj {
			return di > dj
		}
		return routes[i].where < routes[j].where
	})
	drops = append(drops, routes...)
	if len(drops) == 0 {
		return Finding{}, false
	}
	worst := 0
	parts := make([]string, 0, siteAuditListMax+1)
	for i, d := range drops {
		worst = max(worst, d.prev-d.cur)
		if i < siteAuditListMax {
			parts = append(parts, fmt.Sprintf("%s %d -> %d (-%d)", d.where, d.prev, d.cur, d.prev-d.cur))
		}
	}
	if n := len(drops) - siteAuditListMax; n > 0 {
		parts = append(parts, fmt.Sprintf("and %d more", n))
	}
	return Finding{Code: c.code, Service: a.Frontend, Ref: SiteAuditRefScore, Value: float64(worst), Threshold: float64(c.limit),
		Message: fmt.Sprintf("%s: %s score dropped%s: %s (limit %d points)", a.Frontend, c.label, since, strings.Join(parts, ", "), c.limit)}, true
}

func (c siteAuditCategory) newErrorsFinding(a SiteAuditFacts, since string) (Finding, bool) {
	// Each error is held to its own baseline (its category at its scope,
	// recorded by CompareSiteAudits): one without is not new, even in facts
	// a caller built by hand.
	var errs []SiteAuditFinding
	for _, e := range a.NewErrors {
		if e.Category == c.category && e.BaselineScore != nil {
			errs = append(errs, e)
		}
	}
	if len(errs) == 0 {
		return Finding{}, false
	}
	slices.SortStableFunc(errs, func(x, y SiteAuditFinding) int {
		return strings.Compare(x.Route+"\x00"+x.Code+"\x00"+x.Ref, y.Route+"\x00"+y.Code+"\x00"+y.Ref)
	})
	kind := "error"
	if c.category == siteaudit.CategoryAccessibility {
		kind = "critical violation"
	}
	if len(errs) > 1 {
		kind += "s"
	}
	parts := make([]string, 0, siteAuditListMax+1)
	for i, e := range errs {
		if i == siteAuditListMax {
			parts = append(parts, fmt.Sprintf("and %d more", len(errs)-siteAuditListMax))
			break
		}
		what := e.Code
		if e.Ref != "" {
			what += " " + e.Ref
		}
		parts = append(parts, what+" on "+nameOr(e.Route, "the whole site"))
	}
	return Finding{Code: c.code, Service: a.Frontend, Ref: SiteAuditRefNewErrors, Value: float64(len(errs)), Threshold: 1,
		Message: fmt.Sprintf("%s: %d new %s %s%s: %s", a.Frontend, len(errs), c.label, kind, since, strings.Join(parts, "; "))}, true
}
