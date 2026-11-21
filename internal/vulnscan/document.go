package vulnscan

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// DocumentSchema names the vuln-list document and its major version. Field
// names under it are stable: a change that would break a v1 reader is a v2,
// never an edit of v1.
const DocumentSchema = "forge.dev/vuln-list/v1"

// Document is `forge vuln list --json` (and `show`, `scan`): what a release's
// artifacts are known to be vulnerable to, and what the env's vuln_gate makes
// of it. Its JSON Schema is `forge project schema vuln-list`.
type Document struct {
	// Schema is DocumentSchema.
	Schema string `json:"schema"`
	// Env is the env whose policy judged the findings; "" for a scan that
	// named no env (it is judged by the default policy).
	Env string `json:"env"`
	// Release is the release scanned; "" for a directory or image scan.
	Release string `json:"release"`
	// Coverage says how much of the release was scanned.
	Coverage Coverage `json:"coverage"`
	// DB says which scanner and database build the findings came from.
	DB DocumentDB `json:"db"`
	// Gate is the env's vuln_gate verdict over every finding, whatever the
	// filters: a filter narrows what is listed, never what is judged.
	Gate GateResult `json:"gate"`
	// Counts are the listed findings by severity (negligible counts as low).
	Counts Counts `json:"counts"`
	// Findings are the listed findings, strongest first.
	Findings []DocumentFinding `json:"findings"`
	// Notes say what the reader should know about the data: why nothing
	// could be rescanned (no scanner, no database, a stale database), and
	// why the verdict is undetermined.
	Notes []string `json:"notes,omitempty"`
}

// DocumentDB is the freshness of a document's findings.
type DocumentDB struct {
	// Scanner and Version name the tool ("grype", "v0.122.1").
	Scanner string `json:"scanner"`
	Version string `json:"version"`
	// Schema is the database schema ("v6.1.10").
	Schema string `json:"schema,omitempty"`
	// BuiltAt is the build of the OLDEST database any listed artifact was
	// scanned with: the document is only as fresh as that.
	BuiltAt time.Time `json:"built_at,omitzero"`
	// Stale reports that BuiltAt is older than five days (StaleAfter): the
	// verdict is then "stale", exit 2, whatever the findings say.
	Stale bool `json:"stale"`
}

// Counts are findings by severity.
type Counts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
}

func (c *Counts) add(s Severity) {
	switch s {
	case SeverityCritical:
		c.Critical++
	case SeverityHigh:
		c.High++
	case SeverityMedium:
		c.Medium++
	case SeverityLow, SeverityNegligible:
		c.Low++
	default:
		c.Unknown++
	}
}

// Total is every counted finding.
func (c Counts) Total() int { return c.Critical + c.High + c.Medium + c.Low + c.Unknown }

// DocumentFinding is one advisory on one package, across the release's
// artifacts.
type DocumentFinding struct {
	Finding
	// Artifacts are the release artifacts the package is in.
	Artifacts []string `json:"artifacts"`
	// FirstSeen is the first stored scan that reported it.
	FirstSeen time.Time `json:"first_seen,omitzero"`
	// Exemption is the env's exemption that covers it, when one does.
	Exemption *Exemption `json:"exemption,omitempty"`
	// Action is what the vuln_gate rule makes of it: block, warn or none.
	Action Action `json:"action"`
}

// ArtifactScan is one artifact of a release and what is known of it.
type ArtifactScan struct {
	// Artifact is the release's name for it.
	Artifact string
	// SBOMDigest is its stored SBOM, "" when it has none.
	SBOMDigest string
	// Latest is its newest scan; nil when it has no SBOM or no scan, and
	// Reason says which.
	Latest *Scan
	Reason string
	// History is every stored scan of its SBOM, oldest first: where
	// FirstSeen comes from. Latest alone is enough without it.
	History []*Scan
}

// Filter narrows the listed findings. The gate always judges all of them.
type Filter struct {
	// MinSeverity lists findings at or above it ("" or "none": all).
	MinSeverity string
	// Fixable lists only findings with a fixed version.
	Fixable bool
	// KEV lists only KEV-listed findings.
	KEV bool
	// IncludeExempt lists findings an exemption covers too.
	IncludeExempt bool
	// Match lists only findings whose id or alias is Match, or whose
	// package name or purl is Match (`forge vuln show`).
	Match string
}

func (f Filter) keep(d DocumentFinding) bool {
	if d.Exemption != nil && !f.IncludeExempt {
		return false
	}
	if f.MinSeverity != "" && f.MinSeverity != "none" && !d.Severity.AtLeast(f.MinSeverity) {
		return false
	}
	if f.Fixable && !d.Fixable() {
		return false
	}
	if f.KEV && !d.KEV {
		return false
	}
	if m := strings.TrimSpace(f.Match); m != "" {
		if !d.Names(m) && !strings.EqualFold(d.Package.Name, m) && !strings.EqualFold(d.Package.PURL, m) {
			return false
		}
	}
	return true
}

