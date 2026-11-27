package cli

// `forge vuln`: what a release's artifacts are known to be vulnerable to, and
// what the env's vuln_gate makes of it.
//
//	forge vuln scan [<dir> | --image <ref@digest> | --release <v> | --deployed <env>] [--json]
//	forge vuln gate [<name>=]<path>... [--sbom <file>] [--db <dir>] [--out <dir>] [--policy <file> | --env <env>] [--json]
//	forge vuln list <env> [--release v] [--severity s] [--fixable] [--kev] [--include-exempt] [--json]
//	forge vuln show <env> <advisory-id | package> [--json]
//	forge vuln sbom <env> [--release v] [--artifact name] [--format cyclonedx|spdx]
//	forge vuln sbom --file <path> [--artifact name] [--release v] [--format cyclonedx|spdx]
//	forge vuln db status|pull|import <file>
//	forge vuln exempt <env> <advisory-id> --reason "<why>" --expires YYYY-MM-DD [--package p] [--artifact a] [--location /path]
//
// The scanning, the shared grype database and the gate rule are
// internal/vulnscan; this file and its vuln_*.go siblings are flags, the
// env's ledger and policy, output and exit codes.
//
// TWO LEDGERS, ONE DOCUMENT. An env whose ledger is a control plane keeps
// its SBOMs and findings there: `list`, `show` and `scan --deployed` read
// them from its VulnerabilityService (vuln_hosted.go) and print the same
// forge.dev/vuln-list/v1 document, judged by the same rule, with the same
// exit codes. They never guess from this machine's ledger, which holds none
// of such an env's releases (only the SBOM bytes the cuts made here).
//
// Exit codes (exitcodes.go): 0 nothing the env's vuln_gate would block; 1 at
// least one blocking finding (in any mode: the exit says what was found, the
// mode decides whether a promotion is refused); 2 could not determine (an
// artifact with no SBOM or scan, the scanner missing, the database stale or
// absent, an env this forge cannot read yet); 4 refused.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/internal/sbom"
	"github.com/reliant-labs/forge/internal/toolfetch"
	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/release"
)

// vulnDeps are the seams a test replaces: the tools, the database directory,
// the grype runner, the env resolution and the clock.
type vulnDeps struct {
	// tool resolves syft or grype. install=false never downloads (list,
	// show: they only use what is installed); install=true may.
	tool func(ctx context.Context, t toolfetch.Tool, install bool) (path, version string, err error)
	// dbDir is the shared database directory.
	dbDir func() (string, error)
	// dbURL is the database distribution: $FORGE_VULN_DB_URL or grype's own.
	dbURL func() string
	// run runs grype (and syft); nil runs the binary.
	run vulnscan.Runner
	// resolveEnv reads an env's ledger kind and vuln_gate.
	resolveEnv func(ctx context.Context, projectDir, env string) (vulnEnv, error)
	// machineLedger opens this project's machine ledger.
	machineLedger func(projectDir string) (*ledgerfile.Store, error)
	// projectDir is the project the command acts on.
	projectDir func() string
	now        func() time.Time
	// hostedClient reaches the control plane that keeps a hosted env's
	// ledger (vuln_hosted.go).
	hostedClient func(ctx context.Context, env vulnEnv) (cloudCaller, error)
	// sbomRunner runs syft for `vuln sbom --file`; nil execs it.
	sbomRunner sbom.Runner
	// sbomCache is the SBOM cache's directory for `vuln sbom --file`; nil
	// or "" caches nothing.
	sbomCache func() string
}

func defaultVulnDeps() vulnDeps {
	return vulnDeps{
		tool:          resolveVulnTool,
		dbDir:         vulnscan.DefaultDir,
		dbURL:         vulnscan.DefaultBaseURL,
		resolveEnv:    resolveVulnEnv,
		machineLedger: openMachineLedger,
		projectDir:    projectDirForKCL,
		now:           time.Now,
		hostedClient:  hostedVulnClient,
		sbomCache:     sbomCacheDir,
	}
}

