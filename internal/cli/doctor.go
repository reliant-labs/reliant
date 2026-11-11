package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/doctor"
)

// newDoctorCmd builds `forge doctor` — "is this PROJECT healthy and
// well-formed", and nothing else.
//
// Doctor used to straddle two questions and take a `--env string (default
// "dev")` for the second one, so running it in a project silently reported
// on an environment the user never named. Worse, it answered that half
// badly: `App Health` guessed the app was on :8080, missed a project serving
// on :3091, and rendered the miss as a gray dash — indistinguishable from
// "not applicable".
//
// The runtime half now belongs to `forge env status <env>`, which already
// resolves the ports `forge env up` actually bound and reports holder pid,
// forge-ownership, build freshness and duplicate servers on top. There is
// one resolver for "what port does this component serve on", and it is not
// in doctor. What remains here spans EVERY declared environment, needs
// nothing running, and is therefore answerable in CI.
func newDoctorCmd() *cobra.Command {
	var (
		jsonOutput bool
		verbose    bool
		timeout    time.Duration
		signal     string
	)

	cmd := &cobra.Command{
		Use: "doctor",
		// A stray positional used to run the full suite and exit 0. Doctor
		// takes no arguments; a typo must fail rather than be swallowed.
		Args:  cobra.NoArgs,
		Short: "Check that the PROJECT is well-formed (deployability, payload caps, tooling, cluster capability)",
		Long: `Check that this project is healthy and well-formed.

Everything doctor checks is answerable from the project's own artefacts,
across every declared environment, with nothing running:

  * deployability of the rendered manifests — probes, resource requests
    and limits, credentials sourced from Secrets, ServiceAccounts bound to
    pods, a way to apply pending migrations,
  * Connect payload caps,
  * disowned generated files and dead forge.yaml keys,
  * the host tools forge shells out to (and their minimum versions),
  * cluster CAPABILITY: does the cluster carry the GatewayClass /
    ClusterIssuer this project's manifests require.

Doctor takes no --env, because it never asks about one.

For "is the stack for environment X up and reachable" — the app's
/healthz, pprof, the compose infra, the telemetry backends, Delve — use:

  forge env status <env>                 # host services + frontends + runtime checks
  forge env status <env> --signal traces # one signal only

That command resolves the ports the stack actually bound (rather than
guessing a default), and reports the holder pid, whether the process is
forge-owned, whether its build is stale against HEAD, and whether two
processes are serving one service.

Examples:
  forge doctor                  # Check the project
  forge doctor --json           # Machine-readable output
  forge doctor --verbose        # Show evidence for passing checks
  forge doctor --signal deploy  # Deployability gate only (the CI arm)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(jsonOutput, verbose, timeout, signal)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output results as JSON")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Show evidence for all checks (not just failures)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Overall timeout for all checks")
	// No backticks in a usage string: cobra reads the first backticked span
	// as the flag's argument-name placeholder.
	cmd.Flags().StringVar(&signal, "signal", "", "Run only the deployability gate (deploy). Environment-runtime signals live on 'forge env status <env> --signal'.")

	// Subcommands. `parity` is the host-mode vs cluster-mode env+config
	// divergence detector — surfaces "local wasn't representative of
	// prod" bugs before deploy.
	cmd.AddCommand(newDoctorParityCmd())

	return cmd
}

func runDoctor(jsonOutput, verbose bool, timeout time.Duration, signal string) error {
	store, err := loadProjectStore()
	if err != nil {
		return err
	}
	cfg := store.Config()

	// The project directory is where forge.yaml (and docker-compose.yml) live.
	configPath, err := findProjectConfigFile()
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(configPath)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// DeployShaper: the deploy checks judge each env by the path that ships
	// it — the same one `forge ci validate-kcl` wires, so the two agree.
	d := doctor.New(doctor.Deps{DeployShaper: deployShapeOf})

	if !jsonOutput {
		fmt.Printf("\n  Checking the %s project...\n\n", store.Meta().Name)
	}

	report, err := d.RunFiltered(ctx, store.Meta().Name, projectDir, signal)
	if err != nil {
		return err
	}

	// Ingress checks live alongside the signal checks but are wired
	// at the cli layer because they need RenderKCL + ListEnvs +
	// kubectl, none of which the internal/doctor package has access
	// to. Skips when features.ingress is off or the signal filter
	// excludes them.
	appendIngressChecksToReport(&report, runIngressDoctorChecks(ctx, cfg, projectDir, signal))

	// Tool checks verify every host binary forge shells out to is
	// installed (+ meets a minimum version where pinned). Wired at
	// the cli layer for the same reason as ingress: the predicate
	// for mkcert needs RenderKCL/ListEnvs, and the install hints
	// are user-facing CLI guidance.
	appendIngressChecksToReport(&report, runToolDoctorChecks(ctx, cfg, projectDir, signal))

	// The tool checks can all pass on a machine that cannot render: KCL's
	// native library is extracted and loaded at runtime, independently of
	// anything on PATH. See doctor_kclplugin.go.
	appendIngressChecksToReport(&report, runKCLPluginDoctorCheck(ctx, cfg, projectDir, signal))

	// A go.work bridge to a local forge checkout compiles generated code
	// against that checkout; this binary must have been built from it. See
	// forge_bridge.go.
	appendIngressChecksToReport(&report, runBridgeDoctorCheck(projectDir, signal))
	appendIngressChecksToReport(&report, runSourceDoctorChecks(ctx, cfg, projectDir, signal))

	// Docker daemon proxy check: warns when the Docker daemon is
	// configured with an HTTP(S) proxy. A TLS-intercepting proxy
	// (Proxyman, Charles, corporate MITM) silently breaks image pulls —
	// ImagePullBackOff / "EOF" / "connection reset" with no mention of a
	// proxy. WARN, not fail: a well-behaved proxy is legitimate. See
	// doctor_dockerproxy.go.
	appendIngressChecksToReport(&report, runDockerProxyDoctorChecks(ctx, cfg, projectDir, signal))

	// External-build checks surface per-service warnings for KCL
	// services that declare build_cmd — missing build_cwd, first
	// token not on PATH, plus the resolved (substituted) command
	// preview so the user sees what `forge build` will actually exec.
	appendExternalBuildChecksToReport(&report, runExternalBuildDoctorChecks(ctx, cfg, projectDir, signal))

	if jsonOutput {
		return d.PrintJSON(os.Stdout, report)
	}

	d.PrintReport(os.Stdout, report, verbose)

	if report.Overall == doctor.StatusFail {
		// Return a sentinel so cobra exits non-zero. The report has
		// already been printed; main.go's "Error: ..." line prints the
		// short message below so the user sees a clear failure reason.
		return errDoctorFailed
	}
	return nil
}

var errDoctorFailed = fmt.Errorf("doctor reported failing checks; see report above")
