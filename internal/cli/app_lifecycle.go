package cli

// `forge app stop | teardown | delete` — the verbs that act on a HOSTED app as
// a whole: every environment the control plane runs for this project
// (docs/design/unified-shell-and-projects.md §11, D17–D19 and D21 in
// control-plane). From least to most destructive:
//
//   - stop: every env suspended (SetEnvironmentRunState). Compute stops;
//     storage keeps billing. `forge env start <env>` resumes one.
//   - teardown: every env deleted (DeleteEnvironment), the app itself kept —
//     its name, releases, release images and domains — so a later
//     `forge env deploy` recreates an env. Nothing runs or bills.
//   - delete: the app scheduled for deletion (ScheduleAppDeletion): stopped at
//     once, purged with everything that names it after a grace window,
//     cancelable until then (--cancel), readable with --status.
//
// stop and teardown are loops over the per-env rpcs `forge env stop` and
// `forge env delete` call, so they need nothing a control plane does not
// already serve. delete needs the app-deletion rpcs; a control plane without
// them answers Unimplemented and forge says so.
//
// The server decides authority (D19): stop needs deploy:write per env (an env
// the caller may not write is reported and left running); teardown is an org
// owner's or admin's; delete and its cancel also need a live step-up on every
// protected env, which forge reports as the control plane sends it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
)

const (
	procScheduleAppDeletion = "controlplane.v1.DeployService/ScheduleAppDeletion"
	procCancelAppDeletion   = "controlplane.v1.DeployService/CancelAppDeletion"
	procGetAppDeletion      = "controlplane.v1.DeployService/GetAppDeletion"
)

// reasonStepUpRequired is the control plane's stable reason for a change that
// needs a live step-up on a protected environment.
const reasonStepUpRequired = "step_up_required"

// appTarget is the hosted app these verbs act on: the control plane holding
// it and the app's name there (forge.yaml `name`).
type appTarget struct {
	Client   cloudCaller
	Endpoint string
	Project  string
}

// resolveAppTarget is a seam so tests drive the verbs against a fake control
// plane. retry is the command a fix re-runs with --endpoint.
var resolveAppTarget = resolveBoundAppTarget

// resolveBoundAppTarget picks the control plane the way `forge env delete`
// does for an env with no declaration: the one this project is bound to
// (.forge/hosted-projects.json), or the one --endpoint names, which must be
// one of those bindings or Reliant cloud. An app is every env on one control
// plane, so no single env's KCL names it. Project is "" outside a project.
func resolveBoundAppTarget(retry, flagToken, endpoint string) (appTarget, error) {
	project := hostedProjectName()
	lead := "the app's control plane is the one this project is bound to"
	if project != "" {
		lead = fmt.Sprintf("app %q is looked up on the control plane this project is bound to", project)
	}
	url, err := undeclaredControlPlane(projectDirForKCL(), endpoint, lead, retry)
	if err != nil {
		return appTarget{}, err
	}
	ep, err := projectEndpoint("", &cloud.Declaration{Endpoint: url})
	if err != nil {
		return appTarget{}, err
	}
	cred, err := cloud.ResolveCredential(flagToken, ep)
	if err != nil {
		return appTarget{}, err
	}
	return appTarget{Client: cloud.NewClient(ep, cred), Endpoint: ep.URL, Project: project}, nil
}

// requireAppName refuses to act with no app: a directory with no forge.yaml.
func requireAppName(t appTarget) error {
	if strings.TrimSpace(t.Project) == "" {
		return errors.New("no app to act on: this directory has no forge.yaml naming one\n" +
			"fix: run it from the app's checkout (forge.yaml `name` is the app's name), or pass -C <dir>")
	}
	return nil
}

// listAppEnvs is every hosted env of project on the control plane, by name.
func listAppEnvs(ctx context.Context, c cloudCaller, project string) ([]deploytarget.HostedEnvironment, error) {
	envs, err := deploytarget.ListHostedEnvironments(ctx, c, project)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(envs, func(a, b deploytarget.HostedEnvironment) int { return strings.Compare(a.Name, b.Name) })
	return envs, nil
}

