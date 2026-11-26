package siteaudit

import (
	"math"
	"strconv"
)

// Audit is one scored audit: a Lighthouse audit id and its weight in its
// category. The tables mirror Lighthouse's default config (SEO and
// accessibility categories, as of Lighthouse 12); the scores they produce are
// Lighthouse-compatible, not Lighthouse's. They change only with
// [RulesVersion].
type Audit struct {
	// ID is the Lighthouse audit id; for accessibility, also the axe rule id.
	ID string
	// Weight is the audit's weight in its category.
	Weight float64
	// Group is the Lighthouse group the audit is shown under.
	Group AuditGroup
}

// AuditGroup is a Lighthouse audit group.
type AuditGroup string

// The groups the audits are shown under. The accessibility groups are what
// the console groups issues by.
const (
	GroupSEOCrawl          AuditGroup = "seo-crawl"
	GroupSEOContent        AuditGroup = "seo-content"
	GroupA11yNamesLabels   AuditGroup = "a11y-names-labels"
	GroupA11yColorContrast AuditGroup = "a11y-color-contrast"
	GroupA11yARIA          AuditGroup = "a11y-aria"
	GroupA11yNavigation    AuditGroup = "a11y-navigation"
	GroupA11yTablesLists   AuditGroup = "a11y-tables-lists"
	GroupA11yBestPractices AuditGroup = "a11y-best-practices"
	GroupA11yAudioVideo    AuditGroup = "a11y-audio-video"
	GroupA11yLanguage      AuditGroup = "a11y-language"
)

// SEOAudits is Lighthouse's SEO category. is-crawlable weighs 93/23 so that
// failing it alone fails the category, as in Lighthouse.
var SEOAudits = []Audit{
	{ID: "is-crawlable", Weight: 93.0 / 23.0, Group: GroupSEOCrawl},
	{ID: "document-title", Weight: 1, Group: GroupSEOContent},
	{ID: "meta-description", Weight: 1, Group: GroupSEOContent},
	{ID: "http-status-code", Weight: 1, Group: GroupSEOCrawl},
	{ID: "link-text", Weight: 1, Group: GroupSEOContent},
	{ID: "crawlable-anchors", Weight: 1, Group: GroupSEOCrawl},
	{ID: "robots-txt", Weight: 1, Group: GroupSEOCrawl},
	{ID: "image-alt", Weight: 1, Group: GroupSEOContent},
	{ID: "hreflang", Weight: 1, Group: GroupSEOContent},
	{ID: "canonical", Weight: 1, Group: GroupSEOContent},
}

