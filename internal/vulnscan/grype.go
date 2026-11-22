package vulnscan

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The slice of grype's JSON output (`grype -o json`, v0.122.x) forge reads.
// Every other key is ignored, so a grype that adds fields changes nothing.

type grypeDocument struct {
	Matches    []grypeMatch    `json:"matches"`
	Descriptor grypeDescriptor `json:"descriptor"`
}

type grypeDescriptor struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	DB      struct {
		Status grypeDBStatus `json:"status"`
	} `json:"db"`
	Timestamp string `json:"timestamp"`
}

// grypeDBStatus is both descriptor.db.status in a scan and the whole output
// of `grype db status -o json`.
type grypeDBStatus struct {
	SchemaVersion string `json:"schemaVersion"`
	From          string `json:"from"`
	Built         string `json:"built"`
	Path          string `json:"path"`
	Valid         bool   `json:"valid"`
	Error         string `json:"error"`
}

type grypeMatch struct {
	Vulnerability          grypeVulnerability   `json:"vulnerability"`
	RelatedVulnerabilities []grypeVulnerability `json:"relatedVulnerabilities"`
	MatchDetails           []struct {
		Type string `json:"type"`
	} `json:"matchDetails"`
	Artifact grypeArtifact `json:"artifact"`
}

type grypeVulnerability struct {
	ID         string `json:"id"`
	DataSource string `json:"dataSource"`
	Severity   string `json:"severity"`
	CVSS       []struct {
		Metrics struct {
			BaseScore *float64 `json:"baseScore"`
		} `json:"metrics"`
	} `json:"cvss"`
	EPSS []struct {
		EPSS       float64 `json:"epss"`
		Percentile float64 `json:"percentile"`
	} `json:"epss"`
	KnownExploited []json.RawMessage `json:"knownExploited"`
	Fix            struct {
		Versions []string `json:"versions"`
		State    string   `json:"state"`
	} `json:"fix"`
}

type grypeArtifact struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Type      string `json:"type"`
	PURL      string `json:"purl"`
	Locations []struct {
		Path string `json:"path"`
	} `json:"locations"`
}

// matchTypeStrength ranks grype's match types; an unknown type ranks with a
// CPE match, the weakest.
var matchTypeStrength = map[string]int{
	"exact-direct-match":   3,
	"exact-indirect-match": 2,
	"cpe-match":            1,
}

// ParseGrype reads `grype -o json` output into a Scan. scannedAt is used when
// the output carries no timestamp.
//
// It refuses output whose database block names no build time: a scan that
// cannot say how fresh its data was cannot be judged stale or current, and
// forge never records a verdict it cannot date.
func ParseGrype(data []byte, scannedAt time.Time) (*Scan, error) {
	var doc grypeDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("read grype output: %w", err)
	}
	built, err := parseGrypeTime(doc.Descriptor.DB.Status.Built)
	if err != nil {
		return nil, fmt.Errorf("grype output names no database build time (descriptor.db.status.built %q): %w", doc.Descriptor.DB.Status.Built, err)
	}
	if ts, err := parseGrypeTime(doc.Descriptor.Timestamp); err == nil {
		scannedAt = ts
	}
	s := &Scan{
		Schema:         ScanSchema,
		Scanner:        ScannerGrype,
		ScannerVersion: normalizeVersion(doc.Descriptor.Version),
		DBSchema:       doc.Descriptor.DB.Status.SchemaVersion,
		DBBuiltAt:      built.UTC(),
		ScannedAt:      scannedAt.UTC(),
		Findings:       findingsOf(doc.Matches),
	}
	return s, nil
}

func parseGrypeTime(s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, fmt.Errorf("empty")
	}
	return time.Parse(time.RFC3339Nano, s)
}

// normalizeVersion spells a version with its leading v, the way forge's
// pins name it ("0.122.1" → "v0.122.1").
func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

// findingsOf turns grype matches into findings: one per advisory per
// package. grype reports the same advisory on the same package once per
// matcher that found it; those merge, keeping the strongest match.
func findingsOf(matches []grypeMatch) []Finding {
	byKey := map[string]*Finding{}
	var order []string
	for _, m := range matches {
		f := findingOf(m)
		k := f.Key()
		if prev, ok := byKey[k]; ok {
			mergeFinding(prev, f)
			continue
		}
		byKey[k] = &f
		order = append(order, k)
	}
	out := make([]Finding, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	SortFindings(out)
	return out
}

func findingOf(m grypeMatch) Finding {
	v := m.Vulnerability
	f := Finding{
		ID:       v.ID,
		Severity: ParseSeverity(v.Severity),
		Package: Package{
			Name:    m.Artifact.Name,
			Version: m.Artifact.Version,
			PURL:    m.Artifact.PURL,
			Type:    m.Artifact.Type,
		},
		FixedIn:  nonEmpty(v.Fix.Versions),
		FixState: v.Fix.State,
		URL:      v.DataSource,
	}
	aliases := map[string]bool{}
	all := append([]grypeVulnerability{v}, m.RelatedVulnerabilities...)
	for _, rv := range all {
		if rv.ID != "" && rv.ID != v.ID {
			aliases[rv.ID] = true
		}
		if len(rv.KnownExploited) > 0 {
			f.KEV = true
		}
		for _, c := range rv.CVSS {
			if c.Metrics.BaseScore != nil && (f.CVSS == nil || *c.Metrics.BaseScore > *f.CVSS) {
				score := *c.Metrics.BaseScore
				f.CVSS = &score
			}
		}
		for _, e := range rv.EPSS {
			if f.EPSS == nil || e.EPSS > *f.EPSS {
				score, pct := e.EPSS, e.Percentile
				f.EPSS, f.EPSSPercentile = &score, &pct
			}
		}
	}
	for a := range aliases {
		f.Aliases = append(f.Aliases, a)
	}
	sort.Strings(f.Aliases)
	for _, d := range m.MatchDetails {
		if matchTypeStrength[d.Type] > matchTypeStrength[f.MatchType] || f.MatchType == "" {
			f.MatchType = d.Type
		}
	}
	f.Confidence = ConfidenceHigh
	if matchTypeStrength[f.MatchType] <= matchTypeStrength["cpe-match"] {
		f.Confidence = ConfidenceLow
	}
	for _, l := range m.Artifact.Locations {
		if l.Path != "" {
			f.Locations = appendUnique(f.Locations, l.Path)
		}
	}
	return f
}

// mergeFinding folds a second match of the same advisory and package into
// the first.
func mergeFinding(into *Finding, f Finding) {
	if matchTypeStrength[f.MatchType] > matchTypeStrength[into.MatchType] {
		into.MatchType, into.Confidence = f.MatchType, f.Confidence
	}
	for _, a := range f.Aliases {
		into.Aliases = appendUnique(into.Aliases, a)
	}
	sort.Strings(into.Aliases)
	for _, l := range f.Locations {
		into.Locations = appendUnique(into.Locations, l)
	}
	for _, v := range f.FixedIn {
		into.FixedIn = appendUnique(into.FixedIn, v)
	}
	into.KEV = into.KEV || f.KEV
	if into.FixState != FixStateFixed && f.FixState != "" {
		into.FixState = f.FixState
	}
}

// SortFindings orders findings strongest first: KEV, then severity (unknown
// with critical), then advisory id, then package.
func SortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.KEV != b.KEV {
			return a.KEV
		}
		if ra, rb := a.Severity.gateRank(), b.Severity.gateRank(); ra != rb {
			return ra > rb
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Key() < b.Key()
	})
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = appendUnique(out, s)
		}
	}
	return out
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}
