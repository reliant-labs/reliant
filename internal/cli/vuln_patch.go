package cli

// `forge vuln patch`: bump vulnerable dependencies to the lowest version that
// fixes them, with the ecosystem's own tool, verify, and optionally open the
// pull request. The patcher is internal/vulnpatch; this file is the input
// (an env's findings, or a scan of a directory), flags, output and exit codes.
//
//	forge vuln patch <env|dir> [<advisory-id>...] [--ecosystem go|npm|pnpm|yarn|docker]
//	                 [--allow-major] [--verify[=<cmd>]] [--pr | --no-pr]
//	                 [--branch <name>] [--json]
//
// Exit codes (the patch column of the vuln table, exitcodes.go): 0 every
// requested finding bumped (and verified, with --verify); 1 a bump or the
// verification failed — the signal the vuln-patch workflow hands the tree to
// an agent on; 2 nothing deterministic to do for at least one requested
// finding (an ambiguous manifest or Dockerfile, an ecosystem with no
// patcher, no docker to verify a rebuilt image), or the findings could not
// be read; 4 refused (a dirty tree with
// --pr, or a named advisory whose fix needs a major bump without
// --allow-major) — nothing was changed.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/vulnpatch"
	"github.com/reliant-labs/forge/internal/vulnscan"
	"github.com/reliant-labs/forge/pkg/release"
)

// verifyGate is --verify with no value: the project's own gate.
const verifyGate = "gate"

type vulnPatchOptions struct {
	ecosystems []string
	allowMajor bool
	verify     string
	pr         bool
	noPR       bool
	branch     string
	asJSON     bool
}

// patchFindings is what a findings source hands the patcher: the release
// read, its findings judged by the env's vuln_gate (exemptions marked), and
// what the reader should know.
type patchFindings struct {
	Release  string
	Findings []vulnscan.DocumentFinding
	Notes    []string
}

// vulnPatchDeps are the patcher's seams, beside vuln's own.
type vulnPatchDeps struct {
	vulnDeps
	// findingsFor reads an env's findings for its current release. The
	// default (envPatchFindings) reads the env's own ledger: this machine's
	// (machineLedgerPatchFindings), or the control plane's findings for an
	// env that records there (hostedPatchFindings).
	findingsFor func(ctx context.Context, deps vulnPatchDeps, projectDir string, env vulnEnv) (patchFindings, error)
	// exec runs go, npm, pnpm, yarn, the verify command, git and gh.
	exec vulnscan.Runner
}

func newVulnPatchCmd(deps vulnDeps) *cobra.Command {
	return newVulnPatchCmdWith(vulnPatchDeps{vulnDeps: deps, findingsFor: envPatchFindings, exec: vulnscan.Exec})
}

