package cli

// `forge env stop | start | delete` — the run-state and teardown verbs for a
// HOSTED environment, i.e. what the Reliant UI's Stop / Start / Delete buttons
// do, reachable from a terminal with a plain `forge login`.
//
// THESE ARE NOT `forge env down`. `down` stops host processes forge started on
// THIS machine and never touches a control plane. stop/start/delete act only
// on an environment the control plane runs, and refuse an env that is not
// hosted, so neither family can be mistaken for the other.
//
// All three are DeployService admin RPCs; the server clips authority to the
// caller's role, so CLI parity grants nothing the UI did not already have.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

const (
	procScale                  = "controlplane.v1.DeployService/Scale"
	procSetEnvironmentRunState = "controlplane.v1.DeployService/SetEnvironmentRunState"
	procDeleteEnvironment      = "controlplane.v1.DeployService/DeleteEnvironment"
	procListDeployments        = "controlplane.v1.DeployService/ListDeployments"
	procLifecycleGetStatus     = "controlplane.v1.DeployService/GetStatus"
)

const (
	runStateRunning   = "DEPLOY_RUN_STATE_RUNNING"
	runStateSuspended = "DEPLOY_RUN_STATE_SUSPENDED"

	lifecycleWaitDefaultTimeout  = 5 * time.Minute
	lifecycleWaitDefaultInterval = 3 * time.Second
)

// lifecycleDeployment is the subset of controlplane.v1.Deployment these verbs
// read. Declared locally: forge does not vendor the control plane's protos.
type lifecycleDeployment struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Tier     string `json:"tier,omitempty"`
	RunState string `json:"runState,omitempty"`
	Observed *struct {
		State     string `json:"state,omitempty"`
		LastError string `json:"lastError,omitempty"`
	} `json:"observed,omitempty"`
}

func (d lifecycleDeployment) observedState() string {
	if d.Observed == nil {
		return ""
	}
	return d.Observed.State
}

// lifecycleTarget is a resolved hosted environment: the client that reaches
// its control plane and the control plane's id for it.
type lifecycleTarget struct {
	Client        cloudCaller
	EnvironmentID string
	Endpoint      string
}

// resolveLifecycleTarget is a seam so tests drive the verbs against a fake
// control plane without rendering KCL.
var resolveLifecycleTarget = resolveDeclaredLifecycleTarget

// lifecycleInteractive reports whether forge may prompt. A seam for tests.
var lifecycleInteractive = func() bool { return cliutil.StdinIsTTY() && !buildinfo.IsCI() }

// lifecycleStdin is where the typed confirmation is read from.
var lifecycleStdin io.Reader = os.Stdin

// requireHostedEnv refuses an env the control plane does not run. Pure, so the
// refusal is testable without a render.
func requireHostedEnv(env, verb string, kind deploytarget.HostedEnvKind) error {
	if kind == deploytarget.HostedEnvPersistent {
		return nil
	}
	return fmt.Errorf("env %q is not a hosted environment (it runs nothing on a control plane), so `forge env %s` does not apply\n"+
		"fix: `forge env down %s` stops a local `forge env up` stack; a hosted env is one with at least one forge.OnHosted workload, database or frontend",
		env, verb, env)
}

func resolveDeclaredLifecycleTarget(ctx context.Context, env, verb, flagToken string) (lifecycleTarget, error) {
	entities, err := RenderKCL(ctx, projectDirForKCL(), env)
	if err != nil {
		return lifecycleTarget{}, fmt.Errorf("render KCL for env %q: %w", env, err)
	}
	if err := requireHostedEnv(env, verb, hostedEnvKindOf(entities)); err != nil {
		return lifecycleTarget{}, err
	}
	ep, err := cloud.ResolveEndpoint(env, declarationFromEntities(entities))
	if err != nil {
		return lifecycleTarget{}, err
	}
	cred, err := cloud.ResolveCredential(flagToken, ep)
	if err != nil {
		return lifecycleTarget{}, err
	}
	client := cloud.NewClient(ep, cred)
	id, err := deploytarget.LookupHostedEnvironment(ctx, client, hostedProjectName(), env)
	if err != nil {
		return lifecycleTarget{}, err
	}
	return lifecycleTarget{Client: client, EnvironmentID: id, Endpoint: ep.URL}, nil
}