// A11yAudits is Lighthouse's accessibility category: axe rule ids and their
// weights (critical 10, serious 7, moderate 3, informational 1). Audits
// Lighthouse weighs 0 are left out; their violations are still findings.
var A11yAudits = []Audit{
	{ID: "accesskeys", Weight: 7, Group: GroupA11yNavigation},
	{ID: "aria-allowed-attr", Weight: 10, Group: GroupA11yARIA},
	{ID: "aria-command-name", Weight: 7, Group: GroupA11yARIA},
	{ID: "aria-conditional-attr", Weight: 7, Group: GroupA11yARIA},
	{ID: "aria-deprecated-role", Weight: 1, Group: GroupA11yARIA},
	{ID: "aria-dialog-name", Weight: 7, Group: GroupA11yARIA},
	{ID: "aria-hidden-body", Weight: 10, Group: GroupA11yARIA},
	{ID: "aria-hidden-focus", Weight: 7, Group: GroupA11yARIA},
	{ID: "aria-input-field-name", Weight: 7, Group: GroupA11yARIA},
	{ID: "aria-meter-name", Weight: 7, Group: GroupA11yARIA},
	{ID: "aria-progressbar-name", Weight: 7, Group: GroupA11yARIA},
	{ID: "aria-prohibited-attr", Weight: 7, Group: GroupA11yARIA},
	{ID: "aria-required-attr", Weight: 10, Group: GroupA11yARIA},
	{ID: "aria-required-children", Weight: 10, Group: GroupA11yARIA},
	{ID: "aria-required-parent", Weight: 10, Group: GroupA11yARIA},
	{ID: "aria-roles", Weight: 10, Group: GroupA11yARIA},
	{ID: "aria-text", Weight: 7, Group: GroupA11yARIA},
	{ID: "aria-toggle-field-name", Weight: 7, Group: GroupA11yARIA},
	{ID: "aria-tooltip-name", Weight: 7, Group: GroupA11yARIA},
	{ID: "aria-treeitem-name", Weight: 7, Group: GroupA11yARIA},
	{ID: "aria-valid-attr-value", Weight: 10, Group: GroupA11yARIA},
	{ID: "aria-valid-attr", Weight: 10, Group: GroupA11yARIA},
	{ID: "button-name", Weight: 10, Group: GroupA11yNamesLabels},
	{ID: "bypass", Weight: 7, Group: GroupA11yNavigation},
	{ID: "color-contrast", Weight: 7, Group: GroupA11yColorContrast},
	{ID: "definition-list", Weight: 7, Group: GroupA11yTablesLists},
	{ID: "dlitem", Weight: 7, Group: GroupA11yTablesLists},
	{ID: "document-title", Weight: 7, Group: GroupA11yNamesLabels},
	{ID: "duplicate-id-aria", Weight: 10, Group: GroupA11yARIA},
	{ID: "form-field-multiple-labels", Weight: 3, Group: GroupA11yNamesLabels},
	{ID: "frame-title", Weight: 7, Group: GroupA11yNamesLabels},
	{ID: "heading-order", Weight: 3, Group: GroupA11yNavigation},
	{ID: "html-has-lang", Weight: 7, Group: GroupA11yLanguage},
	{ID: "html-lang-valid", Weight: 7, Group: GroupA11yLanguage},
	{ID: "html-xml-lang-mismatch", Weight: 3, Group: GroupA11yLanguage},
	{ID: "image-alt", Weight: 10, Group: GroupA11yNamesLabels},
	{ID: "input-button-name", Weight: 10, Group: GroupA11yNamesLabels},
	{ID: "input-image-alt", Weight: 10, Group: GroupA11yNamesLabels},
	{ID: "label", Weight: 10, Group: GroupA11yNamesLabels},
	{ID: "link-in-text-block", Weight: 7, Group: GroupA11yColorContrast},
	{ID: "link-name", Weight: 7, Group: GroupA11yNamesLabels},
	{ID: "list", Weight: 7, Group: GroupA11yTablesLists},
	{ID: "listitem", Weight: 7, Group: GroupA11yTablesLists},
	{ID: "meta-refresh", Weight: 10, Group: GroupA11yBestPractices},
	{ID: "meta-viewport", Weight: 10, Group: GroupA11yBestPractices},
	{ID: "object-alt", Weight: 7, Group: GroupA11yNamesLabels},
	{ID: "select-name", Weight: 10, Group: GroupA11yNamesLabels},
	{ID: "skip-link", Weight: 3, Group: GroupA11yNamesLabels},
	{ID: "tabindex", Weight: 7, Group: GroupA11yNavigation},
	{ID: "target-size", Weight: 7, Group: GroupA11yBestPractices},
	{ID: "td-headers-attr", Weight: 7, Group: GroupA11yTablesLists},
	{ID: "th-has-data-cells", Weight: 7, Group: GroupA11yTablesLists},
	{ID: "valid-lang", Weight: 7, Group: GroupA11yLanguage},
	{ID: "video-caption", Weight: 10, Group: GroupA11yAudioVideo},
	{ID: "landmark-one-main", Weight: 3, Group: GroupA11yBestPractices},
	{ID: "autocomplete-valid", Weight: 1, Group: GroupA11yBestPractices},
	{ID: "presentation-role-conflict", Weight: 1, Group: GroupA11yBestPractices},
	{ID: "svg-img-alt", Weight: 1, Group: GroupA11yBestPractices},
}

var a11yAuditByID = func() map[string]Audit {
	m := make(map[string]Audit, len(A11yAudits))
	for _, a := range A11yAudits {
		m[a.ID] = a
	}
	return m
}()

// auditOutcome is one audit's result on one page.
type auditOutcome int

const (
	auditNotApplicable auditOutcome = iota
	auditPass
	auditFail
)

// weighted scores outcomes against a table: the weighted share of applicable
// audits that passed, 0-100, rounded; nil when none applied.
func weighted(table []Audit, outcomes map[string]auditOutcome) *int {
	var total, passed float64
	for _, a := range table {
		switch outcomes[a.ID] {
		case auditPass:
			total += a.Weight
			passed += a.Weight
		case auditFail:
			total += a.Weight
		}
	}
	if total == 0 {
		return nil
	}
	s := int(math.Round(100 * passed / total))
	return &s
}

// a11yScore scores a page's axe result: each weighted rule that passed or
// was violated counts; inapplicable and incomplete rules do not.
func a11yScore(res *AxeResult) *int {
	if res == nil {
		return nil
	}
	out := map[string]auditOutcome{}
	for _, r := range res.Passes {
		if _, ok := out[r.ID]; !ok {
			out[r.ID] = auditPass
		}
	}
	for _, r := range res.Violations {
		out[r.ID] = auditFail
	}
	return weighted(A11yAudits, out)
}

// meanScore is the rounded mean of the non-nil scores; nil when there are none.
func meanScore(scores []*int) *int {
	var sum, n int
	for _, s := range scores {
		if s != nil {
			sum += *s
			n++
		}
	}
	if n == 0 {
		return nil
	}
	m := int(math.Round(float64(sum) / float64(n)))
	return &m
}

func itoa(n int) string { return strconv.Itoa(n) }
