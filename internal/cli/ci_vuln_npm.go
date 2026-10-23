package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/config"
)

// The npm half of the vulnerability gate.
//
// It used to be `npm audit --audit-level=high` with the exit code as the
// verdict. That gave a project carrying an advisory with no fixed release —
// a fresh Next.js scaffold carries one, braces through its lint toolchain —
// the same two bad options the Go side had before ci.vuln_scan.exemptions:
// a permanently red gate, or `npm: false`. npm has no reviewed allowlist of
// its own; `--audit-level` is a severity floor, which cannot accept ONE
// advisory without accepting every other one at that level.
//
// So it now follows the Go side exactly: forge runs `npm audit --json`,
// reads the advisories out of the report, and applies the same per-ID,
// reasoned, expiring exemptions (evaluateVulnFindings) to them. The severity
// floor stays where it was — npmGateSeverity — so a project with no
// exemptions passes and fails on precisely the advisories it did before.

// npmGateSeverity is the lowest advisory severity that fails the gate. It is
// the `--audit-level=high` the gate always ran with: lower-severity
// advisories are printed, so they are seen, but do not block.
const npmGateSeverity = "high"

// npmSeverityRank orders npm audit's severities. An unknown severity ranks
// highest, so a value npm adds later fails closed rather than slipping under
// the floor.
func npmSeverityRank(s string) int {
	switch strings.ToLower(s) {
	case "info":
		return 0
	case "low":
		return 1
	case "moderate":
		return 2
	case "high":
		return 3
	case "critical":
		return 4
	}
	return 5
}

// npmAuditReport is the subset of `npm audit --json` (auditReportVersion 2,
// npm >= 7) this gate reads.
type npmAuditReport struct {
	AuditReportVersion int `json:"auditReportVersion"`
	// Vulnerabilities is keyed by package name. Each entry's Via holds
	// either a string (the name of ANOTHER vulnerable package this one is
	// vulnerable through) or an advisory object; only the objects are
	// advisories, the strings are edges of the dependency graph.
	Vulnerabilities map[string]struct {
		Via []json.RawMessage `json:"via"`
	} `json:"vulnerabilities"`
	// Error is what npm prints instead of a report when the audit could
	// not run at all: no lockfile (ENOLOCK), a registry failure, ...
	Error *struct {
		Code    string `json:"code"`
		Summary string `json:"summary"`
		Detail  string `json:"detail"`
	} `json:"error"`
}