func listLifecycleDeployments(ctx context.Context, c cloudCaller, envID string) ([]lifecycleDeployment, error) {
	var resp struct {
		Deployments []lifecycleDeployment `json:"deployments"`
	}
	if err := c.Call(ctx, procListDeployments, map[string]any{"environmentId": envID}, &resp); err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	return resp.Deployments, nil
}

func writeDeploymentStates(out io.Writer, ds []lifecycleDeployment) {
	if len(ds) == 0 {
		fmt.Fprintln(out, "  (the environment has no live deployments)")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "  DEPLOYMENT\tTIER\tDECLARED\tOBSERVED")
	for _, d := range ds {
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", d.Name, shortWire(d.Tier, "DEPLOY_TIER_"),
			shortWire(d.RunState, "DEPLOY_RUN_STATE_"), shortWire(d.observedState(), "DEPLOY_OBSERVED_STATE_"))
	}
	_ = tw.Flush()
}

func shortWire(v, prefix string) string {
	v = strings.TrimPrefix(v, prefix)
	if v == "" || v == "UNSPECIFIED" {
		return "-"
	}
	return strings.ToLower(v)
}

type lifecycleOptions struct {
	Env     string
	Target  string
	Wait    bool
	Timeout time.Duration
	// Interval is the --wait poll cadence; zero means the default.
	Interval time.Duration
}

// runLifecycleRunState is stop (SUSPENDED) and start (RUNNING).
func runLifecycleRunState(ctx context.Context, c cloudCaller, envID string, runState string, opts lifecycleOptions, out io.Writer) error {
	verb := map[string]string{runStateSuspended: "stop", runStateRunning: "start"}[runState]
	var after []lifecycleDeployment

	if opts.Target != "" {
		ds, err := listLifecycleDeployments(ctx, c, envID)
		if err != nil {
			return err
		}
		var id string
		var names []string
		for _, d := range ds {
			names = append(names, d.Name)
			if d.Name == opts.Target {
				id = d.ID
			}
		}
		if id == "" {
			return fmt.Errorf("env %q has no deployment named %q (deployments: %s)\nfix: re-run with `--target <name>` from that list", opts.Env, opts.Target, strings.Join(names, ", "))
		}
		var resp struct {
			Deployment lifecycleDeployment `json:"deployment"`
		}
		if err := c.Call(ctx, procScale, map[string]any{"deploymentId": id, "runState": runState}, &resp); err != nil {
			return lifecycleCallError(verb, opts.Env, err)
		}
		after = []lifecycleDeployment{resp.Deployment}
	} else {
		var resp struct {
			Deployments []lifecycleDeployment `json:"deployments"`
		}
		req := map[string]any{"environmentId": envID, "runState": runState}
		if err := c.Call(ctx, procSetEnvironmentRunState, req, &resp); err != nil {
			return lifecycleCallError(verb, opts.Env, err)
		}
		after = resp.Deployments
	}

	fmt.Fprintf(out, "%s requested for env %s%s; declared state is now:\n", verb, opts.Env, parenthesize(opts.Target))
	writeDeploymentStates(out, after)
	if !opts.Wait {
		fmt.Fprintf(out, "\nThe platform converges asynchronously. Watch it with `forge env %s %s --wait`, or `forge env status %s`.\n", verb, opts.Env, opts.Env)
		return nil
	}
	return waitForRunState(ctx, c, envID, runState, opts, out)
}

