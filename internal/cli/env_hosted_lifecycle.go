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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	neturl "net/url"
	"os"
	"path/filepath"
	"slices"
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
	// Name and Project are the control plane's record of the env, shown
	// before a delete. Name is "" when the resolver did not read it.
	Name, Project string
}

// resolveLifecycleTarget is a seam so tests drive the verbs against a fake
// control plane without rendering KCL.
var resolveLifecycleTarget = resolveDeclaredLifecycleTarget

// resolveLifecycleTargetByID is resolveLifecycleTarget for `env delete --id`.
var resolveLifecycleTargetByID = resolveLifecycleTargetFromID

// lifecycleInteractive reports whether forge may prompt. A seam for tests.
var lifecycleInteractive = func() bool { return cliutil.StdinIsTTY() && !buildinfo.IsCI() }

// lifecycleStdin is where the typed confirmation is read from.
var lifecycleStdin io.Reader = os.Stdin

// requireHostedEnv refuses an env the control plane does not run. Pure, so the
// refusal is testable without a render.
//
// PER PART, NOT PER ENV KIND. The verbs act on the env's HOSTED parts — the
// ones the control plane places — and an env may hold cluster parts beside
// them (docs/design/per-workload-placement.md). stop/start act on the hosted
// parts and leave the cluster parts alone; the caller prints which. delete is
// refused for a mixed env: deleting its control-plane environment would drop
// the ledger of the cluster parts it does not tear down.
func requireHostedEnv(env, verb string, parts placementSummary) error {
	if !parts.Hosted {
		return fmt.Errorf("env %q is not a hosted environment (it runs nothing on a control plane), so `forge env %s` does not apply\n"+
			"fix: `forge env down %s` stops a local `forge env up` stack; a hosted env is one with at least one forge.OnHosted workload, database or frontend",
			env, verb, env)
	}
	if verb == "delete" && parts.Cluster {
		return fmt.Errorf("env %q also runs parts on clusters you operate, which `forge env delete` would not tear down, "+
			"and deleting its control-plane environment drops their release history.\n"+
			"fix: move or remove the cluster parts from deploy/kcl/%s/ and deploy that first; `forge env stop %s` suspends only the hosted parts",
			env, env, env)
	}
	return nil
}

// noteUnownedParts prints the parts a lifecycle verb leaves alone: everything
// the control plane does not place.
func noteUnownedParts(w io.Writer, verb string, e *KCLEntities) {
	for _, p := range placementsOf(e) {
		if p.Placement == placementHosted || p.Actuator == actuatorNone || p.Actuator == actuatorMachine {
			continue
		}
		where := p.Placement
		if p.Cluster != "" {
			where += " " + p.Cluster
		}
		fmt.Fprintf(w, "  note: `forge env %s` leaves %s %s alone: it runs on %s (applied by %s)\n", verb, p.Kind, p.Name, where, p.Actuator)
	}
}

// resolveDeclaredLifecycleTarget resolves env from its deploy/kcl/<env>/
// declaration: the control plane it names, and that control plane's id for it.
//
// delete alone also reaches an env that is NO LONGER declared. A hosted env
// outlives its KCL (a release cut can recreate it after the directory was
// removed), and an orphan nobody can delete with forge is a bill nobody can
// stop: forge reports b9f2c42c, 8bc7f067. With no declaration there is no
// render to say which control plane holds it, so it is looked up by name on
// the one named by endpoint (--endpoint) or, without it, on the one control
// plane this project is bound to (resolveUndeclaredLifecycleTarget).
func resolveDeclaredLifecycleTarget(ctx context.Context, env, verb, flagToken, endpoint string) (lifecycleTarget, error) {
	if err := checkLifecycleEnvName(env); err != nil {
		return lifecycleTarget{}, err
	}
	projectDir := projectDirForKCL()
	declared := envDeclared(projectDir, env)
	if verb == "delete" && !declared {
		return resolveUndeclaredLifecycleTarget(ctx, projectDir, env, flagToken, endpoint)
	}
	if strings.TrimSpace(endpoint) != "" {
		return lifecycleTarget{}, fmt.Errorf("--endpoint names the control plane of an env with no deploy/kcl/%s/ declaration; "+
			"env %q is declared, so its KCL names its control plane\nfix: drop --endpoint", env, env)
	}
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return lifecycleTarget{}, fmt.Errorf("render KCL for env %q: %w", env, err)
	}
	if err := requireHostedEnv(env, verb, summarizePlacements(entities)); err != nil {
		return lifecycleTarget{}, err
	}
	noteUnownedParts(os.Stderr, verb, entities)
	ep, err := projectEndpoint(env, declarationFromEntities(entities))
	if err != nil {
		return lifecycleTarget{}, err
	}
	return lifecycleTargetAt(ctx, env, ep, flagToken)
}

