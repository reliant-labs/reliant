package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/commitpolicy"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/doctor"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/linter/migrationlint"
)

func newCICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "CI helper commands — verify, scan, and validate in CI pipelines",
	}
	cmd.AddCommand(newCIVerifyGeneratedCmd())
	cmd.AddCommand(newCIVerifyTestRunCmd())
	cmd.AddCommand(newCITestBudgetCmd())
	cmd.AddCommand(newCIValidateKCLCmd())
	cmd.AddCommand(newCIVulnScanCmd())
	cmd.AddCommand(newCIMigrationSafetyCmd())
	cmd.AddCommand(newCIRunCmd())
	cmd.AddCommand(newCISummarizeCmd())
	cmd.AddCommand(newCIGateCmd())
	return cmdutil.StrictGroup(cmd)
}

func newCIVerifyGeneratedCmd() *cobra.Command {
	var noCache bool
	cmd := &cobra.Command{
		Use:   "verify-generated",
		Short: "Verify generated code is pristine and up to date",
		Long: "Three checks of the working tree as it stands (tracked files plus untracked\n" +
			"files git does not ignore — on a clean checkout, HEAD):\n" +
			"  1. Self-certification: every generated file's embedded forge:hash marker\n" +
			"     must verify (recompute vs embedded) — catches hand-edits that were\n" +
			"     committed without --force / forge project disown.\n" +
			"  2. Freshness: runs forge generate and verifies no files changed —\n" +
			"     catches stale generated code after an input (proto/forge.yaml) change.\n" +
			"     The regenerate skips its steps when this exact tree, forge binary,\n" +
			"     toolset and environment were already verified as generate's own\n" +
			"     output (see `forge generate --help`); --no-cache runs them anyway.\n" +
			"  3. Commit policy: no generated file is gitignored (check 2 cannot see an\n" +
			"     ignored file), and no machine-local state (.forge-kcl/, a frontend's\n" +
			"     dev public/config.js) is tracked.\n\n" +
			"All three run against a scratch copy of the checkout, made beside the repository\n" +
			"(`.<repo>.forge-verify-*`) and removed afterwards, so the checkout itself is never\n" +
			"written: other readers of it can run at the same time, and an interrupted run\n" +
			"leaves nothing behind. Every change it reports was made by forge generate.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := requireFeature(config.FeatureCI); err != nil {
				return err
			}
			root, err := projectRoot()
			if err != nil {
				return err
			}

			// Refuse a regenerate that would skip part of the tree. forge
			// generate only WARNS when a frontend's protoc-gen-es is missing
			// (a fresh checkout before `npm install` is a normal state
			// locally), and skips that frontend's TypeScript stubs. Here the
			// skip is a silent green: the committed stubs are never
			// regenerated, so nothing is compared, and the gate certifies a
			// tree it only half-checked.
			if store, err := loadProjectStore(); err == nil {
				if err := verifyTSPluginsResolvable(root, store.Config().Frontends); err != nil {
					return err
				}
			}

			// Every pass judges one scratch copy of the working tree, so
			// they agree on what they saw and the checkout is never written
			// (ci_verify_scratch.go). Ctrl-C cancels the regenerate and
			// still removes the copy.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			scratch, err := newVerifyScratch(ctx, root, os.Stderr)
			if err != nil {
				return fmt.Errorf("make a scratch copy of the checkout to verify: %w", err)
			}
			defer scratch.Remove()

			// Pass 1: recompute embedded hashes. A hand-edited generated
			// file fails HERE, by name, before the regenerate pass would
			// abort on the same files with a less CI-shaped message.
			cs, err := generator.LoadChecksums(scratch.root)
			if err != nil {
				return fmt.Errorf("load .forge ownership state: %w", err)
			}
			if drift := scanProjectDrift(scratch.root, cs); len(drift) > 0 {
				fmt.Fprintf(os.Stderr, "Error: %d generated file(s) were hand-edited after forge wrote them:\n", len(drift))
				for _, d := range drift {
					fmt.Fprintf(os.Stderr, "  - %s\n", d.Path)
				}
				fmt.Fprintln(os.Stderr, "Move the edits to a user-owned extension point (then regenerate), or `forge project disown <path> --reason \"<why>\"` to take ownership.")
				return fmt.Errorf("generated files failed self-certification")
			}

			// Pass 2: regenerate the copy and diff it against itself. The
			// diff covers modified, deleted AND created files: a regenerate
			// that CREATED a file (a new entity's ORM, a new service's
			// mounts) is drift too, though `git diff` would never show it.
			if err := scratchRegenerate(ctx, scratch, noCache, false); err != nil {
				return fmt.Errorf("forge generate failed: %w", err)
			}
			changed, err := scratch.changes(ctx)
			if err != nil {
				return err
			}
			if len(changed) > 0 {
				fmt.Fprintf(os.Stderr, "Error: generated code is out of date — %d file(s) changed when forge generate re-ran:\n", len(changed))
				for _, line := range changed {
					fmt.Fprintf(os.Stderr, "  %s\n", line)
				}
				fmt.Fprintln(os.Stderr, "Run 'forge generate' and commit the changes.")
				return fmt.Errorf("generated code is out of date (%d file(s))", len(changed))
			}
			scratch.copyCacheBack()

			// Pass 3: commit policy. The diff honours .gitignore, so it is
			// blind to generated code a project's (inherited) .gitignore
			// excludes — a regenerate that recreates an ignored file reports
			// "up to date" while every fresh clone fails to compile. Checked
			// AFTER the regenerate, so the generated set is complete.
			violations, err := commitpolicy.Check(scratch.root)
			if err != nil {
				return err
			}
			if len(violations) > 0 {
				fmt.Fprintf(os.Stderr, "Error: %d path(s) break forge's commit policy (generated code is committed; machine-local state is not):\n", len(violations))
				fmt.Fprint(os.Stderr, commitpolicy.Format(violations))
				return fmt.Errorf("commit policy violated (%d path(s))", len(violations))
			}

			// State what was verified. "up to date" over a project whose
			// ownership state never loaded (zero certified files) is a claim
			// about nothing; the count is what separates the two.
			certified := countSelfCertifiedFiles(scratch.root, cs)
			if certified == 0 {
				fmt.Println("⏭️  No self-certifying generated files found — nothing to verify.")
				fmt.Println("    (expected `Code generated by forge` markers; is this a forge project that has run `forge generate`?)")
				return nil
			}
			fmt.Printf("✅ Generated code is up to date — %d file(s) self-certified, regenerate produced no changes.\n", certified)
			return nil
		},
	}
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "Make the regenerate run every step, even when the committed tree is one a full forge generate already verified as its own output (FORGE_GENERATE_NO_CACHE=1 does the same)")
	return cmd
}