// lifecycleCallError renders a refused stop/start. A billing refusal on start
// is the expected one: the server's own reason is the answer, so it is printed
// verbatim with the fix rather than wrapped into a generic failure.
func lifecycleCallError(verb, env string, err error) error {
	var cerr *cloud.Error
	if verb == "start" && errors.As(err, &cerr) && cerr.HasCode(cloud.CodeFailedPrecondition) {
		return fmt.Errorf("the control plane refused to start env %q: %s%s\n"+
			"fix: resolve the condition above (billing is managed in Reliant → Settings → Billing, or ask an org admin), then re-run `forge env start %s`.\n"+
			"Nothing was started", env, cerr.Message, reasonSuffix(cerr.Reason), env)
	}
	return fmt.Errorf("forge env %s %s: %w", verb, env, err)
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " (reason: " + reason + ")"
}

// observedSettled reports whether one deployment has reached the state a
// run-state change asked for.
func observedSettled(d lifecycleDeployment, runState string) bool {
	switch runState {
	case runStateSuspended:
		return d.observedState() == "DEPLOY_OBSERVED_STATE_SUSPENDED"
	default:
		return d.observedState() == "DEPLOY_OBSERVED_STATE_READY"
	}
}

func waitForRunState(ctx context.Context, c cloudCaller, envID, runState string, opts lifecycleOptions, out io.Writer) error {
	timeout, interval := opts.Timeout, opts.Interval
	if timeout <= 0 {
		timeout = lifecycleWaitDefaultTimeout
	}
	if interval <= 0 {
		interval = lifecycleWaitDefaultInterval
	}
	want := "ready"
	if runState == runStateSuspended {
		want = "suspended"
	}
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		var resp struct {
			Deployments []struct {
				Deployment lifecycleDeployment `json:"deployment"`
			} `json:"deployments"`
		}
		if err := c.Call(ctx, procLifecycleGetStatus, map[string]any{"environmentId": envID}, &resp); err != nil {
			return fmt.Errorf("read status while waiting: %w", err)
		}
		var pending []string
		var progress []string
		for _, s := range resp.Deployments {
			d := s.Deployment
			if opts.Target != "" && d.Name != opts.Target {
				continue
			}
			progress = append(progress, d.Name+"="+shortWire(d.observedState(), "DEPLOY_OBSERVED_STATE_"))
			if !observedSettled(d, runState) {
				pending = append(pending, d.Name)
			}
		}
		if line := strings.Join(progress, " "); line != last {
			fmt.Fprintf(out, "waiting for %s: %s\n", want, line)
			last = line
		}
		if len(pending) == 0 {
			fmt.Fprintf(out, "env %s: every deployment is %s\n", opts.Env, want)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for env %q to be %s; still waiting on: %s\n"+
				"fix: the request was accepted and the platform keeps converging — re-check with `forge env status %s`",
				timeout, opts.Env, want, strings.Join(pending, ", "), opts.Env)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func newEnvRunStateCmd(verb, runState, short, long string) *cobra.Command {
	var opts lifecycleOptions
	var token string
	cmd := &cobra.Command{
		Use:   verb + " <environment>",
		Short: short,
		Long:  long,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Env = args[0]
			t, err := resolveLifecycleTarget(cmd.Context(), opts.Env, verb, token)
			if err != nil {
				return err
			}
			return runLifecycleRunState(cmd.Context(), t.Client, t.EnvironmentID, runState, opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&opts.Target, "target", "", "Act on ONE workload (by name) instead of the whole environment")
	cmd.Flags().BoolVar(&opts.Wait, "wait", false, "Poll until every affected deployment is observed in the requested state")
	cmd.Flags().DurationVar(&opts.Timeout, "timeout", lifecycleWaitDefaultTimeout, "How long --wait polls before giving up")
	cmd.Flags().DurationVar(&opts.Interval, "poll-interval", lifecycleWaitDefaultInterval, "How often --wait re-reads status")
	cmd.Flags().StringVar(&token, "token", "", "Credential to use, ahead of the env var and the credentials file")
	return cmd
}

func newEnvStopCmd() *cobra.Command {
	return newEnvRunStateCmd("stop", runStateSuspended,
		"Suspend a HOSTED environment's workloads on the control plane (not local processes)",
		`Suspend every deployment of a hosted environment (or one with --target).

  forge env stop prod                  suspend the whole environment
  forge env stop prod --target api     suspend one workload
  forge env stop prod --wait           poll until the platform reports it suspended

Suspending is never refused, keeps the environment and its data, and stops
compute billing for what it suspends. Bring it back with `+"`forge env start`"+`.

This acts on the CONTROL PLANE. It does not touch processes on this machine:
that is `+"`forge env down`"+`. An env that is not hosted is refused.`)
}

func newEnvStartCmd() *cobra.Command {
	return newEnvRunStateCmd("start", runStateRunning,
		"Resume a HOSTED environment's workloads on the control plane (not local processes)",
		`Resume every deployment of a hosted environment (or one with --target).

  forge env start prod                 resume the whole environment
  forge env start prod --target api    resume one workload
  forge env start prod --wait          poll until the platform reports it ready

The control plane refuses a resume the organization is not entitled to (for
example a billing condition); nothing is started then, and the server's reason
is printed with the fix.

This acts on the CONTROL PLANE. Locally running a stack is `+"`forge env up`"+`.
An env that is not hosted is refused.`)
}

func newEnvDeleteCmd() *cobra.Command {
	var yes bool
	var token string
	cmd := &cobra.Command{
		Use:   "delete <environment>",
		Short: "Tear down a HOSTED environment on the control plane (destructive)",
		Long: `Delete a hosted environment: its deployments, Flux objects, custom resources
and namespaces on the control plane.

  forge env delete preview             prompts: type the env name to confirm
  forge env delete preview --yes       no prompt (required with no TTY or CI=1)

DATA RETENTION: a ManagedDatabase's data is RETAINED but orphaned. A later
deploy of the same name creates a NEW environment and does NOT reattach that
data; recovering it is a support operation.

Only hosted environments: for a local stack use ` + "`forge env down`" + `, which never
deletes anything on a control plane and which this command never uses.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env := args[0]
			// Fail fast, before any network: a non-interactive run without
			// --yes must never hang on a prompt.
			if !yes && !lifecycleInteractive() {
				return fmt.Errorf("refusing to delete env %q without confirmation: no interactive terminal (or CI is set)\n"+
					"fix: re-run with `--yes` to confirm non-interactively", env)
			}
			t, err := resolveLifecycleTarget(cmd.Context(), env, "delete", token)
			if err != nil {
				return err
			}
			return runEnvDelete(cmd.Context(), t.Client, t.EnvironmentID, env, yes, lifecycleStdin, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the deletion without the typed-name prompt (required when non-interactive)")
	cmd.Flags().StringVar(&token, "token", "", "Credential to use, ahead of the env var and the credentials file")
	return cmd
}

func runEnvDelete(ctx context.Context, c cloudCaller, envID, env string, yes bool, in io.Reader, out io.Writer) error {
	ds, err := listLifecycleDeployments(ctx, c, envID)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "This will delete hosted env %q from the control plane:\n", env)
	writeDeploymentStates(out, ds)
	fmt.Fprintln(out, "\nRemoved: the environment, its deployments, their Flux objects, custom resources and namespaces.")
	fmt.Fprintln(out, "Retained: ManagedDatabase data — orphaned, and NOT reattached by a later deploy (a re-deploy creates a new environment).")
	if !yes {
		fmt.Fprintf(out, "\nType the environment name (%s) to confirm: ", env)
		var typed string
		_, _ = fmt.Fscanln(in, &typed)
		if strings.TrimSpace(typed) != env {
			return fmt.Errorf("confirmation did not match %q; nothing was deleted", env)
		}
	}
	if err := c.Call(ctx, procDeleteEnvironment, map[string]any{"environmentId": envID, "force": true}, &struct{}{}); err != nil {
		return fmt.Errorf("forge env delete %s: %w", env, err)
	}
	fmt.Fprintf(out, "\nDeleted env %s. Its databases' data was retained and is not reattached by a re-deploy.\n", env)
	return nil
}
