package vulnscan

import (
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// The stdlib case scoped exemptions exist for: one Go advisory in two
// binaries, unreachable in one of them. stdlib's package name and purl are
// the same in every binary, so only the artifact and the path tell them
// apart.
func TestScopedExemptions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	stdlib := func(locs ...string) Finding {
		return Finding{
			ID: "GO-2026-6859", Aliases: []string{"CVE-2026-6859"}, Severity: SeverityCritical, FixState: FixStateFixed,
			Package:   Package{Name: "stdlib", Version: "go1.24.1", PURL: "pkg:golang/stdlib@go1.24.1", Type: "go-module"},
			Locations: locs,
		}
	}
	scoped := func(xs ...release.ShapeVulnScopedExempt) release.ShapeVulnGate {
		g := defaultGate()
		for i := range xs {
			xs[i].Reason, xs[i].Expires = "grype never parses untrusted archives", "2026-12-31"
		}
		g.ScopedExemptions = xs
		return g
	}
	grypeOnly := release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Artifacts: []string{"api"}, Locations: []string{"/usr/local/bin/grype"}}
	occ := func(artifact string, locs ...string) []release.VulnOccurrence {
		return occurrencesOf(stdlib(locs...), []string{artifact})
	}
	both := append(occ("api", "/usr/local/bin/grype"), occ("worker", "/app/worker")...)

	cases := []struct {
		name       string
		gate       release.ShapeVulnGate
		f          Finding
		occ        []release.VulnOccurrence
		wantExempt bool
	}{
		{"unscoped covers every occurrence", func() release.ShapeVulnGate {
			g := defaultGate()
			g.Exemptions = []release.ShapeVulnExempt{{ID: "GO-2026-6859", Reason: "r", Expires: "2026-12-31"}}
			return g
		}(), stdlib("/app/worker"), both, true},
		{"artifact + location covers its one occurrence", scoped(grypeOnly), stdlib("/usr/local/bin/grype"), occ("api", "/usr/local/bin/grype"), true},
		{"by alias", scoped(release.ShapeVulnScopedExempt{ID: "cve-2026-6859", Locations: []string{"/usr/local/bin/grype"}}), stdlib("/usr/local/bin/grype"), occ("api", "/usr/local/bin/grype"), true},
		{"another binary of the same artifact is not covered", scoped(grypeOnly), stdlib("/usr/local/bin/grype", "/app/api"), occ("api", "/usr/local/bin/grype", "/app/api"), false},
		{"the same path in another artifact is not covered", scoped(grypeOnly), stdlib("/usr/local/bin/grype"), occ("worker", "/usr/local/bin/grype"), false},
		{"merged across artifacts: one uncovered keeps it", scoped(grypeOnly), stdlib("/usr/local/bin/grype"), both, false},
		{"merged across artifacts: two scopes cover both", scoped(grypeOnly, release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Artifacts: []string{"worker"}}), stdlib("/usr/local/bin/grype"), both, true},
		{"location directory prefix", scoped(release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Locations: []string{"/usr/local/bin/"}}), stdlib("/usr/local/bin/grype"), occ("api", "/usr/local/bin/grype"), true},
		{"a location that is not a directory is not a prefix", scoped(release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Locations: []string{"/usr/local/bin/gr"}}), stdlib("/usr/local/bin/grype"), occ("api", "/usr/local/bin/grype"), false},
		{"package by name", scoped(release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Packages: []string{"stdlib"}}), stdlib("/app/worker"), both, true},
		{"package by purl prefix", scoped(release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Packages: []string{"pkg:golang/stdlib"}}), stdlib("/app/worker"), both, true},
		{"package purl prefix stops at a boundary", scoped(release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Packages: []string{"pkg:golang/std"}}), stdlib("/app/worker"), both, false},
		{"another package is not covered", scoped(release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Packages: []string{"golang.org/x/net"}}), stdlib("/app/worker"), both, false},
		{"package AND artifact: both must name it", scoped(release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Packages: []string{"stdlib"}, Artifacts: []string{"api"}}), stdlib("/usr/local/bin/grype"), both, false},
		{"no artifact known: an artifact scope covers nothing", scoped(release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Artifacts: []string{"api"}}), stdlib("/usr/local/bin/grype"), nil, false},
		{"no location known: a location scope covers nothing", scoped(release.ShapeVulnScopedExempt{ID: "GO-2026-6859", Locations: []string{"/usr/local/bin/grype"}}), stdlib(), occ("api"), false},
		{"another advisory's scoped exemption", scoped(release.ShapeVulnScopedExempt{ID: "GO-2026-0001", Artifacts: []string{"api"}}), stdlib("/usr/local/bin/grype"), occ("api", "/usr/local/bin/grype"), false},
		{"expired scoped exemption", func() release.ShapeVulnGate {
			g := scoped(grypeOnly)
			g.ScopedExemptions[0].Expires = "2026-11-19"
			return g
		}(), stdlib("/usr/local/bin/grype"), occ("api", "/usr/local/bin/grype"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			action, rule, e := judge(tc.gate, tc.f, tc.occ, now)
			if got := e != nil; got != tc.wantExempt {
				t.Fatalf("exempt = %v, want %v (action %s)", got, tc.wantExempt, action)
			}
			if tc.wantExempt && rule != RuleExemption {
				t.Fatalf("an exempted finding's rule = %q, want %q", rule, RuleExemption)
			}
			if !tc.wantExempt && action != ActionBlock {
				t.Fatalf("an unexempted fixable critical judged %s, want block", action)
			}
		})
	}
}