// scratchRegenerate is the regenerate verify-generated and generate --check
// run in their scratch copy. A seam for tests, which cannot re-execute the
// test binary as forge.
var scratchRegenerate = regenerateScratch

// regenerateScratch runs `forge generate` (this binary) in the scratch copy.
// Its output streams through; its environment is this process's minus any
// variable that would point git at the caller's repository.
func regenerateScratch(ctx context.Context, scratch *verifyScratch, noCache, skipValidate bool) error {
	parts, err := forgeExecCommand()
	if err != nil {
		return fmt.Errorf("resolve forge binary: %w", err)
	}
	genArgs := append(slices.Clone(parts[1:]), "generate")
	if noCache {
		genArgs = append(genArgs, "--no-cache")
	}
	if skipValidate {
		genArgs = append(genArgs, "--skip-validate")
	}
	genCmd := exec.CommandContext(ctx, parts[0], genArgs...)
	genCmd.Dir = scratch.root
	genCmd.Env = scratchEnv(os.Environ())
	genCmd.Stdout = os.Stdout
	genCmd.Stderr = os.Stderr
	return genCmd.Run()
}

func newCIValidateKCLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate-kcl",
		Short: "Validate that every environment renders manifests kubectl will accept",
		Long: "Renders each environment's deploy/kcl/<env>/main.k through the embedded KCL\n" +
			"runtime and asserts the result is DEPLOYABLE, not merely that it evaluates.\n\n" +
			"An env's applied stream is `output.manifests`, EXPANDED exactly as `forge env\n" +
			"deploy` expands it: every Cluster-bound forge.dev Workload record is rendered\n" +
			"through pkg/deploy.RenderWorkloads (Full profile), per (cluster, namespace)\n" +
			"set. A record that does not render fails here. Every expanded object must\n" +
			"carry apiVersion + kind, and no top-level key but `output` may hide k8s\n" +
			"objects no deploy would ever apply.\n\n" +
			"Hosting is PER WORKLOAD. The env's hosted part (forge.OnHosted workloads,\n" +
			"hosted databases, hosted frontends) is judged by its own deploy path:\n" +
			"each workload must pass Workload.Validate(ProfileRestricted), and the set must\n" +
			"render as the control plane renders it — the same plan `forge env deploy`\n" +
			"runs, minus the release and the RPCs.\n\n" +
			"Shares its implementation with `forge doctor --signal deploy`, so CI and\n" +
			"the doctor cannot disagree about whether a project can be deployed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := requireFeature(config.FeatureCI); err != nil {
				return err
			}

			// Source of truth for the env list is the filesystem
			// (deploy/kcl/<env>/main.k presence) — same discovery the
			// doctor check walks.
			projectDir := projectDirForKCL()
			envs, lerr := ListEnvs(projectDir)
			if lerr != nil {
				return fmt.Errorf("list envs: %w", lerr)
			}
			if len(envs) == 0 {
				fmt.Println("No environments declared (no deploy/kcl/<env>/main.k) — nothing to validate.")
				return nil
			}
			fmt.Printf("Validating %s ...\n", strings.Join(envs, ", "))

			// The shaper judges an env's HOSTED part by the deploy path that
			// ships it (the control plane admits its workloads) rather than
			// failing it for the empty manifest stream it renders by design.
			res := doctor.CheckDeployManifests(cmd.Context(), &doctor.Environment{ProjectDir: projectDir, DeployShaper: deployShapeOf})
			switch res.Status {
			case doctor.StatusFail:
				fmt.Fprintf(os.Stderr, "❌ %s\n", res.Message)
				if res.Evidence != "" {
					fmt.Fprintln(os.Stderr, res.Evidence)
				}
				return fmt.Errorf("KCL manifests are not applyable")
			case doctor.StatusSkip:
				fmt.Printf("⏭️  %s\n", res.Message)
				return nil
			default:
				fmt.Printf("✅ %s\n", res.Message)
				return nil
			}
		},
	}
}

func newCIMigrationSafetyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migration-safety",
		Short: "Run SQL migration safety checks based on forge.yaml config",
		Long:  "Checks SQL migrations for patterns that pass on empty databases but fail or lock populated databases.",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := loadProjectStore()
			if err != nil {
				return fmt.Errorf("load project config: %w", err)
			}
			if !store.Features().CIEnabled() {
				return config.DisabledFeatureError(config.FeatureCI)
			}
			if !store.Features().MigrationsEnabled() {
				return config.DisabledFeatureError(config.FeatureMigrations)
			}

			migrationsDir := store.Database().MigrationsDir
			if migrationsDir == "" {
				migrationsDir = filepath.Join("db", "migrations")
			}
			// Resolve against the project root, not the process cwd. The
			// configured path is project-relative, so running this from any
			// subdirectory used to stat a directory that does not exist —
			// and the pass reported "No migration safety warnings!" over a
			// tree it never opened. A gate whose verdict depends on where
			// you happened to be standing is not a gate.
			if !filepath.IsAbs(migrationsDir) {
				if root, rootErr := projectRoot(); rootErr == nil {
					migrationsDir = filepath.Join(root, migrationsDir)
				}
			}
			result, err := migrationlint.LintMigrationsDir(migrationsDir, migrationlint.ConfigFromProject(store.Database().MigrationSafety))
			if err != nil {
				return cliutil.WrapUserErr("forge ci migration-safety",
					"could not read the migrations directory "+migrationsDir, "",
					"check the path and permissions, or point database.migrations_dir in forge.yaml at the right directory", err)
			}
			fmt.Print(result.FormatText())
			if result.HasErrors() {
				return cliutil.UserErr("forge ci migration-safety",
					fmt.Sprintf("%d migration safety violation(s) in %s", len(result.Findings), result.Dir),
					"",
					migrationlint.PrimaryRemediation(result.Findings))
			}
			return nil
		},
	}
}

