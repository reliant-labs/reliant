package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/vulnscan"
)

type vulnListOptions struct {
	release       string
	severity      string
	fixable       bool
	kev           bool
	includeExempt bool
	asJSON        bool
}

func newVulnListCmd(deps vulnDeps) *cobra.Command {
	var opts vulnListOptions
	cmd := &cobra.Command{
		Use:   "list <env>",
		Short: "List the vulnerabilities in the release an env runs, judged by its vuln_gate",
		Long: `List what the release an env runs (or --release) is known to be vulnerable to,
judged by the env's vuln_gate.

Reads the scans stored in this machine's ledger. An SBOM never scanned, or last
scanned with an older vulnerability database than the one on this machine, is
scanned again first (and the scan stored). list never downloads the database or
the scanner: ` + "`forge vuln db pull`" + ` refreshes the database.

Filters narrow what is listed, never the verdict: the gate judges every finding.

For an env whose ledger is a control plane, the findings are the control
plane's: it scanned the SBOMs each cut uploaded, and rescans what is deployed
daily. Nothing is scanned on this machine; reading needs the vuln:read scope.

Exit codes: 0 nothing blocking; 1 a blocking finding; 2 could not determine (an
artifact with no SBOM or scan, findings from a database older than 5 days, or a
control plane that does not serve findings yet); 4 refused (not signed in to the
env's control plane, or no vuln:read).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if s := strings.ToLower(opts.severity); s != "" && !validSeverityFilter(s) {
				return fmt.Errorf("--severity %q (expected low, medium, high or critical)", opts.severity)
			}
			filter := vulnscan.Filter{MinSeverity: strings.ToLower(opts.severity), Fixable: opts.fixable, KEV: opts.kev, IncludeExempt: opts.includeExempt}
			return runVulnList(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), deps, args[0], opts.release, filter, opts.asJSON)
		},
	}
	cmd.Flags().StringVar(&opts.release, "release", "", "list this release instead of the one the env runs")
	cmd.Flags().StringVar(&opts.severity, "severity", "", "list findings at or above this severity: low, medium, high, critical")
	cmd.Flags().BoolVar(&opts.fixable, "fixable", false, "list only findings with a fixed version")
	cmd.Flags().BoolVar(&opts.kev, "kev", false, "list only findings on CISA's Known Exploited Vulnerabilities list")
	cmd.Flags().BoolVar(&opts.includeExempt, "include-exempt", false, "also list findings the env's vuln_gate exempts")
	cmd.Flags().BoolVar(&opts.asJSON, "json", false, "print the forge.dev/vuln-list/v1 document (forge project schema vuln-list)")
	return cmd
}

func validSeverityFilter(s string) bool {
	switch s {
	case "low", "medium", "high", "critical":
		return true
	}
	return false
}

func newVulnShowCmd(deps vulnDeps) *cobra.Command {
	var (
		rel    string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "show <env> <advisory-id | package>",
		Short: "Show one advisory, or every finding in one package, in the release an env runs",
		Long: `Show the findings of one advisory (by its id or any alias: a CVE finds the GHSA
of the same advisory) or of one package (by name or purl) in the release an env
runs, exempt ones included. Reads the ledger the way ` + "`forge vuln list`" + ` does, and
exits the way it does; on a control-plane env the advisory's summary, references
and history per environment follow.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			filter := vulnscan.Filter{Match: args[1], IncludeExempt: true}
			return runVulnList(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), deps, args[0], rel, filter, asJSON)
		},
	}
	cmd.Flags().StringVar(&rel, "release", "", "read this release instead of the one the env runs")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the forge.dev/vuln-list/v1 document, narrowed to the match")
	return cmd
}

func runVulnList(ctx context.Context, out, errOut io.Writer, deps vulnDeps, envName, version string, filter vulnscan.Filter, asJSON bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	projectDir := deps.projectDir()
	env, err := deps.resolveEnv(ctx, projectDir, envName)
	if err != nil {
		return undeterminedf("env %q: %v", envName, err)
	}
	if env.hosted() {
		return runVulnListHosted(ctx, out, errOut, deps, env, version, filter, asJSON)
	}
	store, err := deps.machineLedger(projectDir)
	if err != nil {
		return undeterminedf("open this machine's ledger: %v", err)
	}
	rel, err := releaseOf(store, envName, version)
	if err != nil {
		return err
	}
	now := deps.now()

	// The database and scanner as they are: list installs and downloads
	// nothing. Without them, or with a stale database, nothing is rescanned
	// and the stored scans stand in, under vulnscan.ScanIsCurrent's rule:
	// the verdict is determined only if each artifact's newest stored scan
	// used a database built within five days (it is a scan with a fresh
	// database, even if grype is absent now). Otherwise it is "stale",
	// exit 2, whatever the stored findings say, and they are still listed.
	var notes []string
	var scanner *vulnscan.Scanner
	var local vulnscan.Status
	db, err := deps.openDB(errOut, false)
	if err == nil {
		local, err = db.Status(ctx)
	}
	switch {
	case errors.Is(err, vulnscan.ErrScannerMissing):
		notes = append(notes, "grype is not installed, so nothing was rescanned (`forge vuln db pull` installs it and the database)")
	case err != nil:
		return dbExit(err)
	case !local.Present:
		notes = append(notes, "no vulnerability database on this machine, so nothing was rescanned (`forge vuln db pull`)")
	default:
		scanner = &vulnscan.Scanner{DB: db}
		if local.Stale(now) {
			notes = append(notes, fmt.Sprintf("the vulnerability database on this machine was built %s, older than 5 days, so nothing was rescanned (`forge vuln db pull`)", local.BuiltAt.Format("2006-01-02")))
		} else if !local.Fresh(now) {
			notes = append(notes, fmt.Sprintf("the vulnerability database was built %s ago; `forge vuln db pull` refreshes it", roundAge(local.Age(now))))
		}
	}
	rs, err := vulnscan.ScanRelease(ctx, store, scanner, local, rel, vulnscan.RescanIfNewer)
	if err != nil {
		return undeterminedf("%v", err)
	}
	notes = append(notes, rs.Notes...)
	if env.GradeNote != "" {
		notes = append(notes, env.GradeNote)
	}
	doc := vulnscan.BuildDocument(vulnscan.DocumentInput{
		Env: envName, Release: rel.Version, Gate: env.Gate, GradedAs: env.GradedAs, Artifacts: rs.Artifacts, Filter: filter, Now: now, Notes: notes,
	})
	if asJSON {
		if err := writeVulnJSON(out, doc); err != nil {
			return err
		}
	} else {
		if filter.Match != "" && len(doc.Findings) == 0 {
			_, _ = fmt.Fprintf(out, "no finding in %s release %s matches %q\n", envName, rel.Version, filter.Match)
		}
		writeVulnDocument(out, doc, now, "")
	}
	return vulnExit(doc)
}