// resolveVulnTool is toolfetch: a pinned, sha256-verified release binary.
func resolveVulnTool(ctx context.Context, t toolfetch.Tool, install bool) (string, string, error) {
	if install {
		return toolfetch.Ensure(ctx, t)
	}
	in, ok := toolfetch.Find(t)
	if !ok {
		return "", "", fmt.Errorf("%s %s is not installed (`forge vuln scan` or `forge vuln db pull` installs it)", t.Name, t.Version)
	}
	return in.Path, in.Version, nil
}

// openDB is the shared database, with grype resolved per install.
func (d vulnDeps) openDB(log io.Writer, install bool) (*vulnscan.DB, error) {
	dir, err := d.dbDir()
	if err != nil {
		return nil, err
	}
	return &vulnscan.DB{
		Dir: dir,
		URL: d.dbURL(),
		Grype: func(ctx context.Context) (string, string, error) {
			return d.tool(ctx, toolfetch.Grype, install)
		},
		Run: d.run,
		Now: d.now,
		Log: log,
	}, nil
}

func newVulnCmd() *cobra.Command { return newVulnCmdWith(defaultVulnDeps()) }

func newVulnCmdWith(deps vulnDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vuln",
		Short: "Vulnerabilities in a release's SBOMs, judged by the env's vuln_gate",
		Long: `Scan SBOMs for known vulnerabilities with grype, and judge the findings with the
env's vuln_gate (forge.DeliveryPolicy.vuln_gate).

Every release forge cuts records an SBOM per artifact; these verbs scan them
against one grype database shared by every project on this machine
(` + "`forge vuln db status`" + `), and store each scan in the machine ledger.

Exit codes: 0 nothing the env's vuln_gate would block; 1 at least one blocking
finding (in any gate mode); 2 could not determine (an artifact with no SBOM, the
scanner missing, the database stale or absent); 4 refused.

An env that records on a control plane is read from it: its findings are the
control plane's (it scans the SBOMs each cut uploads, and rescans deployed
releases daily), which needs the vuln:read scope (exit 4 without it).`,
	}
	cmd.AddCommand(newVulnScanCmd(deps))
	cmd.AddCommand(newVulnGateCmd(deps))
	cmd.AddCommand(newVulnListCmd(deps))
	cmd.AddCommand(newVulnShowCmd(deps))
	cmd.AddCommand(newVulnSBOMCmd(deps))
	cmd.AddCommand(newVulnDBCmd(deps))
	cmd.AddCommand(newVulnExemptCmd(deps))
	cmd.AddCommand(newVulnPatchCmd(deps))
	return cmdutil.StrictGroup(cmd)
}

// vulnEnv is what the vuln verbs need to know about an env.
type vulnEnv struct {
	Name string
	// Endpoint is the control plane that keeps the env's ledger; "" for
	// this machine's.
	Endpoint string
	// Gate is the env's vuln_gate, resolved (the defaults when the env
	// declares none).
	Gate release.ShapeVulnGate
	// Project is the forge project the control plane files the env under
	// (hosted envs only).
	Project string
	// decl is the env's control-plane declaration (hosted envs only).
	decl *cloud.Declaration
	// entities is the env's render, nil for an env with no declaration:
	// where a hosted release's artifacts were pushed, and the registry
	// identity a read of them presents (vuln_hosted.go).
	entities *KCLEntities
	// GradedAs is the mode the warn-first rollout judges Gate.Mode at
	// (vuln_gate.go): audit for an enforce while the vuln-gate rule does
	// not gate the project yet. "" is the declared mode.
	GradedAs release.DeliveryMode
	// GradeNote says why GradedAs differs from the declared mode.
	GradeNote string
}

// hosted reports an env whose ledger is a control plane.
func (e vulnEnv) hosted() bool { return e.Endpoint != "" }