func newCIVulnScanCmd() *cobra.Command {
	var (
		flagGo  bool
		flagNPM bool
		flagAll bool
	)

	cmd := &cobra.Command{
		Use:   "vuln-scan",
		Short: "Run vulnerability scanners based on forge.yaml config",
		Long: "Runs govulncheck for Go and npm audit for frontends. Defaults to scanning everything\n" +
			"enabled in forge.yaml.\n\n" +
			"This is a GATE, so it never reports a pass it did not verify. If a selected scanner\n" +
			"cannot run — the binary is not on PATH, or the config selects no scanner at all —\n" +
			"the command FAILS and names the missing piece, rather than exiting 0 over a scan\n" +
			"that never happened. The success line names every scanner that actually ran.\n\n" +
			"Go fails on any advisory your code CALLS; npm fails on any advisory of severity\n" +
			"high or critical (lower ones are printed, not failed). An advisory with no fixed\n" +
			"version that cannot reach your code can be accepted under ci.vuln_scan.exemptions\n" +
			"in forge.yaml — by its GO-… id (govulncheck) or GHSA-… id (npm audit), with a\n" +
			"reason and an expiry date. Each entry accepts that one advisory and nothing else; an\n" +
			"expired or incomplete entry fails the gate, and one that matches nothing is reported\n" +
			"so it can be removed.",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := requireFeature(config.FeatureCI)
			if err != nil {
				return err
			}
			ci := store.CI()

			// If no specific flag set, default to --all behavior.
			if !flagGo && !flagNPM {
				flagAll = true
			}

			// No explicit scanner selection means "all enabled" (project
			// convention). Exemptions are excluded from that test: accepting
			// an advisory says nothing about which scanners to run.
			allEnabled := ci.VulnScan.UsesDefaultScanners()
			runGo := flagGo || (flagAll && (allEnabled || ci.VulnScan.Go))
			runNPM := flagNPM || (flagAll && (allEnabled || ci.VulnScan.NPM))

			// Every branch of the selection above can be false at once:
			// a forge.yaml that sets `ci.vuln_scan.docker: true` and nothing
			// else makes the config non-zero (so `allEnabled` is false)
			// while leaving both `go` and `npm` false. The pre-fix command
			// ran no scanner and printed `✅ Vulnerability scan passed.`
			// The whole point of the job is to be the thing that says no.
			if !runGo && !runNPM {
				return cliutil.UserErr("forge ci vuln-scan",
					"no scanner is selected, so nothing would be scanned — refusing to report a pass over an empty scan",
					"forge.yaml (ci.vuln_scan)",
					"set `ci.vuln_scan.go: true` and/or `ci.vuln_scan.npm: true` in forge.yaml (removing the whole `ci.vuln_scan` block enables both), or select one explicitly with `forge ci vuln-scan --go` / `--npm`")
			}

			// ran records the scanners that actually executed and passed,
			// so the success line can name them; failed names the ones
			// that did not pass, so the failure can. A scanner that could
			// not run contributes an error, never a silent absence.
			var ran, failed []string

			// One exemption list serves both scanners; each entry's ID
			// says which one it belongs to. An entry neither scanner
			// reports can accept nothing, and fails the gate the way a
			// malformed entry does.
			exemptions, unroutable := partitionExemptions(ci.VulnScan.Exemptions)
			for _, m := range unroutable {
				fmt.Fprintf(os.Stderr, "❌ exemption not honored: %s\n", m)
			}
			if len(unroutable) > 0 {
				failed = append(failed, fmt.Sprintf("%d unusable exemption(s) in forge.yaml", len(unroutable)))
			}

			if runGo {
				what, err := ciRunGovulncheck(cmd.Context(), exemptions[ecosystemGo])
				switch {
				case err != nil && errors.Is(err, errScannerUnavailable):
					// Cannot verify ⇒ cannot pass. Hard-fail with the
					// install command rather than downgrading the gate to a
					// warning the CI log will bury.
					return err
				case err != nil:
					fmt.Fprintf(os.Stderr, "❌ govulncheck failed: %v\n", err)
					failed = append(failed, "govulncheck")
				default:
					ran = append(ran, what)
				}
			}

			if runNPM {
				what, err := ciRunNPMAudit(cmd.Context(), store.Config(), exemptions[ecosystemNPM], time.Now())
				switch {
				case err != nil && errors.Is(err, errScannerUnavailable):
					// Only fatal when the user asked for npm explicitly; a
					// project with no frontends legitimately has nothing for
					// npm audit to look at, and the default selection should
					// not fail it for that.
					if flagNPM {
						return err
					}
					fmt.Fprintf(os.Stderr, "⏭️  npm audit: %v\n", err)
				case err != nil:
					fmt.Fprintf(os.Stderr, "❌ npm audit failed: %v\n", err)
					failed = append(failed, "npm audit")
				default:
					ran = append(ran, what)
				}
			}

			if len(failed) > 0 {
				return cliutil.UserErr("forge ci vuln-scan",
					"the vulnerability gate failed: "+strings.Join(failed, ", ")+" (see the output above)",
					"",
					"upgrade the affected dependency (`go get -u <module>` / bump it in package.json, or an npm "+
						"`overrides` entry when only a transitive dependency has the fix). If an advisory has NO fixed "+
						"version and cannot reach your code, accept it explicitly under `ci.vuln_scan.exemptions` in "+
						"forge.yaml — `id` (the GO-… id govulncheck printed, or the GHSA-… id npm audit printed), "+
						"`reason` (why it cannot affect you) and `expires` (YYYY-MM-DD) — which suppresses that "+
						"advisory only")
			}
			if len(ran) == 0 {
				return cliutil.UserErr("forge ci vuln-scan",
					"every selected scanner was skipped, so nothing was actually scanned",
					"",
					"install the missing scanners (`go install golang.org/x/vuln/cmd/govulncheck@latest`), or narrow the selection to one that can run here")
			}

			fmt.Printf("✅ Vulnerability scan passed — %s.\n", strings.Join(ran, "; "))
			return nil
		},
	}

	cmd.Flags().BoolVar(&flagGo, "go", false, "Run govulncheck only")
	cmd.Flags().BoolVar(&flagNPM, "npm", false, "Run npm audit only")
	cmd.Flags().BoolVar(&flagAll, "all", false, "Run all scanners enabled in forge.yaml (default)")

	return cmd
}