// checkLifecycleEnvName refuses an env that is not a single directory name
// under deploy/kcl/. A name such as `../other` would otherwise make
// envDeclared and RenderKCL resolve a directory outside deploy/kcl/, whose KCL
// could name another control plane and receive the user's credential. It runs
// before anything is read, rendered or sent.
func checkLifecycleEnvName(env string) error {
	if env == "" || env == "." || env == ".." || strings.ContainsAny(env, `/\`) || filepath.Base(env) != env {
		return fmt.Errorf("env %q is not an environment name: it must be one directory name under deploy/kcl/ (no path separators, `.` or `..`)\n"+
			"fix: name the env as it appears in deploy/kcl/<env>/, e.g. prod", env)
	}
	return nil
}

// envDeclared reports whether projectDir declares env: deploy/kcl/<env>/.
func envDeclared(projectDir, env string) bool {
	st, err := os.Stat(filepath.Join(projectDir, "deploy", "kcl", env))
	return err == nil && st.IsDir()
}

// resolveUndeclaredLifecycleTarget finds env by name on a control plane when
// the project no longer declares it, on a control plane this project has used:
// one it is bound to in .forge/hosted-projects.json (the record of where its
// hosted envs were created), or Reliant cloud, forge's default control plane.
// endpoint picks among those; without it the project must be bound to exactly
// one. Nothing — not the credential, not the lookup — is sent to any other host.
func resolveUndeclaredLifecycleTarget(ctx context.Context, projectDir, env, flagToken, endpoint string) (lifecycleTarget, error) {
	url, err := undeclaredControlPlane(projectDir, endpoint,
		fmt.Sprintf("env %q is not declared (no deploy/kcl/%s/)", env, env), "forge env delete "+env,
		fmt.Sprintf("; if %q is a typo, the declared envs are the directories under deploy/kcl/", env))
	if err != nil {
		return lifecycleTarget{}, err
	}
	fmt.Fprintf(os.Stderr, "  note: env %q is not declared (no deploy/kcl/%s/); looking it up by name on %s\n", env, env, url)
	ep, err := projectEndpoint(env, &cloud.Declaration{Endpoint: url})
	if err != nil {
		return lifecycleTarget{}, err
	}
	t, err := lifecycleTargetAt(ctx, env, ep, flagToken)
	if errors.Is(err, deploytarget.ErrHostedEnvironmentNotFound) {
		return lifecycleTarget{}, fmt.Errorf("env %q is not declared (no deploy/kcl/%s/) and %s has no hosted env of that name in project %q: nothing to delete",
			env, env, ep.URL, ep.ProjectName)
	}
	return t, err
}

// undeclaredControlPlane picks the control plane to look an env up on when no
// KCL names it: endpoint (--endpoint), which must be a control plane this
// project has used, or else the one control plane the project is bound to.
// lead says why there is no declaration, retry is the command a fix re-runs
// with --endpoint, and hint ends the no-binding fix. Nothing is contacted.
func undeclaredControlPlane(projectDir, endpoint, lead, retry, hint string) (string, error) {
	url := strings.TrimSpace(endpoint)
	if url != "" {
		if err := checkLifecycleEndpoint(url); err != nil {
			return "", err
		}
	}
	bound, err := boundHostedEndpoints(projectDir)
	if err != nil {
		return "", err
	}
	if url != "" {
		if err := checkEndpointUsedByProject(url, bound); err != nil {
			return "", err
		}
		return url, nil
	}
	switch len(bound) {
	case 1:
		return bound[0], nil
	case 0:
		return "", fmt.Errorf("%s, and this project records no hosted control plane (%s) to look it up on\n"+
			"fix: name the control plane that holds it: `%s --endpoint %s` (Reliant cloud)%s",
			lead, hostedProjectBindingFile, retry, cloud.DefaultEndpoint, hint)
	default:
		return "", fmt.Errorf("%s, and this project is bound to %d control planes (%s): %s\n"+
			"fix: name the one that holds it: `%s --endpoint <url>`",
			lead, len(bound), hostedProjectBindingFile, strings.Join(bound, ", "), retry)
	}
}

// resolveLifecycleTargetFromID resolves `forge env delete --id <id>`: the
// environment the control plane knows by that id, read with GetEnvironment on
// the control plane undeclaredControlPlane picks. An id names the env exactly
// where a name may not (a typo, a recreated env), and it is what the Reliant
// UI shows.
//
// It is refused, before anything is deleted, when the env belongs to another
// project, when env (the optional name argument) is not its name, and when
// this project still declares it: a declared env is deleted by name, through
// its KCL, which is what refuses an env with cluster parts.
func resolveLifecycleTargetFromID(ctx context.Context, id, env, flagToken, endpoint string) (lifecycleTarget, error) {
	id = strings.TrimSpace(id)
	if env != "" {
		if err := checkLifecycleEnvName(env); err != nil {
			return lifecycleTarget{}, err
		}
	}
	projectDir := projectDirForKCL()
	url, err := undeclaredControlPlane(projectDir, endpoint,
		fmt.Sprintf("environment %q is looked up by id", id), "forge env delete --id "+id, "")
	if err != nil {
		return lifecycleTarget{}, err
	}
	fmt.Fprintf(os.Stderr, "  note: looking up environment id %q on %s\n", id, url)
	ep, err := projectEndpoint(env, &cloud.Declaration{Endpoint: url})
	if err != nil {
		return lifecycleTarget{}, err
	}
	cred, err := cloud.ResolveCredential(flagToken, ep)
	if err != nil {
		return lifecycleTarget{}, err
	}
	client := cloud.NewClient(ep, cred)
	he, err := deploytarget.GetHostedEnvironment(ctx, client, id)
	if errors.Is(err, deploytarget.ErrHostedEnvironmentNotFound) {
		return lifecycleTarget{}, fmt.Errorf("%s has no hosted env with id %q in your organization: nothing to delete", ep.URL, id)
	}
	if err != nil {
		return lifecycleTarget{}, err
	}
	// The declaration check below probes deploy/kcl/<name>: a reply with no
	// usable name would probe deploy/kcl/ itself and pass every env, so the
	// lookup fails closed on one.
	if err := checkLifecycleEnvName(he.Name); err != nil {
		return lifecycleTarget{}, fmt.Errorf("%s answered environment %q with no usable name (%q), so this project cannot check it does not still declare it; nothing was deleted", ep.URL, id, he.Name)
	}
	// Ownership must be positive: a reply that names no project (an older or
	// malformed control plane) cannot show the env is this project's, so it
	// is refused like another project's, as the list API's filter does.
	project := hostedProjectName()
	if strings.TrimSpace(he.Project) == "" {
		return lifecycleTarget{}, fmt.Errorf("%s answered environment %q (%s) with no project, so this checkout cannot check it belongs to project %q; nothing was deleted", ep.URL, id, he.Name, project)
	}
	if he.Project != project {
		return lifecycleTarget{}, fmt.Errorf("environment %q (%s) belongs to project %q, not this project (%q); nothing was deleted\n"+
			"fix: run `forge env delete --id %s` from that project's checkout", id, he.Name, he.Project, project, id)
	}
	if env != "" && he.Name != env {
		return lifecycleTarget{}, fmt.Errorf("environment %q is named %q on %s, not %q; nothing was deleted\n"+
			"fix: drop the name, or name the env the id belongs to", id, he.Name, ep.URL, env)
	}
	if envDeclared(projectDir, he.Name) {
		return lifecycleTarget{}, fmt.Errorf("environment %q is env %q, which this project still declares (deploy/kcl/%s/)\n"+
			"fix: delete a declared env by name: `forge env delete %s`", id, he.Name, he.Name, he.Name)
	}
	return lifecycleTarget{Client: client, EnvironmentID: he.ID, Endpoint: ep.URL, Name: he.Name, Project: he.Project}, nil
}

// runEnvListOrphaned is `forge env list --orphaned`: the hosted envs of this
// project, on the control planes it has used, that deploy/kcl/ does not
// declare. Those are what `forge env delete <env>` reaches by name alone.
func runEnvListOrphaned(ctx context.Context, projectDir, flagToken, endpoint string, out io.Writer) error {
	var urls []string
	if url := strings.TrimSpace(endpoint); url != "" {
		if err := checkLifecycleEndpoint(url); err != nil {
			return err
		}
		bound, err := boundHostedEndpoints(projectDir)
		if err != nil {
			return err
		}
		if err := checkEndpointUsedByProject(url, bound); err != nil {
			return err
		}
		urls = []string{url}
	} else {
		bound, err := boundHostedEndpoints(projectDir)
		if err != nil {
			return err
		}
		if len(bound) == 0 {
			return fmt.Errorf("this project records no hosted control plane (%s) to ask for its envs\n"+
				"fix: name one: `forge env list --orphaned --endpoint %s` (Reliant cloud)", hostedProjectBindingFile, cloud.DefaultEndpoint)
		}
		urls = bound
	}
	project := hostedProjectName()
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	found := 0
	for _, url := range urls {
		ep, err := projectEndpoint("", &cloud.Declaration{Endpoint: url})
		if err != nil {
			return err
		}
		cred, err := cloud.ResolveCredential(flagToken, ep)
		if err != nil {
			return err
		}
		envs, err := deploytarget.ListHostedEnvironments(ctx, cloud.NewClient(ep, cred), project)
		if err != nil {
			return fmt.Errorf("%s: %w", url, err)
		}
		for _, e := range envs {
			if envDeclared(projectDir, e.Name) {
				continue
			}
			if found == 0 {
				fmt.Fprintln(tw, "ENV\tID\tCONTROL PLANE")
			}
			found++
			fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Name, e.ID, url)
		}
	}
	if found == 0 {
		fmt.Fprintf(out, "no orphaned hosted envs: every env of project %q on %s is declared under deploy/kcl/\n", project, strings.Join(urls, ", "))
		return nil
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	retry := "forge env delete <env>"
	if len(urls) > 1 {
		retry += " --endpoint <control plane>"
	}
	fmt.Fprintf(out, "\n%d hosted env(s) of project %q are not declared under deploy/kcl/. Delete one: %s\n", found, project, retry)
	return nil
}

// checkLifecycleEndpoint refuses an --endpoint that the user's credential must
// not be sent to: anything but https, except plain http to a loopback host (a
// control plane running on this machine, as in dev and tests). It runs before
// any credential is resolved, so a typo'd or cleartext URL never receives the
// bearer token.
func checkLifecycleEndpoint(raw string) error {
	u, err := neturl.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("--endpoint %q is not an absolute URL\nfix: name the control plane as https://<host>, e.g. %s", raw, cloud.DefaultEndpoint)
	}
	if u.User != nil {
		return fmt.Errorf("--endpoint %q carries credentials in the URL; refusing to use it\nfix: drop the user:password@ part and pass the credential with --token", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackEndpointHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("--endpoint %q is plain http to a non-loopback host: your credential would be sent in cleartext\n"+
			"fix: use https://%s (plain http is accepted only for localhost, 127.0.0.0/8 and ::1)", raw, u.Host)
	default:
		return fmt.Errorf("--endpoint %q has scheme %q; a control plane is reached over https\nfix: name it as https://<host>, e.g. %s", raw, u.Scheme, cloud.DefaultEndpoint)
	}
}

// checkEndpointUsedByProject refuses an --endpoint that is neither one of the
// control planes this project is bound to nor Reliant cloud. A typo'd or
// planted URL would otherwise receive the bearer token and could delete an
// unrelated project's env of the same name. It runs before any credential is
// resolved.
func checkEndpointUsedByProject(url string, bound []string) error {
	norm := func(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }
	want := norm(url)
	if want == norm(cloud.DefaultEndpoint) {
		return nil
	}
	for _, b := range bound {
		if norm(b) == want {
			return nil
		}
	}
	known := "none"
	if len(bound) > 0 {
		known = strings.Join(bound, ", ")
	}
	return fmt.Errorf("--endpoint %q is not a control plane this project has used (bound in %s: %s; or Reliant cloud, %s); refusing to send your credential to it\n"+
		"fix: name one of those control planes; a control plane the project was never bound to cannot hold its envs",
		url, hostedProjectBindingFile, known, cloud.DefaultEndpoint)
}

// isLoopbackEndpointHost reports whether host names this machine's loopback
// interface: localhost or a loopback IP literal.
func isLoopbackEndpointHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// boundHostedEndpoints lists the control planes this project is bound to: the
// keys of .forge/hosted-projects.json, sorted. None when the file is absent.
func boundHostedEndpoints(projectDir string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(projectDir, hostedProjectBindingFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids map[string]string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, fmt.Errorf("read hosted project binding %s: %w", hostedProjectBindingFile, err)
	}
	return slices.Sorted(maps.Keys(ids)), nil
}

// lifecycleTargetAt resolves env's id on the control plane ep names.
func lifecycleTargetAt(ctx context.Context, env string, ep cloud.Endpoint, flagToken string) (lifecycleTarget, error) {
	cred, err := cloud.ResolveCredential(flagToken, ep)
	if err != nil {
		return lifecycleTarget{}, err
	}
	client := cloud.NewClient(ep, cred)
	he, err := deploytarget.FindHostedEnvironment(ctx, client, hostedProjectName(), env)
	if err != nil {
		return lifecycleTarget{}, err
	}
	return lifecycleTarget{Client: client, EnvironmentID: he.ID, Endpoint: ep.URL, Name: he.Name, Project: he.Project}, nil
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
			t, err := resolveLifecycleTarget(cmd.Context(), opts.Env, verb, token, "")
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
	var yes, keepData bool
	var token, endpoint, id string
	cmd := &cobra.Command{
		Use:   "delete <environment> | --id <environment-id>",
		Short: "Tear down a HOSTED environment on the control plane (destructive)",
		Long: `Delete a hosted environment and everything it runs on the control plane: its
deployments, workloads, URLs, Flux objects, custom resources, namespaces AND
its managed databases' data (Postgres clusters and volumes). IRREVERSIBLE.

  forge env delete preview             prompts: type the env name to confirm
  forge env delete preview --yes       no prompt (required with no TTY or CI=1)
  forge env delete preview --keep-data keep the databases (still billed as storage)

--keep-data RETAINS each managed database: it keeps running and billing as
storage, is listed by the control plane as a retained database, and stays
until purged (DeleteRetainedDatabase). A later deploy of the same name creates
a NEW environment and does NOT reattach it.

KEPT EITHER WAY: the project's release images in the registry. They belong to
the project, not the environment, and other environments promote them.

An env no longer declared under deploy/kcl/ (its directory was removed but the
hosted env lives on) is looked up by name on the control plane this project is
bound to (.forge/hosted-projects.json), or on the one --endpoint names, which
must be one of those bindings or Reliant cloud:

  forge env delete prod-web --endpoint https://admin.reliantapi.com

--id names the environment by the control plane's id instead (the Reliant UI
shows it), for an undeclared env whose name is ambiguous or unknown. It is
looked up the same way, on a control plane this project has used; a name given
beside it must match. ` + "`forge env list --orphaned`" + ` lists the hosted envs this
project no longer declares.

Before asking for confirmation it shows what will be deleted: the env's id,
project, control plane, deployments and bound custom domains. The control
plane refuses to delete an env while a custom domain is bound to it.

Only hosted environments: for a local stack use ` + "`forge env down`" + `, which never
deletes anything on a control plane and which this command never uses.`,
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var env string
			if len(args) == 1 {
				env = args[0]
			}
			id = strings.TrimSpace(id)
			if env == "" && id == "" {
				return fmt.Errorf("name the environment to delete\n" +
					"fix: `forge env delete <environment>`, or `forge env delete --id <environment-id>`")
			}
			subject := env
			if subject == "" {
				subject = id
			}
			// Fail fast, before any network: a non-interactive run without
			// --yes must never hang on a prompt.
			if !yes && !lifecycleInteractive() {
				return fmt.Errorf("refusing to delete env %q without confirmation: no interactive terminal (or CI is set)\n"+
					"fix: re-run with `--yes` to confirm non-interactively", subject)
			}
			var t lifecycleTarget
			var err error
			if id != "" {
				t, err = resolveLifecycleTargetByID(cmd.Context(), id, env, token, endpoint)
			} else {
				t, err = resolveLifecycleTarget(cmd.Context(), env, "delete", token, endpoint)
			}
			if err != nil {
				return err
			}
			if t.Name != "" {
				env = t.Name
			}
			return runEnvDelete(cmd.Context(), t.Client, t.EnvironmentID, env,
				envDeleteOptions{Yes: yes, KeepData: keepData, Project: t.Project, Endpoint: t.Endpoint}, lifecycleStdin, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the deletion without the typed-name prompt (required when non-interactive)")
	cmd.Flags().BoolVar(&keepData, "keep-data", false, "Keep the environment's managed databases (retained, still billed as storage) instead of destroying them")
	cmd.Flags().StringVar(&token, "token", "", "Credential to use, ahead of the env var and the credentials file")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Control plane holding an env that is no longer declared under deploy/kcl/: one this project is bound to (.forge/hosted-projects.json) or Reliant cloud; https, or http to localhost only (default: the one this project is bound to)")
	cmd.Flags().StringVar(&id, "id", "", "Delete the hosted environment with this control-plane id (an env no longer declared under deploy/kcl/); a name given beside it must match")
	return cmd
}

// writeEnvDeleteSummary is the head of the delete preview: which env, exactly,
// and what is bound to it. A bound custom domain is listed with the fix,
// because the control plane refuses the delete while it stays bound (control
// plane #1547). A domain list that cannot be read is an error: the preview
// would be incomplete right before an irreversible act, so the delete stops
// before confirming. A control plane with no domain service (Unimplemented)
// has no domain to bind, so its preview is complete.
func writeEnvDeleteSummary(ctx context.Context, c cloudCaller, out io.Writer, envID string, opts envDeleteOptions, deployments int) error {
	fmt.Fprintf(out, "  id:             %s\n", envID)
	if opts.Project != "" {
		fmt.Fprintf(out, "  project:        %s\n", opts.Project)
	}
	if opts.Endpoint != "" {
		fmt.Fprintf(out, "  control plane:  %s\n", opts.Endpoint)
	}
	fmt.Fprintf(out, "  deployments:    %d\n", deployments)
	hosts, err := boundDomainHostnames(ctx, c, envID)
	switch {
	case err != nil && hostedErrorHasCode(err, cloud.CodeUnimplemented):
		fmt.Fprintln(out, "  custom domains: none (this control plane has no domain service)")
	case err != nil:
		fmt.Fprintf(out, "  custom domains: could not be read (%v)\n", err)
		return fmt.Errorf("could not list the custom domains bound to env %s, so the delete preview is incomplete; nothing was deleted: %w\n"+
			"fix: re-run once the control plane's domain service answers (`forge domain list` checks it)", envID, err)
	case len(hosts) == 0:
		fmt.Fprintln(out, "  custom domains: none bound")
	default:
		fmt.Fprintf(out, "  custom domains: %s\n", strings.Join(hosts, ", "))
		fmt.Fprintln(out, "  note: the control plane refuses to delete an env while a custom domain is bound to it; unbind each first:")
		for _, h := range hosts {
			fmt.Fprintf(out, "    forge domain unbind %s --env <env>   (<env>: any env this project declares on this control plane)\n", h)
		}
	}
	return nil
}

// boundDomainHostnames lists, sorted, the custom domains bound to envID.
func boundDomainHostnames(ctx context.Context, c cloudCaller, envID string) ([]string, error) {
	var resp struct {
		Domains []domainWire `json:"domains"`
	}
	if err := c.Call(ctx, procListDomains, map[string]any{}, &resp); err != nil {
		return nil, err
	}
	var hosts []string
	for _, d := range resp.Domains {
		if d.Binding != nil && d.Binding.EnvironmentID == envID && d.Hostname != "" {
			hosts = append(hosts, d.Hostname)
		}
	}
	slices.Sort(hosts)
	return hosts, nil
}

// envDeleteOptions are the caller's decisions about one `forge env delete`.
type envDeleteOptions struct {
	// Yes skips the typed-name confirmation.
	Yes bool
	// KeepData retains the environment's managed databases. Off, the control
	// plane destroys them with everything else.
	KeepData bool
	// Project and Endpoint describe the env in the preview; "" omits them.
	Project, Endpoint string
}

func runEnvDelete(ctx context.Context, c cloudCaller, envID, env string, opts envDeleteOptions, in io.Reader, out io.Writer) error {
	ds, err := listLifecycleDeployments(ctx, c, envID)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "This will delete hosted env %q from the control plane:\n", env)
	if err := writeEnvDeleteSummary(ctx, c, out, envID, opts, len(ds)); err != nil {
		return err
	}
	writeDeploymentStates(out, ds)
	if opts.KeepData {
		fmt.Fprintln(out, "\nRemoved: the environment, its deployments, workloads, URLs, Flux objects, custom resources and namespaces.")
		fmt.Fprintln(out, "Kept (--keep-data): its managed databases — still running and billed as storage, listed as retained")
		fmt.Fprintln(out, "  databases until purged, and NOT reattached by a later deploy (a re-deploy creates a new environment).")
	} else {
		fmt.Fprintln(out, "\nRemoved: the environment, its deployments, workloads, URLs, Flux objects, custom resources, namespaces")
		fmt.Fprintln(out, "  AND its managed databases' data. IRREVERSIBLE (pass --keep-data to retain the databases).")
	}
	fmt.Fprintln(out, "Kept: the project's release images in the registry (they belong to the project, not this environment).")
	if !opts.Yes {
		fmt.Fprintf(out, "\nType the environment name (%s) to confirm: ", env)
		var typed string
		_, _ = fmt.Fscanln(in, &typed)
		if strings.TrimSpace(typed) != env {
			return fmt.Errorf("confirmation did not match %q; nothing was deleted", env)
		}
	}
	req := map[string]any{"environmentId": envID, "force": true}
	if opts.KeepData {
		req["keepData"] = true
	}
	if err := c.Call(ctx, procDeleteEnvironment, req, &struct{}{}); err != nil {
		return fmt.Errorf("forge env delete %s: %w", env, err)
	}
	// The control plane records the teardown and its converger performs it,
	// so the delete returns before the clusters have converged.
	if opts.KeepData {
		fmt.Fprintf(out, "\nDeleted env %s; the platform is tearing it down. Its databases were kept and are not reattached by a re-deploy.\n", env)
	} else {
		fmt.Fprintf(out, "\nDeleted env %s; the platform is tearing it down, its databases included.\n", env)
	}
	return nil
}