// DocumentInput is what BuildDocument assembles.
type DocumentInput struct {
	Env     string
	Release string
	// Gate is the env's vuln_gate, resolved.
	Gate release.ShapeVulnGate
	// GradedAs is the mode the rollout grades Gate.Mode as; "" is the
	// declared mode (EvaluateInput.GradedAs).
	GradedAs  release.DeliveryMode
	Artifacts []ArtifactScan
	Filter    Filter
	Now       time.Time
	// Notes are carried into the document (Document.Notes).
	Notes []string
}

// ScanIsCurrent reports whether s can vouch for an artifact at now: its
// database was built within StaleAfter. This is the freshness rule: a
// verdict is determined only if every scanned artifact's NEWEST stored scan
// is current by its own db_built_at. A stored scan that is current counts
// even when no scanner or database is on this machine now: it IS a scan with
// a fresh database. One that is not current makes the verdict "stale" (exit
// 2) unless a rescan replaced it, whatever it found.
func ScanIsCurrent(s *Scan, now time.Time) bool {
	return s != nil && now.Sub(s.DBBuiltAt) <= StaleAfter
}

// BuildDocument merges a release's per-artifact scans into one document:
// each advisory-on-package once, with every artifact it is in; the gate's
// verdict over all of them; then the filter.
func BuildDocument(in DocumentInput) Document {
	doc := Document{Schema: DocumentSchema, Env: in.Env, Release: in.Release, Findings: []DocumentFinding{}, Notes: append([]string(nil), in.Notes...)}
	doc.Coverage.Artifacts = len(in.Artifacts)
	byKey := map[string]*DocumentFinding{}
	var order []string
	var oldest *Scan
	for _, a := range in.Artifacts {
		if a.SBOMDigest != "" {
			doc.Coverage.WithSBOM++
		}
		if a.Latest == nil {
			reason := a.Reason
			if reason == "" {
				reason = "not scanned"
			}
			doc.Coverage.Unscanned = append(doc.Coverage.Unscanned, UnscannedArtifact{Artifact: a.Artifact, Reason: reason})
			continue
		}
		doc.Coverage.Scanned++
		if oldest == nil || a.Latest.DBBuiltAt.Before(oldest.DBBuiltAt) {
			oldest = a.Latest
		}
		firstSeen := firstSeenIn(a.History)
		for _, f := range a.Latest.Findings {
			k := f.Key()
			seen := firstSeen[k]
			if seen.IsZero() {
				seen = a.Latest.ScannedAt
			}
			if d, ok := byKey[k]; ok {
				d.Artifacts = appendUnique(d.Artifacts, a.Artifact)
				if seen.Before(d.FirstSeen) {
					d.FirstSeen = seen
				}
				continue
			}
			byKey[k] = &DocumentFinding{Finding: f, Artifacts: []string{a.Artifact}, FirstSeen: seen}
			order = append(order, k)
		}
	}
	// The document is as fresh as its oldest artifact: when that
	// artifact's newest scan is not current (ScanIsCurrent), nor is the
	// verdict.
	if oldest != nil {
		doc.DB = DocumentDB{
			Scanner: oldest.Scanner, Version: oldest.ScannerVersion, Schema: oldest.DBSchema,
			BuiltAt: oldest.DBBuiltAt, Stale: !ScanIsCurrent(oldest, in.Now),
		}
	} else {
		doc.DB = DocumentDB{Scanner: ScannerGrype}
	}

	all := make([]Finding, 0, len(order))
	for _, k := range order {
		all = append(all, byKey[k].Finding)
	}
	ev := Evaluate(EvaluateInput{Gate: in.Gate, GradedAs: in.GradedAs, Findings: all, Coverage: doc.Coverage, Stale: doc.DB.Stale, Now: in.Now})
	doc.Gate = ev.Gate
	if doc.DB.Stale && ev.Gate.Verdict == VerdictStale {
		doc.Notes = append(doc.Notes, fmt.Sprintf("undetermined: the newest scan of an artifact used a vulnerability database built %s, older than %s, and no current scan replaced it", doc.DB.BuiltAt.Format("2006-01-02"), humanDuration(StaleAfter)))
	}
	for i, j := range ev.Findings {
		d := byKey[order[i]]
		d.Action, d.Exemption = j.Action, j.Exemption
		sort.Strings(d.Artifacts)
		if in.Filter.keep(*d) {
			doc.Findings = append(doc.Findings, *d)
			doc.Counts.add(d.Severity)
		}
	}
	sortDocumentFindings(doc.Findings)
	return doc
}

// firstSeenIn maps each finding key to the first scan in history that
// reported it.
func firstSeenIn(history []*Scan) map[string]time.Time {
	out := map[string]time.Time{}
	for _, s := range history {
		if s == nil {
			continue
		}
		for _, f := range s.Findings {
			k := f.Key()
			if t, ok := out[k]; !ok || s.ScannedAt.Before(t) {
				out[k] = s.ScannedAt
			}
		}
	}
	return out
}

// sortDocumentFindings orders listed findings the way SortFindings does,
// blocking ones first.
func sortDocumentFindings(ds []DocumentFinding) {
	rank := map[Action]int{ActionBlock: 0, ActionWarn: 1, ActionNone: 2}
	sort.SliceStable(ds, func(i, j int) bool {
		if ri, rj := rank[ds[i].Action], rank[ds[j].Action]; ri != rj {
			return ri < rj
		}
		a, b := ds[i].Finding, ds[j].Finding
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