// confirmTypedName is the typed-name confirmation `forge env delete` uses:
// the exact name, or nothing happens.
func confirmTypedName(in io.Reader, out io.Writer, what, name string) bool {
	fmt.Fprintf(out, "\nType the %s name (%s) to confirm: ", what, name)
	var typed string
	_, _ = fmt.Fscanln(in, &typed)
	return strings.TrimSpace(typed) == name
}

// requireConfirmable fails fast, before any network, when the confirmation
// cannot be asked: a non-interactive run without --yes must never hang.
func requireConfirmable(yes bool, verb, app string) error {
	if yes || lifecycleInteractive() {
		return nil
	}
	return fmt.Errorf("refusing to %s app %q without confirmation: no interactive terminal (or CI is set)\n"+
		"fix: re-run with `--yes` to confirm non-interactively", verb, app)
}

// appCallMessage is a refusal as the control plane sent it: its message and
// reason, without forge's transport framing.
func appCallMessage(err error) string {
	var cerr *cloud.Error
	if errors.As(err, &cerr) && cerr.Message != "" {
		return cerr.Message + reasonSuffix(cerr.Reason)
	}
	return err.Error()
}

// writeAppEnvProgress prints one env's outcome in a loop, aligned on width.
func writeAppEnvProgress(out io.Writer, width int, env, outcome string) {
	fmt.Fprintf(out, "  %-*s  %s\n", width, env, outcome)
}

func appEnvNameWidth(envs []deploytarget.HostedEnvironment) int {
	w := 0
	for _, e := range envs {
		w = max(w, len(e.Name))
	}
	return w
}

// ---------------------------------------------------------------------------
// stop

func runAppStop(ctx context.Context, t appTarget, yes bool, in io.Reader, out io.Writer) error {
	envs, err := listAppEnvs(ctx, t.Client, t.Project)
	if err != nil {
		return err
	}
	if len(envs) == 0 {
		fmt.Fprintf(out, "app %q has no hosted environments on %s: nothing to stop\n", t.Project, t.Endpoint)
		return nil
	}
	fmt.Fprintf(out, "This will stop app %q on %s: every deployment of each env is suspended.\n", t.Project, t.Endpoint)
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "  ENV\tID")
	for _, e := range envs {
		fmt.Fprintf(tw, "  %s\t%s\n", e.Name, e.ID)
	}
	_ = tw.Flush()
	fmt.Fprintln(out, "\nKept: URLs, data, secrets and custom domains. `forge env start <env>` resumes an env.")
	fmt.Fprintln(out, "Compute stops billing. Storage keeps billing (database volumes, a static site's files);")
	fmt.Fprintln(out, "  `forge app teardown` is the state that bills nothing.")
	if !yes && !confirmTypedName(in, out, "app", t.Project) {
		return fmt.Errorf("confirmation did not match %q; nothing was stopped", t.Project)
	}
	fmt.Fprintln(out)
	width := appEnvNameWidth(envs)
	var left []string
	for _, e := range envs {
		var resp struct {
			Deployments []lifecycleDeployment `json:"deployments"`
		}
		req := map[string]any{"environmentId": e.ID, "runState": runStateSuspended}
		if err := t.Client.Call(ctx, procSetEnvironmentRunState, req, &resp); err != nil {
			writeAppEnvProgress(out, width, e.Name, "NOT stopped: "+appCallMessage(err))
			left = append(left, e.Name)
			continue
		}
		writeAppEnvProgress(out, width, e.Name, fmt.Sprintf("stopped (%d deployment(s) suspended)", len(resp.Deployments)))
	}
	if len(left) > 0 {
		return fmt.Errorf("stopped %d of %d envs of app %q; left running: %s\n"+
			"fix: each refusal is printed above. An env you may not write (deploy:write) stays running: ask someone who may stop it, "+
			"or re-run `forge app stop` once the refusal is resolved (stopping a stopped env changes nothing)",
			len(envs)-len(left), len(envs), t.Project, strings.Join(left, ", "))
	}
	fmt.Fprintf(out, "\nStopped every env of app %q. The platform converges asynchronously: `forge env status <env>` shows it.\n", t.Project)
	return nil
}

