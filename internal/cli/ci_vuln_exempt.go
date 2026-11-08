package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/config"
)

// The vulnerability gate's decision layer, shared by both scanners.
//
// Neither scanner has a reviewed allowlist. govulncheck has none at all: it
// prints what it finds and exits non-zero. npm audit has none either — its
// only knob is a severity floor, which cannot say "this ONE advisory". So a
// project that carries an advisory with NO fixed version has only two native
// options — a permanently red gate, or no gate at all. Both are bad in the
// same direction. A red gate stops being read after the second week, and a
// disabled one cannot report the NEXT advisory, which is the one that might
// actually be reachable.
//
// So forge makes the pass/fail call itself. It runs each scanner in JSON
// mode, which hands the decision over deliberately: govulncheck exits 0
// regardless of findings, and npm audit's exit code is ignored in favour of
// the report it prints. Forge keeps the findings that can fail the gate
// (govulncheck's CALLED findings; npm advisories at or above `high`) and
// drops only the advisory IDs the project has explicitly accepted.
// Everything else fails exactly as before.
//
// Both scanners read ONE list, ci.vuln_scan.exemptions, and the ID's own
// namespace says which scanner an entry belongs to: govulncheck reports
// GO-YYYY-NNNN, npm audit reports GHSA-xxxx-xxxx-xxxx. See
// partitionExemptions.
//
// What keeps this from becoming a place to hide problems:
//   - Exemptions are per-ID. Accepting one CVE in a module does not accept
//     the module; a new advisory in the same dependency still fails.
//   - Every entry carries a reason and an expiry, and an expired entry
//     fails the gate. The re-review happens on a date instead of never.
//   - An entry matching nothing is reported as stale, so the list shrinks
//     when advisories get fixed rather than silently pre-authorizing a
//     future finding.

// govulncheckFinding is the subset of govulncheck's JSON stream this gate
// reads. The stream is a sequence of concatenated objects, each carrying
// exactly one of "config" / "SBOM" / "progress" / "osv" / "finding"; the
// fields not named here are ignored.
type govulncheckFinding struct {
	OSV   string `json:"osv"`
	Trace []struct {
		Module   string `json:"module"`
		Version  string `json:"version"`
		Package  string `json:"package"`
		Function string `json:"function"`
	} `json:"trace"`
}

// govulncheckMessage is one element of the JSON stream.
type govulncheckMessage struct {
	OSV *struct {
		ID      string `json:"id"`
		Summary string `json:"summary"`
	} `json:"osv"`
	Finding *govulncheckFinding `json:"finding"`
}

// vulnFinding is one advisory a scanner reported, reduced to what the gate
// reports. The Go scanner fills ID/Summary/Module/Version; the npm scanner
// fills ID/Summary/Module (the package)/Range/Severity/URL/In.
type vulnFinding struct {
	ID      string
	Summary string
	Module  string
	Version string
	// Range is the vulnerable version range npm audit states, e.g.
	// "<=3.0.3" — npm reports which versions are affected, not the one
	// installed.
	Range string
	// Severity is the advisory's own severity (npm only). The gate's
	// threshold applies to it — see npmGateSeverity.
	Severity string
	// URL is the advisory page, printed so a reviewer is one click away
	// from the details.
	URL string
	// In names the frontends whose install tree carries the advisory.
	In []string
	// Acceptance is populated only after a live exemption matched.
	Acceptance *config.CIVulnExemption
}

// label renders the finding's location for the report: module@version for
// Go, "package <range>, severity, in <frontends>" for npm.
func (f vulnFinding) label() string {
	s := f.Module
	switch {
	case f.Version != "":
		s += "@" + f.Version
	case f.Range != "":
		s += " " + f.Range
	}
	if f.Severity != "" {
		s += ", " + f.Severity
	}
	if len(f.In) > 0 {
		s += ", in " + strings.Join(f.In, ", ")
	}
	return s
}

// Advisory namespaces. Each scanner reports exactly one, so an exemption's ID
// alone routes it: there is no `scanner:` field to keep in step with the ID.
const (
	ecosystemGo  = "go"  // govulncheck: GO-2026-4887
	ecosystemNPM = "npm" // npm audit:   GHSA-vfj7-8cjw-p6xm
)

var (
	goAdvisoryRE   = regexp.MustCompile(`^GO-\d{4}-\d+$`)
	ghsaAdvisoryRE = regexp.MustCompile(`^GHSA(-[23456789cfghjmpqrvwx]{4}){3}$`)
)