// npmAdvisory is one advisory object inside a vulnerability's Via list.
type npmAdvisory struct {
	// Name is the package the advisory is filed against.
	Name     string `json:"name"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Severity string `json:"severity"`
	Range    string `json:"range"`
}

// parseNPMAuditJSON reduces one `npm audit --json` report to its advisories,
// one finding per advisory ID, in stable ID order.
//
// npm lists the same advisory under every package it reaches through, and
// files one advisory against several packages when it affects them all (the
// vitest path-traversal advisory is filed against both vitest and
// @vitest/mocker), so findings are keyed by the advisory's GHSA ID, keeping
// the highest severity seen. The ID is the advisory URL's last segment:
// GitHub's advisory database is npm's source, and the URL is the one field
// that names the GHSA ID.
func parseNPMAuditJSON(r io.Reader) ([]vulnFinding, error) {
	var report npmAuditReport
	if err := json.NewDecoder(r).Decode(&report); err != nil {
		return nil, fmt.Errorf("parse npm audit JSON output: %w", err)
	}
	if report.Error != nil {
		msg := strings.TrimSpace(report.Error.Summary)
		if d := strings.TrimSpace(report.Error.Detail); d != "" {
			msg += " — " + d
		}
		return nil, fmt.Errorf("npm audit could not run (%s): %s", report.Error.Code, msg)
	}
	// A missing or older report shape is refused rather than read as
	// "no vulnerabilities": an empty map from a format this parser does
	// not understand would otherwise be a pass nobody verified.
	if report.AuditReportVersion != 2 {
		return nil, fmt.Errorf("npm audit printed auditReportVersion %d; this gate reads version 2 "+
			"(npm >= 7) — upgrade npm", report.AuditReportVersion)
	}
	if report.Vulnerabilities == nil {
		return nil, fmt.Errorf("npm audit report has no vulnerabilities map — refusing an unverified pass")
	}

	// Walk packages in name order so an advisory filed against several
	// packages always reports the same package and range.
	pkgs := make([]string, 0, len(report.Vulnerabilities))
	for pkg := range report.Vulnerabilities {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)

	byID := map[string]vulnFinding{}
	for _, pkg := range pkgs {
		vuln := report.Vulnerabilities[pkg]
		if len(vuln.Via) == 0 {
			return nil, fmt.Errorf("npm audit vulnerability %s has no advisory or dependency edges", pkg)
		}
		for _, raw := range vuln.Via {
			// Only strings are graph edges. Malformed advisory objects
			// must fail, not disappear into a passing report.
			var edge string
			if json.Unmarshal(raw, &edge) == nil && edge != "" {
				continue
			}
			var adv npmAdvisory
			if err := json.Unmarshal(raw, &adv); err != nil || adv.URL == "" || adv.Name == "" || adv.Severity == "" {
				return nil, fmt.Errorf("npm audit %s has an unreadable advisory: %s", pkg, raw)
			}
			id := canonicalAdvisoryID(path.Base(strings.TrimRight(adv.URL, "/")))
			if advisoryEcosystem(id) != ecosystemNPM {
				// No GHSA ID to accept it by. Keep it, keyed by its URL,
				// so it still blocks — an advisory the exemption list
				// cannot name must not be one the gate cannot report.
				id = adv.URL
			}
			f, seen := byID[id]
			if !seen {
				f = vulnFinding{ID: id, Summary: adv.Title, Module: adv.Name, Range: adv.Range, Severity: adv.Severity, URL: adv.URL}
			} else if npmSeverityRank(adv.Severity) > npmSeverityRank(f.Severity) {
				f.Severity = adv.Severity
			}
			byID[id] = f
		}
	}
	if len(report.Vulnerabilities) > 0 && len(byID) == 0 {
		return nil, fmt.Errorf("npm audit reported vulnerable packages but no advisories — refusing an unverified pass")
	}

	out := make([]vulnFinding, 0, len(byID))
	for _, f := range byID {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// mergeNPMFindings folds one frontend's findings into the project-wide set,
// recording which frontends carry each advisory. Exemptions apply to the
// project, so an advisory present in two frontends is one finding, and an
// exemption is stale only when NO frontend still carries its advisory.
func mergeNPMFindings(into map[string]*vulnFinding, frontend string, findings []vulnFinding) {
	for _, f := range findings {
		if existing, ok := into[f.ID]; ok {
			existing.In = append(existing.In, frontend)
			if npmSeverityRank(f.Severity) > npmSeverityRank(existing.Severity) {
				existing.Severity = f.Severity
			}
			continue
		}
		f.In = []string{frontend}
		into[f.ID] = &f
	}
}

// belowNPMGate moves the unaccepted findings below npmGateSeverity out of
// res.Blocking and returns them. They are reported, never silently dropped:
// a moderate advisory in a production dependency is worth seeing even though
// the gate does not fail on it.
func belowNPMGate(res *vulnGateResult) []vulnFinding {
	var blocking, below []vulnFinding
	for _, f := range res.Blocking {
		if npmSeverityRank(f.Severity) < npmSeverityRank(npmGateSeverity) {
			below = append(below, f)
			continue
		}
		blocking = append(blocking, f)
	}
	res.Blocking = blocking
	return below
}

// ciRunNPMAudit runs npm audit across every declared frontend, applies the
// npm entries of ci.vuln_scan.exemptions, and returns a short description of
// what it scanned (for the caller's success line), or an error. A project
// with no frontends yields errScannerUnavailable — nothing was audited,
// which the caller must not fold into a pass.
//
// npm audit's exit code is not the verdict: it exits non-zero whenever it
// finds anything at all, including advisories the gate does not fail on or
// that the project accepted. The verdict is the report. Every way of NOT
// getting a report — npm missing, npm failing before it prints one, output
// that will not parse — is therefore an explicit error, never a pass.
func ciRunNPMAudit(ctx context.Context, cfg *config.ProjectConfig, exemptions []config.CIVulnExemption, now time.Time) (string, error) {
	if len(cfg.Frontends) == 0 {
		return "", unavailable{cliutil.UserErr("forge ci vuln-scan --npm",
			"no frontends are declared in forge.yaml, so there is no package.json to audit",
			"forge.yaml (frontends)",
			"add a frontend with `forge scaffold frontend <name>`, or drop --npm — a Go-only project has nothing for npm audit to scan")}
	}
	if _, err := exec.LookPath("npm"); err != nil {
		// Not errScannerUnavailable: this project HAS frontends, so a
		// missing npm means the audit it needs could not run. That is a
		// failure even under the default selection.
		return "", fmt.Errorf("npm is not on PATH, so the %d frontend(s) could not be audited — install Node.js (npm ships with it)", len(cfg.Frontends))
	}

	all := map[string]*vulnFinding{}
	for _, fe := range cfg.Frontends {
		dir := fe.DeclaredDir()
		fmt.Printf("Running npm audit in %s ...\n", dir)
		findings, err := runNPMAudit(ctx, dir)
		if err != nil {
			return "", fmt.Errorf("frontend %s (%s): %w", fe.Name, dir, err)
		}
		mergeNPMFindings(all, fe.Name, findings)
	}

	findings := make([]vulnFinding, 0, len(all))
	for _, f := range all {
		findings = append(findings, *f)
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })

	res := evaluateVulnFindings(findings, exemptions, now)
	below := belowNPMGate(&res)
	res.report(os.Stdout)
	for _, f := range below {
		fmt.Printf("  ℹ️  %s (%s) — below the gate's %q threshold, not failing it\n", f.ID, f.label(), npmGateSeverity)
		printFindingDetail(os.Stdout, f)
	}

	if len(res.Blocking) > 0 {
		return "", fmt.Errorf("npm audit found %d advisory/ies at or above %q", len(res.Blocking), npmGateSeverity)
	}
	if len(res.Malformed) > 0 {
		return "", fmt.Errorf("%d vulnerability exemption(s) in forge.yaml could not be honored", len(res.Malformed))
	}

	what := fmt.Sprintf("npm audit (%d frontend(s))", len(cfg.Frontends))
	if len(res.Accepted) > 0 {
		what += fmt.Sprintf(", %d accepted exemption(s)", len(res.Accepted))
	}
	return what, nil
}

// runNPMAudit runs `npm audit --json` in dir and parses its report.
func runNPMAudit(ctx context.Context, dir string) ([]vulnFinding, error) {
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "npm", "audit", "--json")
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()

	if runErr != nil {
		var exitErr *exec.ExitError
		if ctx.Err() != nil || !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 1 {
			return nil, fmt.Errorf("npm audit did not complete: %w", runErr)
		}
	}
	findings, err := parseNPMAuditJSON(&stdout)
	if err != nil {
		return nil, err
	}
	if runErr != nil && len(findings) == 0 {
		return nil, fmt.Errorf("npm audit exited non-zero without advisories: %w", runErr)
	}
	return findings, nil
}
