package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/hostlaunch"
)

func newEnvExecCmd() *cobra.Command {
	var workload string
	cmd := &cobra.Command{
		Use:          "exec <env> [--workload <name>] -- <command> [args...]",
		Short:        "Run a local command with a workload's resolved configuration",
		SilenceUsage: true,
		Long: `Run a command on THIS host, with inherited stdio and working directory.
The default workload is exactly "api" (the scaffold's _api.name), never the
first service. Renamed/multiple APIs require --workload. Flags after -- belong
to the child and are passed literally, without a shell. To use shell syntax,
explicitly request a shell yourself.

Only OnHost and OnCompose workloads are allowed. Hosted, cluster and build-only
workloads are refused before reading secret stores. No deploy or startup runs.

Configuration shares env config's resolver (including launched coordinates),
and env up's local configuration layers. Declared values override the inherited
environment; OnHost honors FORGE_ENV_OVERRIDE except declared bind ports.
OnCompose uses the HOST-side Compose launcher env (secrets < env_file < runtime
env), not a container's environment or docker compose exec. Use host-reachable
published mappings in your local declaration, not container DNS. Forge does not
scrape or rewrite DSNs, inspect containers, or infer arbitrary URL semantics.

Normal child exit codes pass through unchanged; signals are forwarded, and a
signal-terminated child returns 128 + signal number. No resolved values or child
arguments are logged by Forge.

  ` + Name() + ` env exec dev --workload api -- go run ./cmd/myapp apikey create`,
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() != 1 || len(args) < 2 || args[1] == "" {
				return fmt.Errorf("use: %s env exec <env> [--workload <name>] -- <command> [args...]", Name())
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir := projectDirForKCL()
			e, err := readEnvConfigEntities(cmd.Context(), projectDir, args[0])
			if err != nil {
				return err
			}
			w, err := selectExecWorkload(e, workload)
			if err != nil {
				return err
			}
			// Placement is checked BEFORE resolving the report or any secret store.
			if w.Runtime.Type != RuntimeHost && w.Runtime.Type != RuntimeCompose {
				return fmt.Errorf("workload %q uses %s: env exec only supports OnHost/OnCompose; use your hosting provider's exec tool or kubectl exec for remote workloads", w.Name, w.Runtime.Type)
			}
			// Strict: the child would run without a secret it declares.
			report, err := resolvedEnvConfigReport(cmd.Context(), projectDir, args[0], e, w.Name, secretsRequired)
			if err != nil {
				return err
			}
			if len(report.Workloads) != 1 {
				return fmt.Errorf("workload %q is ambiguous", w.Name)
			}
			values := report.Workloads[0].Env
			var childEnv []string
			if w.Runtime.Type == RuntimeHost {
				childEnv = hostlaunch.LayerHostEnv(os.Environ(), nil, nil, values)
				// Bind ports cannot be overridden (same contract as env up), but do
				// not log their old or new values: arbitrary port-like keys can be secrets.
				for key, value := range values {
					if key == "PORT" || strings.HasSuffix(key, "_PORT") {
						childEnv = withForcedEnv(childEnv, key, value)
					}
				}
			} else {
				childEnv = append([]string(nil), os.Environ()...)
				for key, value := range values {
					childEnv = withForcedEnv(childEnv, key, value)
				}
			}
			c := exec.Command(args[1], args[2:]...)
			c.Env = childEnv
			c.Stdin, c.Stdout, c.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
			return runEnvExecChild(cmd.Context(), c)
		},
	}
	cmd.Flags().StringVar(&workload, "workload", "api", "Workload name (default: exactly api, the scaffold's _api.name)")
	return cmd
}

func selectExecWorkload(e *KCLEntities, name string) (WorkloadEntity, error) {
	var matches []WorkloadEntity
	for _, w := range e.Workloads {
		if strings.EqualFold(w.Name, name) {
			matches = append(matches, w)
		}
	}
	if len(matches) != 1 {
		return WorkloadEntity{}, fmt.Errorf("expected exactly one workload named %q, found %d; select its declared name with --workload (the default is api, not a KCL variable name)", name, len(matches))
	}
	return matches[0], nil
}

func runEnvExecChild(ctx context.Context, c *exec.Cmd) error {
	// Register before Start so a signal cannot kill only the parent between
	// starting the child and installing the handler. Terminal signals also
	// reach the child's foreground process group directly.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := c.Start(); err != nil {
		code := 126
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			code = 127
		}
		// exec errors can contain argv[0]; do not echo user-controlled arguments.
		return exitCodeError{code: code, msg: "env exec: could not start child command"}
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	canceled := ctx.Done()
	for {
		select {
		case s := <-sigs:
			_ = c.Process.Signal(s)
		case <-canceled:
			_ = c.Process.Signal(os.Interrupt)
			canceled = nil
		case err := <-done:
			if err == nil {
				return nil
			}
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				return fmt.Errorf("env exec: child wait failed")
			}
			code := ee.ExitCode()
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			}
			if code <= 0 {
				code = 1
			}
			return exitCodeError{code: code, msg: fmt.Sprintf("env exec: child exited with status %d", code)}
		}
	}
}