// ---------------------------------------------------------------------------
// teardown

// appTeardownPreflight refuses, before anything is deleted, an env this
// project declares with parts on a cluster the author operates: as for
// `forge env delete`, deleting its control-plane environment would drop the
// release history of parts the control plane does not tear down. A seam so
// tests need no KCL.
var appTeardownPreflight = declaredEnvTeardownCheck

func declaredEnvTeardownCheck(ctx context.Context, env string) error {
	projectDir := projectDirForKCL()
	if checkLifecycleEnvName(env) != nil || !envDeclared(projectDir, env) {
		return nil
	}
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return fmt.Errorf("render KCL for env %q: %w; nothing was deleted", env, err)
	}
	if summarizePlacements(entities).Cluster {
		return fmt.Errorf("env %q also runs parts on clusters you operate, which `forge app teardown` would not tear down, "+
			"and deleting its control-plane environment drops their release history; nothing was deleted\n"+
			"fix: move or remove the cluster parts from deploy/kcl/%s/ and deploy that first, then re-run `forge app teardown`",
			env, env)
	}
	return nil
}

// boundDomainsByEnv maps each env id to the custom domains bound to it,
// sorted. Any failure is returned, Unimplemented included: teardown loops
// over irreversible deletes, so "could not read the domains" must never read
// as "no domain is bound" — the control plane would refuse a bound env's
// delete halfway through the loop.
func boundDomainsByEnv(ctx context.Context, c cloudCaller) (map[string][]string, error) {
	var resp struct {
		Domains []domainWire `json:"domains"`
	}
	if err := c.Call(ctx, procListDomains, map[string]any{}, &resp); err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, d := range resp.Domains {
		if d.Binding != nil && d.Binding.EnvironmentID != "" && d.Hostname != "" {
			out[d.Binding.EnvironmentID] = append(out[d.Binding.EnvironmentID], d.Hostname)
		}
	}
	for _, hosts := range out {
		slices.Sort(hosts)
	}
	return out, nil
}

// writeAppTeardownEffects says what a teardown removes and keeps.
func writeAppTeardownEffects(out io.Writer, keepData bool) {
	if keepData {
		fmt.Fprintln(out, "\nRemoved: each environment, its deployments, workloads, URLs, Flux objects, custom resources and namespaces.")
		fmt.Fprintln(out, "Kept (--keep-data): their managed databases — still running and billed as storage, listed by")
		fmt.Fprintln(out, "  `forge env retained list` until purged, and NOT reattached by a later deploy.")
	} else {
		fmt.Fprintln(out, "\nRemoved: each environment, its deployments, workloads, URLs, Flux objects, custom resources, namespaces")
		fmt.Fprintln(out, "  AND its managed databases' data. IRREVERSIBLE (pass --keep-data to retain the databases).")
	}
	fmt.Fprintln(out, "Kept: the app — its name, release history and release images (not billed) — so")
	fmt.Fprintln(out, "  `forge env deploy <env> <release>` recreates an env; custom domains stay in your organization, unbound.")
	fmt.Fprintln(out, "Each env's secrets stop being delivered and are not given back to a recreated env: set them again.")
	if keepData {
		fmt.Fprintln(out, "Afterwards nothing runs or bills except the kept databases' storage.")
	} else {
		fmt.Fprintln(out, "Afterwards nothing runs or bills.")
	}
}

type appTeardownOptions struct {
	// Yes skips the typed-name confirmation.
	Yes bool
	// KeepData retains every env's managed databases (`forge env delete
	// --keep-data` for each env).
	KeepData bool
}