func newVulnPatchCmdWith(deps vulnPatchDeps) *cobra.Command {
	var opts vulnPatchOptions
	cmd := &cobra.Command{
		Use:   "patch <env | dir> [<advisory-id>...]",
		Short: "Bump vulnerable dependencies, base images and the Go toolchain to their fix, and verify the result",
		Long: `Bump each vulnerable dependency to the lowest version that fixes it, staying in
its major version, with the ecosystem's own tool so lockfiles stay consistent:

  go     go get <module>@<fix> && go mod tidy, in every module (go.work aware)
         that requires it
  npm    npm install <pkg>@<fix> (direct), or an "overrides" entry (transitive)
  pnpm   pnpm update <pkg>@<fix>, or "pnpm.overrides"
  yarn   yarn up <pkg>@<fix> (yarn upgrade on yarn 1), or "resolutions"
  docker an OS package (deb, apk, rpm): the FROM of the Dockerfile whose final
         stage built the image moves to the newest tag of its minor
         (alpine:3.19.1 -> alpine:3.19.4), else to its tag's newest digest
  go     the Go standard library: the toolchain directive of each go.mod
         (go mod edit -toolchain=) and the golang builder image's tag

<env> patches the findings of the release the env runs, read from this
machine's ledger (` + "`forge vuln list <env>`" + ` shows them); a directory (".", "./web")
is scanned first and patched in place. With no advisory ids, every finding with
a fix that no exemption covers is patched; named ids (a CVE finds its GHSA) are
patched whatever their exemption.

Nothing is guessed. A finding the tree cannot be tied to one manifest (or to
one Dockerfile), a Python package, or a fix only in a new major is reported as
skipped with the reason: the vuln-patch workflow's agent picks those up.
--allow-major takes a major bump; for a base image or the Go toolchain, a new
minor (alpine 3.20, Go 1.23) already counts as one.

--verify runs the project's gate after the bump (vuln.patch.verify in
forge.yaml, else ` + "`task test:short`" + ` when the Taskfile has it, else go build and go test
-short, else the package manager's test script; --verify=<cmd> runs <cmd>),
then rescans each changed manifest's directory, and rebuilds each changed
Dockerfile (docker build --pull --no-cache, the project as context) and rescans
its image: a finding still reported is still_present. With no docker, a
Dockerfile fix is skipped (no docker to verify).

--pr refuses a tree with uncommitted changes, and, when the bump verified,
commits it on forge/vuln-patch/<date>-<commit>, pushes, and opens the pull
request with gh as you. --no-pr (the default) leaves the tree changed.

Exit codes: 0 every requested finding bumped (and verified); 1 a bump or the
verification failed (an agent takes over); 2 nothing deterministic to do for at
least one finding, or the findings could not be read; 4 refused (a dirty tree
with --pr, or a named advisory whose fix needs --allow-major), nothing changed.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVulnPatch(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), deps, args[0], args[1:], opts)
		},
	}
	cmd.Flags().StringSliceVar(&opts.ecosystems, "ecosystem", nil, "patch only these ecosystems: go, npm, pnpm, yarn, docker (repeatable, or comma-separated)")
	cmd.Flags().BoolVar(&opts.allowMajor, "allow-major", false, "take a fix in a new major version when no fix is in the installed one")
	cmd.Flags().Var((*optionalString)(&opts.verify), "verify", "after the bump, run the project's gate (or --verify=CMD) and rescan the changed manifests")
	cmd.Flags().Lookup("verify").NoOptDefVal = verifyGate
	cmd.Flags().BoolVar(&opts.pr, "pr", false, "commit on a new branch, push, and open a pull request with gh (refuses a dirty tree)")
	cmd.Flags().BoolVar(&opts.noPR, "no-pr", false, "leave the tree changed and open nothing (the default)")
	cmd.Flags().StringVar(&opts.branch, "branch", "", "with --pr, the branch to commit on (default forge/vuln-patch/<date>-<commit>)")
	cmd.Flags().BoolVar(&opts.asJSON, "json", false, "print the forge.dev/vuln-patch/v1 document (forge project schema vuln-patch)")
	return cmd
}

// optionalString is a string flag whose value is optional (--verify, or
// --verify=<cmd>): it names no value type in help, which would read as a
// required one.
type optionalString string

func (s *optionalString) String() string     { return string(*s) }
func (s *optionalString) Set(v string) error { *s = optionalString(v); return nil }
func (s *optionalString) Type() string       { return "" }

// isPatchDir reports whether patch's first argument names a directory rather
// than an env: env names never hold a path separator.
func isPatchDir(arg string) bool {
	return arg == "." || arg == ".." || strings.ContainsAny(arg, `/\`)
}

func runVulnPatch(ctx context.Context, out, errOut io.Writer, deps vulnPatchDeps, target string, ids []string, opts vulnPatchOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.pr && opts.noPR {
		return fmt.Errorf("--pr and --no-pr contradict each other")
	}
	if opts.branch != "" && !opts.pr {
		return fmt.Errorf("--branch names the branch --pr commits on; add --pr")
	}
	var ecos []vulnpatch.Ecosystem
	for _, e := range opts.ecosystems {
		eco := vulnpatch.Ecosystem(strings.ToLower(strings.TrimSpace(e)))
		ok := false
		for _, known := range vulnpatch.Ecosystems() {
			ok = ok || eco == known
		}
		if !ok {
			return fmt.Errorf("--ecosystem %q (expected one of %s)", e, ecosystemList())
		}
		ecos = append(ecos, eco)
	}

	projectDir := deps.projectDir()
	tree, envName := projectDir, ""
	if isPatchDir(target) {
		abs, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return fmt.Errorf("patch %s: not a directory", target)
		}
		tree = abs
	} else {
		envName = target
	}

	// The database comes first when it is needed (a directory scan, or the
	// rescan --verify runs): a machine that cannot scan must say so before
	// the tree changes, not after.
	var scanner *vulnscan.Scanner
	if envName == "" || opts.verify != "" {
		db, err := deps.openDB(errOut, true)
		if err != nil {
			return dbExit(err)
		}
		if _, err := db.Ensure(ctx, vulnscan.EnsureOptions{Fetch: true}); err != nil {
			return dbExit(err)
		}
		scanner = &vulnscan.Scanner{DB: db}
	}

	var in patchFindings
	if envName != "" {
		env, err := deps.resolveEnv(ctx, projectDir, envName)
		if err != nil {
			return undeterminedf("env %q: %v", envName, err)
		}
		if in, err = deps.findingsFor(ctx, deps, projectDir, env); err != nil {
			return err
		}
	} else {
		scan, err := scanner.Scan(ctx, "dir:"+tree)
		if err != nil {
			return dbExit(err)
		}
		doc := vulnscan.BuildDocument(vulnscan.DocumentInput{
			Gate: release.DefaultDeliveryPolicy().VulnGate, Artifacts: []vulnscan.ArtifactScan{{Artifact: target, Latest: scan}},
			Filter: vulnscan.Filter{IncludeExempt: true}, Now: deps.now(),
		})
		in.Findings, in.Notes = doc.Findings, doc.Notes
	}

	findings, missing, notes := selectPatchFindings(in, ids)
	plan, err := vulnpatch.NewPlanContext(ctx, tree, findings, vulnpatch.Options{AllowMajor: opts.allowMajor, Ecosystems: ecos, Explicit: len(ids) > 0})
	if err != nil {
		return err
	}
	for _, id := range missing {
		plan.Missing(id, fmt.Sprintf("not among the findings of %s", subjectOf(envName, in.Release, target)))
	}
	plan.Notes = append(append(in.Notes, notes...), plan.Notes...)

	if opts.pr && plan.Changes() && len(plan.Refusals) == 0 {
		if reason := dirtyTree(ctx, deps.exec, tree); reason != "" {
			plan.Refuse(reason)
		}
	}
	if len(plan.Refusals) == 0 {
		if err := plan.Apply(ctx, deps.exec); err != nil {
			return err
		}
		if opts.verify != "" && plan.Changes() {
			command, source := verifyCommand(opts.verify, projectDir, tree)
			plan.Verify(ctx, vulnpatch.VerifyOptions{
				Command: command, Source: source, Run: deps.exec,
				Rescan: func(ctx context.Context, dir string) ([]vulnscan.Finding, error) {
					s, err := scanner.Scan(ctx, "dir:"+dir)
					if err != nil {
						return nil, err
					}
					return s.Findings, nil
				},
				ScanImage: func(ctx context.Context, image string) ([]vulnscan.Finding, error) {
					s, err := scanner.Scan(ctx, "docker:"+image)
					if err != nil {
						return nil, err
					}
					return s.Findings, nil
				},
			})
		}
	}
	doc := plan.Document(envName, in.Release, opts.allowMajor)
	if opts.pr && doc.ExitCode != vulnpatch.ExitRefused {
		pr, note, err := openPatchPR(ctx, deps, tree, projectDir, doc, opts.branch)
		if err != nil {
			return err
		}
		doc.PR = pr
		if note != "" {
			doc.Notes = append(doc.Notes, note)
		}
	}

	if opts.asJSON {
		if err := writeVulnJSON(out, doc); err != nil {
			return err
		}
	} else {
		writePatchDocument(out, doc)
	}
	return patchExit(doc)
}

func ecosystemList() string {
	var out []string
	for _, e := range vulnpatch.Ecosystems() {
		out = append(out, string(e))
	}
	return strings.Join(out, ", ")
}

// selectPatchFindings picks the findings to patch: those ids name (by id or
// alias), or, with no ids, every finding with a fix that no exemption
// covers. It returns the ids that named nothing.
func selectPatchFindings(in patchFindings, ids []string) (out []vulnscan.Finding, missing, notes []string) {
	if len(ids) == 0 {
		unfixable, exempt := 0, 0
		for _, f := range in.Findings {
			switch {
			case f.Exemption != nil:
				exempt++
			case !f.Fixable():
				unfixable++
			default:
				out = append(out, f.Finding)
			}
		}
		if unfixable > 0 {
			notes = append(notes, fmt.Sprintf("%d finding(s) with no fixed version left out (name one to see why)", unfixable))
		}
		if exempt > 0 {
			notes = append(notes, fmt.Sprintf("%d exempt finding(s) left out (name one to patch it anyway)", exempt))
		}
		return out, nil, notes
	}
	seen := map[string]bool{}
	for _, id := range ids {
		hit := false
		for _, f := range in.Findings {
			if f.Names(id) {
				hit = true
				if !seen[f.Key()] {
					seen[f.Key()] = true
					out = append(out, f.Finding)
				}
			}
		}
		if !hit {
			missing = append(missing, id)
		}
	}
	return out, missing, nil
}

func subjectOf(env, release, dir string) string {
	switch {
	case env != "" && release != "":
		return env + " release " + release
	case env != "":
		return env
	}
	return "the scan of " + dir
}

// verifyCommand resolves --verify: a command given on the flag, else
// forge.yaml's vuln.patch.verify, else the tree's own gate.
func verifyCommand(flag, projectDir, tree string) (command, source string) {
	if flag != verifyGate {
		return flag, vulnpatch.VerifySourceFlag
	}
	if c := strings.TrimSpace(config.ReadVulnConfig(projectDir).Patch.Verify); c != "" {
		return c, vulnpatch.VerifySourceConfig
	}
	return vulnpatch.DefaultVerify(tree)
}

// envPatchFindings reads an env's findings from the ledger it records in, as
// `forge vuln list` does: the control plane's for an env whose ledger is a
// control plane, else this machine's.
func envPatchFindings(ctx context.Context, deps vulnPatchDeps, projectDir string, env vulnEnv) (patchFindings, error) {
	if env.hosted() {
		return hostedPatchFindings(ctx, deps.vulnDeps, env)
	}
	return machineLedgerPatchFindings(ctx, deps, projectDir, env)
}

// machineLedgerPatchFindings reads an env's findings the way `forge vuln
// list` does: its current release from this machine's ledger, rescanned
// when the local database is newer than its stored scan. It never downloads.
func machineLedgerPatchFindings(ctx context.Context, deps vulnPatchDeps, projectDir string, env vulnEnv) (patchFindings, error) {
	var out patchFindings
	store, err := deps.machineLedger(projectDir)
	if err != nil {
		return out, undeterminedf("open this machine's ledger: %v", err)
	}
	rel, err := releaseOf(store, env.Name, "")
	if err != nil {
		return out, err
	}
	var scanner *vulnscan.Scanner
	var local vulnscan.Status
	if db, err := deps.openDB(io.Discard, false); err == nil {
		if local, err = db.Status(ctx); err == nil && local.Present {
			scanner = &vulnscan.Scanner{DB: db}
		}
	}
	rs, err := vulnscan.ScanRelease(ctx, store, scanner, local, rel, vulnscan.RescanIfNewer)
	if err != nil {
		return out, undeterminedf("%v", err)
	}
	doc := vulnscan.BuildDocument(vulnscan.DocumentInput{
		Env: env.Name, Release: rel.Version, Gate: env.Gate, Artifacts: rs.Artifacts,
		Filter: vulnscan.Filter{IncludeExempt: true}, Now: deps.now(), Notes: rs.Notes,
	})
	if doc.Coverage.Scanned == 0 {
		return out, undeterminedf("nothing to patch from: no artifact of %s release %s has a scan (forge vuln scan --release %s)", env.Name, rel.Version, rel.Version)
	}
	// The patcher reads findings, never the gate's verdict: a stale scan
	// (vulnscan.ScanIsCurrent) leaves the verdict undetermined, but what it
	// found is still worth bumping. The document's notes say why the data
	// is what it is, and are carried as they are.
	out.Release, out.Findings = rel.Version, doc.Findings
	out.Notes = append(out.Notes, doc.Notes...)
	for _, u := range doc.Coverage.Unscanned {
		out.Notes = append(out.Notes, fmt.Sprintf("%s was not scanned (%s): its findings are not patched", u.Artifact, u.Reason))
	}
	if doc.DB.Stale {
		out.Notes = append(out.Notes, fmt.Sprintf("the findings to patch come from a vulnerability database built %s, older than 5 days: a current scan may report more (forge vuln db pull)", doc.DB.BuiltAt.Format("2006-01-02")))
	}
	return out, nil
}

// patchExit turns the document's exit code into the command's error.
func patchExit(doc vulnpatch.Document) error {
	s := doc.Summary
	counts := fmt.Sprintf("%d fixed, %d bumped, %d still present, %d failed, %d skipped", s.Fixed, s.Bumped, s.StillPresent, s.Failed, s.Skipped)
	switch doc.ExitCode {
	case vulnpatch.ExitOK:
		return nil
	case vulnpatch.ExitVerify:
		why := "a bump failed"
		if v := doc.Verify; v != nil && !v.Passed {
			why = "verification failed (" + v.Command + ")"
		} else if v != nil && v.RescanError != "" {
			why = "the rescan could not confirm the fix"
		} else if s.StillPresent > 0 {
			why = "the rescan still reports a finding"
		}
		return &exitCodeError{code: exitWrong, verdict: true, msg: "patched, but " + why + ": " + counts}
	case vulnpatch.ExitNothingToDo:
		return &exitCodeError{code: exitUndetermined, verdict: true, msg: "nothing deterministic to do for some findings: " + counts}
	case vulnpatch.ExitRefused:
		return &exitCodeError{code: exitRefused, verdict: true, msg: "refused, nothing changed: " + strings.Join(doc.Refusals, "; ")}
	}
	return &exitCodeError{code: doc.ExitCode, msg: counts}
}

func writePatchDocument(w io.Writer, doc vulnpatch.Document) {
	subject := doc.Dir
	if doc.Env != "" {
		subject = doc.Env + " · release " + doc.Release
	}
	_, _ = fmt.Fprintf(w, "%s · %d finding(s)\n", subject, len(doc.Findings))
	if len(doc.Findings) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, r := range doc.Findings {
			change := r.Installed
			if r.Target != "" {
				change += " → " + r.Target
			}
			detail := strings.Join(r.Manifests, ", ")
			if r.Reason != "" {
				detail = r.Reason
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s %s\t%s\n", strings.ToUpper(string(r.Status)), r.ID, r.Package.Name, change, detail)
		}
		_ = tw.Flush()
	}
	for _, b := range doc.Bumps {
		if b.Error != "" {
			_, _ = fmt.Fprintf(w, "\nfailed in %s:\n%s\n", b.Manifest, b.Error)
		}
	}
	if v := doc.Verify; v != nil {
		if v.Command != "" {
			state := "passed"
			if !v.Passed {
				state = "FAILED"
			}
			_, _ = fmt.Fprintf(w, "\nverify (%s): %s %s\n", v.Source, v.Command, state)
			if !v.Passed && v.Output != "" {
				_, _ = fmt.Fprintln(w, v.Output)
			}
		}
		if v.RescanError != "" {
			_, _ = fmt.Fprintf(w, "rescan failed: %s\n", v.RescanError)
		}
	}
	if doc.PR != nil && doc.PR.URL != "" {
		_, _ = fmt.Fprintf(w, "\npull request: %s\n", doc.PR.URL)
	}
	for _, n := range doc.Notes {
		_, _ = fmt.Fprintf(w, "note: %s\n", n)
	}
	for _, n := range doc.Refusals {
		_, _ = fmt.Fprintf(w, "refused: %s\n", n)
	}
	if !doc.Changed {
		_, _ = fmt.Fprintln(w, "the tree was not changed")
	}
}

// patchProjectName is forge.yaml's name, "" when there is none.
func patchProjectName(projectDir string) string {
	data, err := os.ReadFile(filepath.Join(projectDir, "forge.yaml"))
	if err != nil {
		return ""
	}
	var doc struct {
		Name string `yaml:"name"`
	}
	_ = yaml.Unmarshal(data, &doc)
	return strings.TrimSpace(doc.Name)
}