// resolveVulnEnv renders env's declaration once for its ledger and its
// vuln_gate. The ledger choice is selectLedger's rule — a forge.ControlPlane
// declaration keeps the ledger there unless FORGE_LEDGER=machine — read
// without resolving a credential, because these verbs only need to know
// WHERE, not to reach it.
func resolveVulnEnv(ctx context.Context, projectDir, env string) (vulnEnv, error) {
	out := vulnEnv{Name: env, Gate: release.DefaultDeliveryPolicy().VulnGate}
	selection, err := ledgerSelectionFromEnv()
	if err != nil {
		return out, err
	}
	if !envHasDeclaration(projectDir, env, filepath.Join(projectDir, "deploy", "kcl", env, "main.k")) {
		return out, nil
	}
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return out, fmt.Errorf("render deploy/kcl/%s: %w", env, err)
	}
	out.Gate = entities.DeliveryPolicy.Resolved().VulnGate
	grade := gradeVulnRules(projectDir).Vuln
	out.GradedAs = gradedMode(out.Gate.Mode, grade)
	if out.GradedAs != out.Gate.Mode {
		out.GradeNote = fmt.Sprintf("vuln_gate enforce is judged as audit: %s; gate now with %s", grade.Note(), grade.OptIn())
	}
	if decl := declarationFromEntities(entities); decl != nil && selection != ledgerSelectMachine {
		out.Endpoint = strings.TrimRight(strings.TrimSpace(decl.Endpoint), "/")
		if out.Endpoint == "" {
			out.Endpoint = "its control plane"
		}
		out.Project = hostedEnvRefFor(env, entities).Project
		out.decl = decl
		out.entities = entities
	}
	return out, nil
}

// releaseOf resolves the release a verb reads: --release, else the one the
// env's machine ledger says it runs.
func releaseOf(store *ledgerfile.Store, env, version string) (*release.Release, error) {
	if version == "" {
		p, ok, err := store.CurrentPromotion(env)
		if err != nil {
			return nil, undeterminedf("read env %q's promotions from %s: %v", env, store.Dir(), err)
		}
		if !ok {
			return nil, undeterminedf("env %q has no promotion in this machine's ledger (%s); name a release with --release <v>", env, store.Dir())
		}
		version = p.Release
	}
	rel, err := store.Release(version)
	if err != nil {
		return nil, undeterminedf("read release %s from %s: %v", version, store.Dir(), err)
	}
	if rel == nil {
		return nil, undeterminedf("release %s is not in this machine's ledger (%s)", version, store.Dir())
	}
	return rel, nil
}

// vulnExit turns a document's verdict into the exit code. A determined
// verdict that blocks nothing is success.
func vulnExit(doc vulnscan.Document) error {
	g := doc.Gate
	switch g.Verdict {
	case vulnscan.VerdictBlock:
		return &exitCodeError{code: exitWrong, verdict: true, msg: fmt.Sprintf("vuln_gate (%s) would block: %s", g.Mode, strings.Join(g.Blocking, ", "))}
	case vulnscan.VerdictStale:
		msg := fmt.Sprintf("could not determine: the findings come from a vulnerability database built %s, older than 5 days, and no current scan replaced them (forge vuln db pull, then forge vuln scan --release %s)", doc.DB.BuiltAt.Format("2006-01-02"), doc.Release)
		if len(g.Blocking) > 0 {
			msg += "; the old data would block: " + strings.Join(g.Blocking, ", ")
		}
		return &exitCodeError{code: exitUndetermined, verdict: true, msg: msg}
	case vulnscan.VerdictUnscanned:
		var names []string
		for _, u := range doc.Coverage.Unscanned {
			names = append(names, u.Artifact+" ("+u.Reason+")")
		}
		return &exitCodeError{code: exitUndetermined, verdict: true, msg: "could not determine: not scanned: " + strings.Join(names, ", ")}
	}
	return nil
}

// dbExit classifies a database or scanner failure: every one is "could not
// determine" (exit 2), never a pass.
func dbExit(err error) error {
	if err == nil {
		return nil
	}
	var ec *exitCodeError
	if errors.As(err, &ec) {
		return err
	}
	switch {
	case errors.Is(err, vulnscan.ErrScannerMissing):
		return undeterminedf("not scanned: %v", err)
	case errors.Is(err, vulnscan.ErrNoDB), errors.Is(err, vulnscan.ErrStale):
		return undeterminedf("not scanned: %v", err)
	}
	return undeterminedf("%v", err)
}