func runAppTeardown(ctx context.Context, t appTarget, opts appTeardownOptions, in io.Reader, out io.Writer) error {
	envs, err := listAppEnvs(ctx, t.Client, t.Project)
	if err != nil {
		return err
	}
	if len(envs) == 0 {
		fmt.Fprintf(out, "app %q has no hosted environments on %s: nothing to tear down (the app, its name and releases, is kept)\n", t.Project, t.Endpoint)
		return nil
	}
	for _, e := range envs {
		if err := appTeardownPreflight(ctx, e.Name); err != nil {
			return err
		}
	}
	domains, err := boundDomainsByEnv(ctx, t.Client)
	if rpcUnserved(err) {
		return fmt.Errorf("%s does not serve DomainService/ListDomains, so forge cannot check that no custom domain is bound to app %q's envs "+
			"and the teardown preview is incomplete; nothing was deleted\n"+
			"fix: delete each env with `forge env delete <env>`, which shows its own preview, or tear down from the Reliant web app", t.Endpoint, t.Project)
	}
	if err != nil {
		return fmt.Errorf("could not list the custom domains bound to app %q's envs, so the teardown preview is incomplete; nothing was deleted: %w\n"+
			"fix: re-run once the control plane's domain service answers (`forge domain list` checks it)", t.Project, err)
	}
	counts := make([]int, len(envs))
	for i, e := range envs {
		ds, err := listLifecycleDeployments(ctx, t.Client, e.ID)
		if err != nil {
			return fmt.Errorf("env %s: %w; nothing was deleted", e.Name, err)
		}
		counts[i] = len(ds)
	}

	fmt.Fprintf(out, "This will tear down app %q on %s: every hosted environment is deleted.\n", t.Project, t.Endpoint)
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "  ENV\tID\tDEPLOYMENTS\tCUSTOM DOMAINS")
	var bound []string
	for i, e := range envs {
		hosts := "none"
		if hs := domains[e.ID]; len(hs) > 0 {
			hosts = strings.Join(hs, ", ")
			for _, h := range hs {
				bound = append(bound, fmt.Sprintf("    forge domain unbind %s --env %s", h, e.Name))
			}
		}
		fmt.Fprintf(tw, "  %s\t%s\t%d\t%s\n", e.Name, e.ID, counts[i], hosts)
	}
	_ = tw.Flush()
	if len(bound) > 0 {
		return fmt.Errorf("the control plane refuses to delete an env while a custom domain is bound to it, so nothing was deleted\n"+
			"fix: unbind each (the domains stay in your organization), then re-run `forge app teardown`:\n%s", strings.Join(bound, "\n"))
	}

	writeAppTeardownEffects(out, opts.KeepData)
	if !opts.Yes && !confirmTypedName(in, out, "app", t.Project) {
		return fmt.Errorf("confirmation did not match %q; nothing was deleted", t.Project)
	}

	fmt.Fprintln(out)
	width := appEnvNameWidth(envs)
	var left []string
	for _, e := range envs {
		req := map[string]any{"environmentId": e.ID, "force": true}
		if opts.KeepData {
			req["keepData"] = true
		}
		if err := t.Client.Call(ctx, procDeleteEnvironment, req, &struct{}{}); err != nil {
			writeAppEnvProgress(out, width, e.Name, "NOT deleted: "+appCallMessage(err))
			left = append(left, e.Name)
			continue
		}
		writeAppEnvProgress(out, width, e.Name, "deleted; the platform is tearing it down")
	}
	if len(left) > 0 {
		return fmt.Errorf("deleted %d of %d envs of app %q; not deleted: %s\n"+
			"fix: each refusal is printed above (tearing down an app needs an org owner or admin); resolve it and re-run `forge app teardown`, "+
			"which deletes only the envs that remain",
			len(envs)-len(left), len(envs), t.Project, strings.Join(left, ", "))
	}
	fmt.Fprintf(out, "\nTore down app %q: every env is deleted and the platform is tearing them down. Its releases are kept.\n", t.Project)
	if opts.KeepData {
		fmt.Fprintln(out, "Kept databases bill storage until purged: `forge env retained list`, then `forge env retained purge <id>`.")
	}
	return nil
}

// ---------------------------------------------------------------------------
// delete

// appDeletionWire is controlplane.v1.AppDeletion. Declared locally: forge
// does not vendor the control plane's protos.
type appDeletionWire struct {
	Project             string `json:"project"`
	ScheduledAt         string `json:"scheduledAt,omitempty"`
	PurgeAfter          string `json:"purgeAfter,omitempty"`
	ScheduledBy         string `json:"scheduledBy,omitempty"`
	PurgeStartedAt      string `json:"purgeStartedAt,omitempty"`
	SecretDestroyPaused bool   `json:"secretDestroyPaused,omitempty"`
}

