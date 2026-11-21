package vulnscan

import (
	"strings"
	"time"
)

// Severity is a finding's severity, lower-cased from the scanner's label.
type Severity string

// The severities, strongest first. Negligible is grype's band below low (a
// distro's "unimportant"); Unknown is a finding the database gives no
// severity, which the gate counts as critical (fail closed).
const (
	SeverityCritical   Severity = "critical"
	SeverityHigh       Severity = "high"
	SeverityMedium     Severity = "medium"
	SeverityLow        Severity = "low"
	SeverityNegligible Severity = "negligible"
	SeverityUnknown    Severity = "unknown"
)

// severityRank orders severities and the policy's thresholds on one scale.
// "none" is the policy's "this band is off"; it ranks below everything, so
// a threshold of none is checked separately rather than matched.
var severityRank = map[string]int{
	"none":                     0,
	string(SeverityNegligible): 1,
	string(SeverityLow):        2,
	string(SeverityMedium):     3,
	string(SeverityHigh):       4,
	string(SeverityCritical):   5,
}

// ParseSeverity normalizes a scanner's severity label. Anything it does not
// recognise, including the empty string, is SeverityUnknown.
func ParseSeverity(s string) Severity {
	switch v := Severity(strings.ToLower(strings.TrimSpace(s))); v {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityNegligible:
		return v
	default:
		return SeverityUnknown
	}
}

// gateRank is the rank the gate judges a finding at: an unknown severity is
// critical, following identityadvisory's fail-closed rule.
func (s Severity) gateRank() int {
	if r, ok := severityRank[string(s)]; ok {
		return r
	}
	return severityRank[string(SeverityCritical)]
}

// AtLeast reports whether s is at or above min ("low", "high", ...), judged
// the way the gate judges it (unknown is critical).
func (s Severity) AtLeast(min string) bool {
	r, ok := severityRank[strings.ToLower(min)]
	if !ok || r == 0 {
		return true
	}
	return s.gateRank() >= r
}

// Fix states, as grype reports them. Only FixStateFixed counts as fixable:
// "not-fixed" means the vendor has not shipped one, "wont-fix" that they
// decided not to.
const (
	FixStateFixed    = "fixed"
	FixStateNotFixed = "not-fixed"
	FixStateWontFix  = "wont-fix"
)

// Match confidence. A CPE match (a name guessed into a vendor/product pair)
// caused every likely false positive in the phase-0 samples; an exact match
// against an ecosystem's own advisory data caused none.
const (
	ConfidenceHigh = "high"
	ConfidenceLow  = "low"
)

// Package is the component a finding is in.
type Package struct {
	// Name and Version as the SBOM names them.
	Name    string `json:"name"`
	Version string `json:"version"`
	// PURL is the package URL, when the SBOM gives one.
	PURL string `json:"purl,omitempty"`
	// Type is the ecosystem: go-module, npm, apk, deb, python, binary, ...
	Type string `json:"type,omitempty"`
}

// Finding is one advisory matched against one package.
type Finding struct {
	// ID is the advisory the scanner matched (CVE-…, GHSA-…, GO-…).
	ID string `json:"id"`
	// Aliases are the other identifiers of the same advisory (the CVE of a
	// GHSA). An exemption of any of them covers the finding.
	Aliases []string `json:"aliases,omitempty"`
	// Severity is the scanner's label, normalized.
	Severity Severity `json:"severity"`
	// CVSS is the highest CVSS base score the database records for the
	// advisory, absent when it records none.
	CVSS *float64 `json:"cvss,omitempty"`
	// KEV reports that the advisory is on CISA's Known Exploited
	// Vulnerabilities list.
	KEV bool `json:"kev"`
	// EPSS is the probability of exploitation in the next 30 days (0..1),
	// and EPSSPercentile its percentile, when the database has a score.
	EPSS           *float64 `json:"epss,omitempty"`
	EPSSPercentile *float64 `json:"epss_percentile,omitempty"`
	// Package is the component the advisory matched.
	Package Package `json:"package"`
	// FixedIn are the versions that fix it, when a fix exists.
	FixedIn []string `json:"fixed_in,omitempty"`
	// FixState is fixed, not-fixed, wont-fix, or "" when unknown.
	FixState string `json:"fix_state,omitempty"`
	// MatchType is the strongest way the scanner matched it:
	// exact-direct-match, exact-indirect-match or cpe-match.
	MatchType string `json:"match_type,omitempty"`
	// Confidence is high for an exact match and low for a CPE-only one.
	Confidence string `json:"confidence,omitempty"`
	// Locations are where in the scanned artifact the package was found.
	Locations []string `json:"locations,omitempty"`
	// URL is the advisory's page in the source the match came from.
	URL string `json:"url,omitempty"`
}

// Fixable reports whether a fixed version exists.
func (f Finding) Fixable() bool { return f.FixState == FixStateFixed }

// Key identifies a finding across scans and artifacts: the advisory and the
// exact package it matched.
func (f Finding) Key() string {
	pkg := f.Package.PURL
	if pkg == "" {
		pkg = f.Package.Type + ":" + f.Package.Name + "@" + f.Package.Version
	}
	return f.ID + "\x00" + pkg
}

// Names reports whether id is the finding's ID or one of its aliases,
// compared case-insensitively.
func (f Finding) Names(id string) bool {
	if strings.EqualFold(f.ID, id) {
		return true
	}
	for _, a := range f.Aliases {
		if strings.EqualFold(a, id) {
			return true
		}
	}
	return false
}

// ScanSchema names the body of a stored scan (ledgerfile.FindingsRecord.Body).
const ScanSchema = "forge.dev/vuln-scan/v1"

// ScannerGrype is the scanner every Scan names today.
const ScannerGrype = "grype"

// Scan is one scan of one source against one build of the database: what it
// found, and how fresh that answer is.
type Scan struct {
	// Schema is ScanSchema, so a stored body says what it is.
	Schema string `json:"schema"`
	// Scanner and ScannerVersion name the tool ("grype", "v0.122.1").
	Scanner        string `json:"scanner"`
	ScannerVersion string `json:"scanner_version"`
	// DBSchema is the database's schema version ("v6.1.10"), and DBBuiltAt
	// when that build was made: the freshness of every finding below.
	DBSchema  string    `json:"db_schema"`
	DBBuiltAt time.Time `json:"db_built_at"`
	// ScannedAt is when the scan ran.
	ScannedAt time.Time `json:"scanned_at"`
	// Findings is what it found, sorted by severity then id. Never nil: an
	// empty list is a clean scan.
	Findings []Finding `json:"findings"`
}