// BuildDocument matches a scoped exemption against every artifact and path a
// merged finding was found at, and lists them.
func TestBuildDocumentScopedExemption(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	scan := func(loc string) *Scan {
		return &Scan{Schema: ScanSchema, Scanner: ScannerGrype, DBBuiltAt: now, ScannedAt: now, Findings: []Finding{{
			ID: "GO-2026-6859", Severity: SeverityCritical, FixState: FixStateFixed,
			Package: Package{Name: "stdlib", Version: "go1.24.1", PURL: "pkg:golang/stdlib@go1.24.1"}, Locations: []string{loc},
		}}}
	}
	gate := defaultGate()
	gate.ScopedExemptions = []release.ShapeVulnScopedExempt{{ID: "GO-2026-6859", Reason: "r", Expires: "2026-12-31", Artifacts: []string{"api"}, Locations: []string{"/usr/local/bin/grype"}}}
	in := DocumentInput{Env: "prod", Release: "v1", Gate: gate, Now: now, Filter: Filter{IncludeExempt: true}, Artifacts: []ArtifactScan{
		{Artifact: "api", SBOMDigest: "sha256:a", Latest: scan("/usr/local/bin/grype")},
	}}
	if doc := BuildDocument(in); len(doc.Findings) != 1 || doc.Findings[0].Exemption == nil || doc.Gate.Verdict != VerdictPass {
		t.Fatalf("one artifact, covered: findings %+v, verdict %s", doc.Findings, doc.Gate.Verdict)
	}
	in.Artifacts = append(in.Artifacts, ArtifactScan{Artifact: "worker", SBOMDigest: "sha256:w", Latest: scan("/app/worker")})
	doc := BuildDocument(in)
	if len(doc.Findings) != 1 || doc.Findings[0].Exemption != nil || doc.Gate.Verdict != VerdictBlock {
		t.Fatalf("merged with an uncovered artifact: findings %+v, verdict %s; want it blocking", doc.Findings, doc.Gate.Verdict)
	}
	want := []Occurrence{{Artifact: "api", Location: "/usr/local/bin/grype"}, {Artifact: "worker", Location: "/app/worker"}}
	if got := doc.Findings[0].Occurrences; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("occurrences = %+v, want %+v", got, want)
	}
}