// wireTime renders a protojson Timestamp for people, in UTC; anything that
// does not parse is shown as sent.
func wireTime(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return s
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// rpcUnserved reports a control plane that does not serve the rpc at all:
// Unimplemented, or a bare 404 with no Connect envelope (a server with no
// such route). A Connect NotFound is also HTTP 404 but carries its code, and
// means the rpc ran.
func rpcUnserved(err error) bool {
	var cerr *cloud.Error
	if !errors.As(err, &cerr) {
		return false
	}
	return cerr.HasCode(cloud.CodeUnimplemented) || (cerr.Code == "" && cerr.HTTPStatus == 404)
}

// appDeletionError renders a refused app-deletion rpc. verb is what was being
// done ("schedule the deletion of", "cancel the deletion of", "read the
// deletion of").
func appDeletionError(err error, t appTarget, verb, rpc string) error {
	var cerr *cloud.Error
	switch {
	case rpcUnserved(err):
		return fmt.Errorf("%s cannot delete apps: it does not serve DeployService/%s (an older control plane, or one with app deletion not configured); nothing was changed\n"+
			"fix: `forge app teardown` deletes every env and stops all billing while keeping the app's name and releases; "+
			"deleting the app itself needs a control plane with app deletion", t.Endpoint, rpc)
	case hostedErrorHasCode(err, cloud.CodeNotFound):
		return fmt.Errorf("app %q does not exist on %s (your organization has no hosted app of that name); nothing was changed", t.Project, t.Endpoint)
	case errors.As(err, &cerr) && cerr.HasReason(reasonStepUpRequired):
		return fmt.Errorf("the control plane refused to %s app %q: %s\n"+
			"fix: app %q has a protected environment, and deleting it (or cancelling that) needs a live step-up on each protected env. "+
			"Step up in the Reliant web app (a one-time code is emailed to you), then re-run while it is live.\nNothing was changed",
			verb, t.Project, appCallMessage(err), t.Project)
	case hostedErrorHasCode(err, cloud.CodePermissionDenied):
		return fmt.Errorf("the control plane refused to %s app %q: %s\n"+
			"fix: deleting an app, and cancelling that, needs an org owner or admin signed in as a person (a token cannot); ask one.\nNothing was changed",
			verb, t.Project, appCallMessage(err))
	}
	return fmt.Errorf("%s app %q: %w", verb, t.Project, err)
}

func getAppDeletion(ctx context.Context, t appTarget) (*appDeletionWire, error) {
	var resp struct {
		Deletion *appDeletionWire `json:"deletion"`
	}
	if err := t.Client.Call(ctx, procGetAppDeletion, map[string]any{"project": t.Project}, &resp); err != nil {
		return nil, appDeletionError(err, t, "read the deletion of", "GetAppDeletion")
	}
	return resp.Deletion, nil
}

// writeAppDeletion prints a scheduled deletion.
func writeAppDeletion(out io.Writer, d *appDeletionWire) {
	scheduled := wireTime(d.ScheduledAt)
	if d.ScheduledBy != "" {
		scheduled += " by user " + d.ScheduledBy
	}
	fmt.Fprintf(out, "  scheduled:     %s\n", scheduled)
	fmt.Fprintf(out, "  purge after:   %s\n", wireTime(d.PurgeAfter))
	if d.PurgeStartedAt != "" {
		fmt.Fprintf(out, "  purge started: %s (it can no longer be cancelled)\n", wireTime(d.PurgeStartedAt))
	} else {
		fmt.Fprintln(out, "  purge started: not yet (cancel with `forge app delete --cancel`)")
	}
	if d.SecretDestroyPaused {
		fmt.Fprintln(out, "  secret destroy paused: the purge is waiting on a secret destroy that the control plane's")
		fmt.Fprintln(out, "    kill switch holds; it resumes when the platform operator turns destroys back on")
	}
}

func runAppDeletionStatus(ctx context.Context, t appTarget, out io.Writer) error {
	d, err := getAppDeletion(ctx, t)
	if err != nil {
		return err
	}
	if d == nil {
		fmt.Fprintf(out, "app %q on %s has no deletion scheduled\n", t.Project, t.Endpoint)
		return nil
	}
	fmt.Fprintf(out, "app %q on %s is scheduled for deletion:\n", t.Project, t.Endpoint)
	writeAppDeletion(out, d)
	return nil
}

func runAppDelete(ctx context.Context, t appTarget, yes bool, in io.Reader, out io.Writer) error {
	// The read first: it shows an existing schedule instead of asking again,
	// and it finds a control plane with no app deletion before the prompt.
	d, err := getAppDeletion(ctx, t)
	if err != nil {
		return err
	}
	if d != nil {
		fmt.Fprintf(out, "app %q on %s is already scheduled for deletion; nothing to do:\n", t.Project, t.Endpoint)
		writeAppDeletion(out, d)
		return nil
	}
	envs, err := listAppEnvs(ctx, t.Client, t.Project)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "This will schedule app %q on %s for deletion.\n", t.Project, t.Endpoint)
	if len(envs) == 0 {
		fmt.Fprintln(out, "  (the app has no hosted environments)")
	} else {
		tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
		fmt.Fprintln(tw, "  ENV\tID")
		for _, e := range envs {
			fmt.Fprintf(tw, "  %s\t%s\n", e.Name, e.ID)
		}
		_ = tw.Flush()
	}
	fmt.Fprintln(out, "\nNow: every env is stopped (compute stops billing), and deploys, promotions and new envs of the app are refused.")
	fmt.Fprintln(out, "After the grace window (7 days): the control plane purges everything that names the app — every env and")
	fmt.Fprintln(out, "  its data, kept databases, release history and registry images, custom domains and their certificates,")
	fmt.Fprintln(out, "  secrets — and frees the name. Activity, audit and spend history are kept.")
	fmt.Fprintln(out, "Until the purge starts, `forge app delete --cancel` keeps the app and starts again what this stopped.")
	fmt.Fprintln(out, "To keep the app's name and releases but stop every charge, use `forge app teardown` instead.")
	if !yes && !confirmTypedName(in, out, "app", t.Project) {
		return fmt.Errorf("confirmation did not match %q; nothing was scheduled", t.Project)
	}
	var resp struct {
		Deletion             *appDeletionWire `json:"deletion"`
		StoppedDeploymentIDs []string         `json:"stoppedDeploymentIds"`
	}
	if err := t.Client.Call(ctx, procScheduleAppDeletion, map[string]any{"project": t.Project}, &resp); err != nil {
		return appDeletionError(err, t, "schedule the deletion of", "ScheduleAppDeletion")
	}
	fmt.Fprintf(out, "\nScheduled app %q for deletion; stopped %d deployment(s).\n", t.Project, len(resp.StoppedDeploymentIDs))
	if resp.Deletion != nil {
		writeAppDeletion(out, resp.Deletion)
	}
	fmt.Fprintln(out, "`forge app delete --status` shows it; `forge app delete --cancel` cancels it until the purge starts.")
	return nil
}