// canonicalAdvisoryID normalizes an ID for comparison. GitHub prints GHSA
// IDs with an upper-case prefix and a lower-case body, and people retype them
// in either case; an exemption that fails to match on case alone would be a
// silent no-op, so both sides are folded to GitHub's spelling before
// comparing. Anything that is not a GHSA ID is only trimmed.
func canonicalAdvisoryID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > 5 && strings.EqualFold(id[:5], "GHSA-") {
		return "GHSA-" + strings.ToLower(id[5:])
	}
	return id
}

// advisoryEcosystem names the scanner that reports id, or "" when neither
// does.
func advisoryEcosystem(id string) string {
	id = canonicalAdvisoryID(id)
	switch {
	case goAdvisoryRE.MatchString(id):
		return ecosystemGo
	case ghsaAdvisoryRE.MatchString(id):
		return ecosystemNPM
	}
	return ""
}

// partitionExemptions splits ci.vuln_scan.exemptions by the scanner each
// entry's ID belongs to, so each scanner judges only its own: a GHSA entry
// must not read as stale to govulncheck merely because govulncheck never
// reports GHSA IDs, and the reverse.
//
// An entry whose ID neither scanner reports — a blank id, a CVE alias, a
// typo — can accept nothing anywhere. It is returned in unroutable, as a
// message, and the caller FAILS the gate on it, for the same reason a
// malformed entry fails: the file claims an acceptance the gate is not
// honoring. Reporting it here, once, rather than inside each scanner keeps it
// from being reported twice, or not at all when only the other scanner runs.
func partitionExemptions(all []config.CIVulnExemption) (byEcosystem map[string][]config.CIVulnExemption, unroutable []string) {
	byEcosystem = map[string][]config.CIVulnExemption{}
	for _, ex := range all {
		id := strings.TrimSpace(ex.ID)
		eco := advisoryEcosystem(id)
		switch {
		case id == "":
			unroutable = append(unroutable,
				"an exemption entry has no `id` — it cannot accept anything, so it is ignored")
		case eco == "":
			unroutable = append(unroutable, fmt.Sprintf(
				"%s is not an ID either scanner reports, so it can accept nothing — govulncheck findings "+
					"are accepted by their GO-YYYY-NNNN id and npm audit findings by their GHSA-xxxx-xxxx-xxxx "+
					"id; use the id the scan printed", id))
		default:
			byEcosystem[eco] = append(byEcosystem[eco], ex)
		}
	}
	return byEcosystem, unroutable
}

