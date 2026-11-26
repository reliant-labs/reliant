package siteaudit

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// SnippetMaxChars bounds an axe node's HTML snippet in a finding (design D6,
// O10): enough to find the element, never a stored copy of the page.
const SnippetMaxChars = 300

// maxNodesPerFinding bounds the nodes one axe violation lists.
const maxNodesPerFinding = 20

// AxeResult is axe-core's run result (`axe.run()`), the parts this package
// reads. It unmarshals from axe's own JSON.
type AxeResult struct {
	Violations   []AxeRule `json:"violations"`
	Passes       []AxeRule `json:"passes"`
	Incomplete   []AxeRule `json:"incomplete"`
	Inapplicable []AxeRule `json:"inapplicable"`
}

// AxeRule is one rule's outcome.
type AxeRule struct {
	ID          string    `json:"id"`
	Impact      string    `json:"impact"`
	Tags        []string  `json:"tags"`
	Description string    `json:"description"`
	Help        string    `json:"help"`
	HelpURL     string    `json:"helpUrl"`
	Nodes       []AxeNode `json:"nodes"`
}

// AxeNode is one element a rule judged.
type AxeNode struct {
	Target         AxeTarget `json:"target"`
	HTML           string    `json:"html"`
	Impact         string    `json:"impact"`
	FailureSummary string    `json:"failureSummary"`
}

// AxeTarget is an element's selector path. axe writes a list of selectors,
// where an entry is itself a list when the element is inside shadow DOM;
// both flatten into one string, frames and shadow roots joined by " >>> ".
type AxeTarget []string

// UnmarshalJSON accepts axe's nested selector arrays.
func (t *AxeTarget) UnmarshalJSON(b []byte) error {
	var raw []any
	if err := json.Unmarshal(b, &raw); err != nil {
		var s string
		if err2 := json.Unmarshal(b, &s); err2 != nil {
			return err
		}
		*t = AxeTarget{s}
		return nil
	}
	out := make(AxeTarget, 0, len(raw))
	for _, e := range raw {
		switch v := e.(type) {
		case string:
			out = append(out, v)
		case []any:
			var parts []string
			for _, p := range v {
				if s, ok := p.(string); ok {
					parts = append(parts, s)
				}
			}
			out = append(out, strings.Join(parts, " >>> "))
		}
	}
	*t = out
	return nil
}

// String is the selector path joined with " >>> ".
func (t AxeTarget) String() string { return strings.Join(t, " >>> ") }

// axeSeverity maps an axe impact to a finding severity: critical breaks the
// page for someone (error), serious should be fixed (warn), moderate and
// minor are advice (info).
func axeSeverity(impact string) Severity {
	switch impact {
	case "critical":
		return SeverityError
	case "serious":
		return SeverityWarn
	default:
		return SeverityInfo
	}
}

// truncate cuts s to at most n characters (runes), marking the cut with "…".
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// axeFindings turns a page's violations into findings.
func axeFindings(route string, res *AxeResult) []Finding {
	var out []Finding
	for _, v := range res.Violations {
		f := Finding{
			Code:     "a11y_violation",
			Severity: axeSeverity(v.Impact),
			Category: CategoryAccessibility,
			Route:    route,
			Ref:      v.ID,
			Evidence: map[string]string{"impact": v.Impact, "help": v.Help},
			Fix:      v.Help,
		}
		if v.HelpURL != "" {
			f.Evidence["help_url"] = v.HelpURL
		}
		if a, ok := a11yAuditByID[v.ID]; ok {
			f.Evidence["group"] = string(a.Group)
		}
		if tags := wcagTags(v.Tags); tags != "" {
			f.Evidence["wcag"] = tags
		}
		if f.Fix == "" {
			f.Fix = v.Description
		}
		for i, n := range v.Nodes {
			if i == maxNodesPerFinding {
				f.Evidence["nodes_omitted"] = itoa(len(v.Nodes) - maxNodesPerFinding)
				break
			}
			f.Nodes = append(f.Nodes, Node{
				Target:  n.Target.String(),
				HTML:    truncate(n.HTML, SnippetMaxChars),
				Summary: truncate(n.FailureSummary, SnippetMaxChars),
			})
		}
		out = append(out, f)
	}
	return out
}

func wcagTags(tags []string) string {
	var out []string
	for _, t := range tags {
		if strings.HasPrefix(t, "wcag") || t == "best-practice" {
			out = append(out, t)
		}
	}
	return strings.Join(out, ",")
}