func runAppDeleteCancel(ctx context.Context, t appTarget, out io.Writer) error {
	d, err := getAppDeletion(ctx, t)
	if err != nil {
		return err
	}
	if d == nil {
		fmt.Fprintf(out, "app %q on %s has no deletion scheduled; nothing to cancel\n", t.Project, t.Endpoint)
		return nil
	}
	var resp struct {
		ResumedDeploymentIDs []string `json:"resumedDeploymentIds"`
		ResumeRefusedReason  string   `json:"resumeRefusedReason"`
	}
	if err := t.Client.Call(ctx, procCancelAppDeletion, map[string]any{"project": t.Project}, &resp); err != nil {
		return appDeletionError(err, t, "cancel the deletion of", "CancelAppDeletion")
	}
	fmt.Fprintf(out, "Cancelled the deletion of app %q; it is kept.\n", t.Project)
	if resp.ResumeRefusedReason != "" {
		fmt.Fprintf(out, "Its stopped deployments were NOT started again: %s\n", resp.ResumeRefusedReason)
		fmt.Fprintln(out, "  Resolve that, then start each env with `forge env start <env>`.")
		return nil
	}
	fmt.Fprintf(out, "Started again the %d deployment(s) the deletion stopped; one stopped before it stays stopped.\n", len(resp.ResumedDeploymentIDs))
	return nil
}