// writeVulnDocument prints a document as text. label names what was scanned
// when the document names no env or release (a directory, an image, a file).
func writeVulnDocument(w io.Writer, doc vulnscan.Document, now time.Time, label string) {
	subject := "release " + doc.Release
	switch {
	case doc.Env != "" && doc.Release != "":
		subject = doc.Env + " · release " + doc.Release
	case doc.Release == "":
		subject = label
	}
	db := "no scan"
	if !doc.DB.BuiltAt.IsZero() {
		db = fmt.Sprintf("%s %s, db built %s (%s ago)", doc.DB.Scanner, doc.DB.Version, doc.DB.BuiltAt.Format("2006-01-02 15:04Z"), roundAge(now.Sub(doc.DB.BuiltAt)))
		if doc.DB.Stale {
			db += " STALE"
		}
	}
	scanned := fmt.Sprintf("%d of %d artifacts scanned", doc.Coverage.Scanned, doc.Coverage.Artifacts)
	if na := doc.Coverage.NotApplicable; len(na) > 0 {
		reasons := make([]string, len(na))
		for i, a := range na {
			reasons[i] = a.Reason
		}
		scanned = fmt.Sprintf("%d of %d applicable artifacts scanned; %s", doc.Coverage.Scanned, doc.Coverage.Artifacts, notApplicableSummary(reasons))
	}
	_, _ = fmt.Fprintf(w, "%s · %s · %s\n", subject, scanned, db)
	gate := strings.ToUpper(string(doc.Gate.Verdict))
	if len(doc.Gate.Blocking) > 0 {
		gate += ": " + strings.Join(doc.Gate.Blocking, ", ")
	}
	mode := doc.Gate.Mode
	if doc.Gate.GradedAs != "" && doc.Gate.GradedAs != doc.Gate.Mode {
		mode += ", graded " + doc.Gate.GradedAs
	}
	_, _ = fmt.Fprintf(w, "vuln_gate (%s): %s\n", mode, gate)
	c := doc.Counts
	_, _ = fmt.Fprintf(w, "%d critical, %d high, %d medium, %d low, %d unknown\n", c.Critical, c.High, c.Medium, c.Low, c.Unknown)
	if len(doc.Findings) > 0 {
		_, _ = fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, f := range doc.Findings {
			action := ""
			if f.Action != vulnscan.ActionNone {
				action = strings.ToUpper(string(f.Action))
			}
			if f.Exemption != nil {
				action = "EXEMPT"
			}
			where := ""
			switch {
			case len(f.Artifacts) == 0:
				// Attributed to the release as a whole, to no artifact.
				where = "release"
			case doc.Coverage.Artifacts > 1:
				where = strings.Join(f.Artifacts, ", ")
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", action, f.ID, severityLabel(f.Finding), f.Package.Name+" "+f.Package.Version, fixLabel(f.Finding), where)
		}
		_ = tw.Flush()
	}
	for _, u := range doc.Coverage.Unscanned {
		_, _ = fmt.Fprintf(w, "not scanned: %s (%s)\n", u.Artifact, u.Reason)
	}
	for _, n := range doc.Notes {
		_, _ = fmt.Fprintf(w, "note: %s\n", n)
	}
	if doc.Env != "" && len(doc.Gate.Blocking) > 0 {
		_, _ = fmt.Fprintf(w, "→ forge vuln show %s %s\n", doc.Env, doc.Gate.Blocking[0])
	}
}

func severityLabel(f vulnscan.Finding) string {
	s := string(f.Severity)
	if f.KEV {
		s += " KEV"
	}
	if f.Confidence == vulnscan.ConfidenceLow {
		s += " (CPE match, low confidence)"
	}
	return s
}

func fixLabel(f vulnscan.Finding) string {
	switch {
	case f.Fixable() && len(f.FixedIn) > 0:
		return "fixed in " + strings.Join(f.FixedIn, ", ")
	case f.FixState == vulnscan.FixStateWontFix:
		return "won't fix"
	case f.FixState == vulnscan.FixStateNotFixed:
		return "no fix yet"
	default:
		return "fix unknown"
	}
}

// roundAge spells an age in the largest whole unit.
func roundAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
}