// errScannerUnavailable marks "this scanner could not run at all", as
// distinct from "this scanner ran and found problems". The caller must
// treat the two differently: a scanner that found nothing is evidence,
// and a scanner that never ran is the absence of evidence. Collapsing
// them into `return nil` is what let `✅ Vulnerability scan passed.`
// print over a machine with no govulncheck installed.
var errScannerUnavailable = errors.New("scanner unavailable")

// unavailable tags err as an errScannerUnavailable case while keeping its
// user-facing text verbatim — the caller needs the classification, the
// user needs the actionable sentence, and neither should cost the other.
type unavailable struct{ err error }

func (u unavailable) Error() string        { return u.err.Error() }
func (u unavailable) Is(target error) bool { return target == errScannerUnavailable }
func (u unavailable) Unwrap() error        { return u.err }

// ciRunGovulncheck runs govulncheck and returns a short description of
// what it scanned (for the caller's success line), or an error.
//
// It runs in JSON mode rather than text mode, which moves the pass/fail
// decision from govulncheck's exit code into forge. That is what makes
// ci.vuln_scan.exemptions possible: govulncheck has no allowlist, so an
// advisory with no fixed version would otherwise leave only a permanently
// red gate or no gate at all. See ci_vuln_exempt.go for the rules that
// keep an exemption from being a blanket bypass.
//
// The trade-off is that JSON mode exits 0 on findings, so a bug here reads
// as a pass. Everything that could go wrong — the scanner missing, the
// process failing, output that will not parse — is therefore an explicit
// error below, never a fall-through.
func ciRunGovulncheck(ctx context.Context, exemptions []config.CIVulnExemption) (string, error) {
	if _, err := exec.LookPath("govulncheck"); err != nil {
		return "", unavailable{cliutil.UserErr("forge ci vuln-scan",
			"govulncheck is not on PATH, so the Go vulnerability gate cannot run — refusing to report a pass it did not verify",
			"",
			"install it with `go install golang.org/x/vuln/cmd/govulncheck@latest` (forge's generated .github/workflows/ci.yml installs a pinned version in the vuln-scan job, and scripts/bootstrap.sh installs it locally), then re-run")}
	}

	fmt.Println("Running govulncheck ./...")
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "govulncheck", "-format", "json", "./...")
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// In JSON mode a non-zero exit is a scanner failure (bad flags,
		// a build error in the module, a panic), NOT a findings report.
		return "", fmt.Errorf("govulncheck did not complete: %w", err)
	}

	findings, err := parseGovulncheckJSON(&stdout)
	if err != nil {
		return "", err
	}

	res := evaluateVulnFindings(findings, exemptions, time.Now())
	res.report(os.Stdout)

	if len(res.Blocking) > 0 {
		return "", fmt.Errorf("govulncheck found %d vulnerability/ies your code calls", len(res.Blocking))
	}
	// A malformed or expired exemption is a failure in its own right,
	// even when nothing is blocking: it means the file claims an
	// acceptance the gate did not honor, and silence there would let the
	// claim and the behavior drift apart.
	if len(res.Malformed) > 0 {
		return "", fmt.Errorf("%d vulnerability exemption(s) in forge.yaml could not be honored", len(res.Malformed))
	}

	what := "govulncheck (Go modules + stdlib)"
	if len(res.Accepted) > 0 {
		what += fmt.Sprintf(", %d accepted exemption(s)", len(res.Accepted))
	}
	return what, nil
}