// ---------------------------------------------------------------------------
// commands

func newAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Stop, tear down or delete a HOSTED app: every environment the control plane runs for this project",
		Long: `Act on a hosted app as a whole: every environment the control plane runs for
this project (forge.yaml ` + "`name`" + ` is the app's name). From least to most
destructive:

  forge app stop        suspend every env; storage keeps billing    (forge env start <env> resumes)
  forge app teardown    delete every env, keep the app's name, releases, images and domains; nothing bills
  forge app delete      schedule the app's deletion: stopped now, purged with its name after 7 days
                        (--cancel keeps it, --status shows it)

Each acts on the control plane this project is bound to
(.forge/hosted-projects.json), or on the one --endpoint names. One env alone is
` + "`forge env stop|start|delete <env>`" + `; databases kept by a delete are
` + "`forge env retained list|purge`" + `.`,
	}
	cmd.AddCommand(newAppStopCmd())
	cmd.AddCommand(newAppTeardownCmd())
	cmd.AddCommand(newAppDeleteCmd())
	return cmdutil.StrictGroup(cmd)
}

func addAppTargetFlags(cmd *cobra.Command, token, endpoint *string) {
	cmd.Flags().StringVar(token, "token", "", "Credential to use, ahead of the env var and the credentials file")
	cmd.Flags().StringVar(endpoint, "endpoint", "", "Control plane holding the app: one this project is bound to (.forge/hosted-projects.json) or Reliant cloud; https, or http to localhost only (default: the one this project is bound to)")
}

// resolveNamedApp resolves the app and refuses a directory with no app name.
func resolveNamedApp(retry, token, endpoint string) (appTarget, error) {
	t, err := resolveAppTarget(retry, token, endpoint)
	if err != nil {
		return appTarget{}, err
	}
	return t, requireAppName(t)
}

func newAppStopCmd() *cobra.Command {
	var yes bool
	var token, endpoint string
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Suspend every hosted environment of this app (storage keeps billing)",
		Long: `Suspend every deployment of every hosted environment of this app: the loop
` + "`forge env stop <env>`" + ` would be for each env, with each env's outcome printed.

  forge app stop          prompts: type the app name to confirm
  forge app stop --yes    no prompt (required with no TTY or CI=1)

URLs, data, secrets and custom domains are kept, and compute stops billing.
Storage keeps billing (database volumes, a static site's files): ` + "`forge app teardown`" + `
is the state that bills nothing. Resume an env with ` + "`forge env start <env>`" + `.

An env you may not write (deploy:write on it) is refused by the control plane,
reported, and left running; the others are still stopped, and the command exits
non-zero.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Before anything is resolved or sent: a non-interactive run
			// without --yes must never hang on a prompt.
			if err := requireConfirmable(yes, "stop", hostedProjectName()); err != nil {
				return err
			}
			t, err := resolveNamedApp("forge app stop", token, endpoint)
			if err != nil {
				return err
			}
			return runAppStop(cmd.Context(), t, yes, lifecycleStdin, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm without the typed-name prompt (required when non-interactive)")
	addAppTargetFlags(cmd, &token, &endpoint)
	return cmd
}

func newAppTeardownCmd() *cobra.Command {
	var opts appTeardownOptions
	var token, endpoint string
	cmd := &cobra.Command{
		Use:   "teardown",
		Short: "Delete every hosted environment of this app, keeping the app (destructive)",
		Long: `Delete every hosted environment of this app — ` + "`forge env delete <env>`" + ` for each
env, with each env's outcome printed — and keep the app itself: its name, its
release history and release images (not billed), and its custom domains
(unbound, still in your organization). Afterwards nothing runs or bills, and
` + "`forge env deploy <env> <release>`" + ` recreates an env from the same releases.

  forge app teardown              prompts: type the app name to confirm
  forge app teardown --yes        no prompt (required with no TTY or CI=1)
  forge app teardown --keep-data  keep the databases (still billed as storage)

By default each env's managed databases' data is destroyed with it
(IRREVERSIBLE). --keep-data RETAINS them, as ` + "`forge env delete --keep-data`" + ` does:
they keep billing as storage, are listed by ` + "`forge env retained list`" + ` until
purged, and are NOT reattached by a later deploy. Each env's secrets stop being
delivered and are not given back to a recreated env.

Before asking for confirmation it shows every env with its id, deployment count
and bound custom domains, and deletes nothing when any domain is still bound
(the control plane refuses to delete an env while one is: ` + "`forge domain unbind`" + `
it first) or when an env this project declares also runs parts on a cluster you
operate. It also deletes nothing when the bound domains cannot be read — the
domain service failing, or a control plane without one. Tearing down an app
needs an org owner or admin.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Before anything is resolved or sent: a non-interactive run
			// without --yes must never hang on a prompt.
			if err := requireConfirmable(opts.Yes, "tear down", hostedProjectName()); err != nil {
				return err
			}
			t, err := resolveNamedApp("forge app teardown", token, endpoint)
			if err != nil {
				return err
			}
			return runAppTeardown(cmd.Context(), t, opts, lifecycleStdin, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&opts.Yes, "yes", false, "Confirm without the typed-name prompt (required when non-interactive)")
	cmd.Flags().BoolVar(&opts.KeepData, "keep-data", false, "Keep every env's managed databases (retained, still billed as storage) instead of destroying them")
	addAppTargetFlags(cmd, &token, &endpoint)
	return cmd
}