// parseGovulncheckJSON reduces a govulncheck JSON stream to the advisories
// it judged CALLED, in stable ID order.
//
// "Called" is the distinction that makes this gate meaningful, and it is
// carried in the shape of the trace rather than in a field of its own:
// govulncheck emits a finding per level of detail, and only the
// symbol-level one has a Function set on the frame. Module- and
// package-level findings for the same advisory mean "present in the build
// graph", which govulncheck's own text output reports separately as "your
// code doesn't appear to call these". Treating those as failures would
// fail the gate on advisories in code that is compiled but never run.
func parseGovulncheckJSON(r io.Reader) ([]vulnFinding, error) {
	dec := json.NewDecoder(r)

	summaries := map[string]string{}
	called := map[string]vulnFinding{}

	for {
		var msg govulncheckMessage
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("parse govulncheck JSON output: %w", err)
		}

		if msg.OSV != nil {
			summaries[msg.OSV.ID] = msg.OSV.Summary
			continue
		}
		if msg.Finding == nil || len(msg.Finding.Trace) == 0 {
			continue
		}
		// Frame 0 is the vulnerable symbol itself. A Function there is
		// what separates a call trace from a mere presence report.
		top := msg.Finding.Trace[0]
		if top.Function == "" {
			continue
		}
		called[msg.Finding.OSV] = vulnFinding{
			ID:      msg.Finding.OSV,
			Module:  top.Module,
			Version: top.Version,
		}
	}

	out := make([]vulnFinding, 0, len(called))
	for id, f := range called {
		f.Summary = summaries[id]
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// vulnGateResult is the verdict over one scan.
type vulnGateResult struct {
	// Blocking are findings that must fail the gate: not exempted, or
	// exempted by an entry that has expired.
	Blocking []vulnFinding
	// Accepted are findings suppressed by a live exemption.
	Accepted []vulnFinding
	// StaleExemptions name entries that matched no finding.
	StaleExemptions []string
	// Malformed name entries that cannot be honored as written.
	Malformed []string
}

// evaluateVulnFindings applies the project's exemptions to a scan.
//
// now is injected so the expiry rule is testable without waiting for a
// calendar date to pass.
func evaluateVulnFindings(findings []vulnFinding, exemptions []config.CIVulnExemption, now time.Time) vulnGateResult {
	var res vulnGateResult

	// live maps an advisory ID to the exemption accepting it. An entry
	// that is malformed or expired is deliberately NOT entered here, so
	// its advisory falls through and blocks — the fail-closed direction.
	live := map[string]config.CIVulnExemption{}
	matched := map[string]bool{}

	for _, ex := range exemptions {
		id := canonicalAdvisoryID(ex.ID)
		switch {
		case id == "":
			res.Malformed = append(res.Malformed,
				"an exemption entry has no `id` — it cannot accept anything, so it is ignored")
			continue
		case strings.TrimSpace(ex.Reason) == "":
			res.Malformed = append(res.Malformed, fmt.Sprintf(
				"%s has no `reason` — an unexplained exemption cannot be reviewed, so it is not honored", id))
			continue
		case strings.TrimSpace(ex.Expires) == "":
			res.Malformed = append(res.Malformed, fmt.Sprintf(
				"%s has no `expires` — an exemption without a review date never gets re-examined, so it is not honored", id))
			continue
		}

		expiry, err := time.Parse("2006-01-02", strings.TrimSpace(ex.Expires))
		if err != nil {
			res.Malformed = append(res.Malformed, fmt.Sprintf(
				"%s has an unparseable `expires: %s` — want YYYY-MM-DD, so it is not honored", id, ex.Expires))
			continue
		}
		// Expiry is end-of-day: an exemption dated today is still live
		// today and lapses tomorrow.
		if now.After(expiry.AddDate(0, 0, 1)) {
			res.Malformed = append(res.Malformed, fmt.Sprintf(
				"%s expired on %s — re-review it (is it still unreachable? is there a fix now?) and either "+
					"extend `expires` with a fresh justification or remove the entry", id, ex.Expires))
			continue
		}
		live[id] = ex
	}

	for _, f := range findings {
		id := canonicalAdvisoryID(f.ID)
		if ex, ok := live[id]; ok {
			matched[id] = true
			f.Acceptance = &ex
			res.Accepted = append(res.Accepted, f)
			continue
		}
		res.Blocking = append(res.Blocking, f)
	}

	for id := range live {
		if !matched[id] {
			res.StaleExemptions = append(res.StaleExemptions, id)
		}
	}
	sort.Strings(res.StaleExemptions)
	return res
}

// report prints the verdict. Accepted findings are printed in full, with
// their justification: an accepted risk that scrolls by as a count is one
// nobody re-reads, and this output is the only place most people will
// ever see the reasoning.
func (r vulnGateResult) report(w io.Writer) {
	for _, f := range r.Accepted {
		fmt.Fprintf(w, "  ⚠️  accepted: %s (%s)\n", f.ID, f.label())
		printFindingDetail(w, f)
		if f.Acceptance != nil {
			fmt.Fprintf(w, "      reason: %s\n      expires: %s\n", f.Acceptance.Reason, f.Acceptance.Expires)
		}
	}
	for _, id := range r.StaleExemptions {
		fmt.Fprintf(w, "  🧹 exemption %s matched nothing in this scan — the advisory is gone or now "+
			"unreachable. Remove it from forge.yaml (ci.vuln_scan.exemptions) so it cannot silently "+
			"accept a future finding.\n", id)
	}
	for _, m := range r.Malformed {
		fmt.Fprintf(w, "  ❌ exemption not honored: %s\n", m)
	}
	for _, f := range r.Blocking {
		fmt.Fprintf(w, "  ❌ %s (%s)\n", f.ID, f.label())
		printFindingDetail(w, f)
	}
}

// printFindingDetail prints the indented summary and advisory link under a
// finding's headline.
func printFindingDetail(w io.Writer, f vulnFinding) {
	if f.Summary != "" {
		fmt.Fprintf(w, "      %s\n", f.Summary)
	}
	if f.URL != "" {
		fmt.Fprintf(w, "      %s\n", f.URL)
	}
}