// countSelfCertifiedFiles reports how many forge-owned files carry a
// verifiable `Code generated by forge` marker in this checkout — i.e. how
// many files pass 1 of verify-generated actually examined. Disowned paths
// are excluded: they are user-owned by recorded intent and are not part of
// the claim.
func countSelfCertifiedFiles(root string, cs *generator.FileChecksums) int {
	n := 0
	for relPath := range checksums.ScanMarkers(root) {
		if cs != nil && cs.IsDisowned(relPath) {
			continue
		}
		n++
	}
	if cs != nil {
		for relPath := range cs.Unstampable {
			if !cs.IsDisowned(relPath) {
				n++
			}
		}
	}
	return n
}

// verifyTSPluginsResolvable returns an error naming every frontend whose
// TypeScript stubs `forge generate` would silently skip because the local
// protoc-gen-es plugin its buf.gen.yaml runs is not installed. Frontends that
// generate no TypeScript, or use a remote plugin, are not its concern.
func verifyTSPluginsResolvable(root string, frontends []config.FrontendConfig) error {
	var missing []string
	for _, feDir := range localTSPluginFrontendDirs(root, frontends) {
		if _, ok := resolveLocalTSPluginRel(root, feDir); !ok {
			missing = append(missing, feDir)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	for _, dir := range missing {
		fmt.Fprintf(os.Stderr, "Error: %s: @bufbuild/protoc-gen-es is not installed, so its TypeScript stubs cannot be regenerated — run `npm ci` in %s first.\n", dir, dir)
	}
	return fmt.Errorf("cannot verify generated code: %d frontend(s) have no protoc-gen-es (%s)", len(missing), strings.Join(missing, ", "))
}

// localTSPluginFrontendDirs returns the project-relative, slash-separated
// directory of every frontend whose TypeScript stubs `forge generate`
// produces with the LOCAL protoc-gen-es — the frontends that need the plugin
// in node_modules. Frontends that generate no TypeScript, or whose
// buf.gen.yaml uses a remote plugin, are excluded.
func localTSPluginFrontendDirs(root string, frontends []config.FrontendConfig) []string {
	var dirs []string
	for _, fe := range frontends {
		if !generatesTypeScript(fe.Type) {
			continue
		}
		feDir, ok := fe.Dir(root)
		if !ok {
			continue
		}
		bufGen := filepath.Join(root, feDir, "buf.gen.yaml")
		if _, err := os.Stat(bufGen); err != nil || !usesLocalTSPlugin(bufGen) {
			continue
		}
		dirs = append(dirs, filepath.ToSlash(feDir))
	}
	return dirs
}

// generatesTypeScript mirrors stepFrontendBufTS's frontend-type gate: the
// types whose stubs `forge generate` produces with buf + protoc-gen-es.
func generatesTypeScript(feType string) bool {
	switch strings.ToLower(feType) {
	case "nextjs", "react-native", "vite-spa":
		return true
	}
	return false
}