func newAppDeleteCmd() *cobra.Command {
	var yes, cancel, status bool
	var token, endpoint string
	cmd := &cobra.Command{
		Use:   "delete [--cancel | --status]",
		Short: "Schedule this hosted app's deletion, cancel it, or show it (destructive)",
		Long: `Delete this hosted app: everything that names it, and its name.

  forge app delete            prompts: type the app name to confirm
  forge app delete --yes      no prompt (required with no TTY or CI=1)
  forge app delete --status   show the scheduled deletion, if any
  forge app delete --cancel   cancel it, until the purge starts

Deleting is SCHEDULED, not immediate. At once, every env is stopped (compute
stops billing) and deploys, promotions and new envs of the app are refused.
After a grace window (7 days) the control plane purges every env and its data,
kept databases, release history and registry images, custom domains and their
certificates, and secrets, then frees the name. Activity, audit and spend
history are kept. --cancel keeps the app and starts again the deployments the
deletion stopped; one stopped before it stays stopped.

To keep the app's name and releases and still stop every charge, use
` + "`forge app teardown`" + `.

Deleting an app, and cancelling that, needs an org owner or admin signed in as
a person, with a live step-up on every protected environment of the app; the
control plane's refusal is printed as it is sent. A control plane that predates
app deletion is reported as such, and nothing is changed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cancel && status {
				return errors.New("--cancel and --status are different requests\nfix: pass one of them")
			}
			if !cancel && !status {
				if err := requireConfirmable(yes, "delete", hostedProjectName()); err != nil {
					return err
				}
			}
			t, err := resolveNamedApp("forge app delete", token, endpoint)
			if err != nil {
				return err
			}
			switch {
			case status:
				return runAppDeletionStatus(cmd.Context(), t, cmd.OutOrStdout())
			case cancel:
				return runAppDeleteCancel(cmd.Context(), t, cmd.OutOrStdout())
			}
			return runAppDelete(cmd.Context(), t, yes, lifecycleStdin, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm without the typed-name prompt (required when non-interactive)")
	cmd.Flags().BoolVar(&cancel, "cancel", false, "Cancel the app's scheduled deletion (until its purge starts)")
	cmd.Flags().BoolVar(&status, "status", false, "Show the app's scheduled deletion, if any, and change nothing")
	addAppTargetFlags(cmd, &token, &endpoint)
	return cmd
}
