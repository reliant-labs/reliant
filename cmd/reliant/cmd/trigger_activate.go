// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// `reliant trigger activate` turns on the triggers a workflow declares in its
// own `triggers:` block — the same act as the app's Enable dialog, through the
// same RPC (TriggerService.EnableWorkflow), so the CLI is never a lesser way
// to set up an automation than the app. Naming --trigger narrows that same
// call to those declared triggers (EnableWorkflowRequest.triggers), so an
// existing activation is updated in place rather than created a second time.

// activateFlags are the choices an activation makes: where its runs execute,
// as whom, and with which inputs.
type activateFlags struct {
	workflow    string
	triggers    []string
	projectPath string
	projectID   string
	worktreeID  string
	machine     string
	noMachine   bool
	// daemonLister overrides the client built from the connection; tests set it.
	daemonLister triggerDaemonLister
	params       []string
	paramsJSON   string
	presets      []string
	connections  []string
	message      string
	namePrefix   string
	actingAs     string
	notify       bool
	// hmacSecretFiles are --hmac-secret-file entries, trigger=path.
	hmacSecretFiles []string
}

// activateChoices is activateFlags resolved against the server: ids instead
// of paths and hostnames, typed values instead of strings.
type activateChoices struct {
	projectID   string
	worktreeID  *string
	daemonID    string
	noMachine   bool
	params      map[string]*structpb.Value
	presets     map[string]string
	connections map[string]string
	acting      reliantv1.ActingIdentity
	// hmacSecrets are signing secrets by declared trigger name.
	hmacSecrets map[string]string
}

func newTriggerActivateCmd() *cobra.Command {
	var f activateFlags
	cmd := &cobra.Command{
		Use:     "activate",
		PreRunE: refuseFromAgentShell,
		Short:   "Turn on the triggers a workflow declares",
		Long: `Turn on the triggers a workflow declares in its triggers: block.

This is the app's Enable dialog as a command, and it calls the same RPC. With
no --trigger it is all or nothing: every declared trigger is validated first,
and if any one cannot be enabled (a missing connection, a required input with
no value) none is, and the reason is printed per trigger. Run it again with
changed flags to change an automation's settings; an existing activation is
brought to them.

--trigger enables only the named declared triggers (all or nothing among
them) and leaves the workflow's other activations as they are. A named
trigger that is already activated is brought to the flags, not duplicated.

A webhook trigger's URL and token are printed when its activation is created,
once: store the token in the sender then. ` + "`reliant trigger rotate`" + ` issues a new
one. A webhook that declares hmac: needs the secret its sender signs with:
--hmac-secret-file <trigger>=<file> reads it from a file ("-" for standard
input), keeping it out of your shell history. It is stored sealed and never
shown; pass it again to replace it.

--notify is about successful runs only. With it, a run that completes
successfully is marked unread, which raises a desktop notification, and stays
out of the archive so the notification has a run to open. It never puts the
run in the Inbox: the Inbox lists only what needs you. Without it a successful
run is silent. Either way, a failed run notifies you once per failure streak
and is an Inbox item until a later run of the same trigger succeeds, and a run
waiting for an answer or an approval, or an automation that is broken, is an
Inbox item. A workflow that has something to tell you after a success says so
itself, with a step that messages you.`,
		Example: `  # Enable a GitHub-event workflow for three repositories on one machine
  reliant trigger activate --workflow pr-review --machine 2aab1465-... \
    --param 'repos=["acme/api","acme/web"]' --name-prefix "PR pipeline · pr-review"

  # Just one of its declared triggers
  reliant trigger activate --workflow release --trigger merged --machine build-box \
    --params-json '{"repos":["acme/api"],"promote_prod":false}'

  # A signed webhook: the signing secret from a file
  reliant trigger activate --workflow deploy-hooks --no-machine \
    --hmac-secret-file signed=./signing-secret.txt`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			if strings.TrimSpace(f.workflow) == "" {
				return fmt.Errorf("--workflow is required")
			}
			choices, err := f.resolve(cmd.Context(), cmd, conn)
			if err != nil {
				return err
			}
			req := f.enableRequest(choices)
			resp, err := newTriggerServiceClient(conn).EnableWorkflow(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				return conn.annotate(fmt.Errorf("enabling workflow %q: %w%s", f.workflow, err, builtinRefHint(f.workflow)))
			}
			printWorkflowTriggerResults(cmd.OutOrStdout(), resp.Msg.GetResults())
			if !resp.Msg.GetEnabled() {
				if hint := builtinRefHint(f.workflow); hint != "" {
					return fmt.Errorf("%w%s", activateFailure(f.workflow, resp.Msg.GetResults()), hint)
				}
				return activateFailure(f.workflow, resp.Msg.GetResults())
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.workflow, "workflow", "", "Workflow whose declared triggers to turn on: a slug, or builtin://<name> (required)")
	fl.StringArrayVar(&f.triggers, "trigger", nil, "Activate only this declared trigger; repeatable (default: all of them, all or nothing)")
	fl.StringVar(&f.projectPath, "project", "", "Project path (resolved to a project id; defaults to the working directory)")
	fl.StringVar(&f.projectID, "project-id", "", "Project id, if you already have it")
	fl.StringVar(&f.worktreeID, "worktree", "", "Worktree id the runs execute in (default: the project's main worktree)")
	fl.StringVar(&f.machine, "machine", "", "Machine the runs execute on, by daemon id or hostname (or --no-machine)")
	fl.BoolVar(&f.noMachine, "no-machine", false, "Run without a machine (workflows that only need the web and integrations)")
	fl.StringArrayVar(&f.params, "param", nil, "Workflow input as key=value; repeatable. Values parse as JSON, else as a string")
	fl.StringVar(&f.paramsJSON, "params-json", "", "Workflow inputs as one JSON object; --param entries win over its keys")
	fl.StringArrayVar(&f.presets, "preset", nil, "Preset assignment as group=name, or just name for the top level; repeatable")
	fl.StringArrayVar(&f.connections, "connection", nil, "Connection an integration listens through, as integration=connection-id (default: your default connection)")
	fl.StringVar(&f.message, "message", "", "Prompt for a declared trigger that has none (not needed when the workflow runs no agent)")
	fl.StringVar(&f.namePrefix, "name-prefix", "", "Names new activations \"<prefix> · <trigger>\" (default: the workflow)")
	fl.StringVar(&f.actingAs, "acting-as", "", "Who GitHub writes speak as: app (the Reliant bot) or owner (default: app for a new activation, unchanged otherwise)")
	fl.BoolVar(&f.notify, "notify", false, "Desktop notification when a run succeeds; never an Inbox item (failures always notify)")
	fl.StringArrayVar(&f.hmacSecretFiles, "hmac-secret-file", nil, "Signing secret of a webhook trigger that declares hmac:, as trigger=path (path - reads standard input); repeatable")
	return cmd
}

// newTriggerDeactivateCmd pauses every activation of a workflow, the inverse
// of activate. Pausing is the safe direction, so an agent may run it.
func newTriggerDeactivateCmd() *cobra.Command {
	var workflow, projectID, projectPath string
	cmd := &cobra.Command{
		Use:   "deactivate",
		Short: "Pause every activation of a workflow",
		Long: `Pause every activation of a workflow, as the app's Disable does.

With no --project or --project-id it pauses the workflow's activations in
EVERY project, so it says so first, and each paused line names its project.
Pass --project (a path) or --project-id to pause one project's only.

It is not all or nothing: a pause that fails is reported and the rest still
pause. Resume with activate; remove one activation with delete.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(workflow) == "" {
				return fmt.Errorf("--workflow is required")
			}
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			if projectID == "" && projectPath != "" {
				id, err := resolveProjectPathToID(cmd.Context(), conn, projectPath)
				if err != nil {
					return fmt.Errorf("resolving project %q: %w", projectPath, err)
				}
				if id == "" {
					return fmt.Errorf("could not resolve a project for %q; pass --project-id", projectPath)
				}
				projectID = id
			}
			req := &reliantv1.DisableWorkflowRequest{Workflow: workflow}
			if projectID != "" {
				req.ProjectId = &projectID
			} else {
				// The unscoped default is kept for compatibility, but it is
				// never silent: it reaches production automations in every
				// project (TL-38).
				fmt.Fprintf(cmd.ErrOrStderr(), "Pausing %q in every project (pass --project or --project-id to pause one project's only).\n", workflow)
			}
			resp, err := newTriggerServiceClient(conn).DisableWorkflow(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				return conn.annotate(fmt.Errorf("disabling workflow %q: %w%s", workflow, err, builtinRefHint(workflow)))
			}
			out := cmd.OutOrStdout()
			for _, t := range resp.Msg.GetPaused() {
				fmt.Fprintf(out, "paused  %s (%s)  project %s\n", t.GetName(), t.GetId(), t.GetProjectId())
			}
			for _, fail := range resp.Msg.GetFailed() {
				fmt.Fprintf(out, "FAILED  %s (%s): %s\n", fail.GetName(), fail.GetTriggerId(), fail.GetMessage())
			}
			if len(resp.Msg.GetFailed()) > 0 {
				return fmt.Errorf("%d activation(s) of %q could not be paused", len(resp.Msg.GetFailed()), workflow)
			}
			if len(resp.Msg.GetPaused()) == 0 {
				fmt.Fprintf(out, "No enabled activations of %q.%s\n", workflow, builtinRefHint(workflow))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workflow, "workflow", "", "Workflow whose activations to pause: a slug, or builtin://<name> (required)")
	cmd.Flags().StringVar(&projectPath, "project", "", "Pause only the activations of the project at this path")
	cmd.Flags().StringVar(&projectID, "project-id", "", "Pause only this project's activations (default, with no --project either: every project)")
	return cmd
}

func (f *activateFlags) resolve(ctx context.Context, cmd *cobra.Command, conn *connection) (*activateChoices, error) {
	if f.noMachine == (f.machine != "") {
		return nil, fmt.Errorf("pass exactly one of --machine or --no-machine: an activation must say where its runs execute")
	}
	projectID := f.projectID
	if projectID == "" {
		path := f.projectPath
		if path == "" {
			path = "."
		}
		id, err := resolveProjectPathToID(ctx, conn, path)
		if err != nil {
			return nil, fmt.Errorf("resolving project %q: %w", path, err)
		}
		if id == "" {
			return nil, fmt.Errorf("could not resolve a project for %q; pass --project-id", path)
		}
		projectID = id
	}
	c := &activateChoices{projectID: projectID, noMachine: f.noMachine}
	if f.worktreeID != "" {
		c.worktreeID = &f.worktreeID
	}
	if f.machine != "" {
		lister := f.daemonLister
		if lister == nil {
			lister = newDaemonRegistryClient(conn)
		}
		id, err := resolveTriggerDaemon(ctx, lister, f.machine)
		if err != nil {
			return nil, err
		}
		c.daemonID = id
	}
	params, err := mergeActivateParams(f.paramsJSON, f.params)
	if err != nil {
		return nil, err
	}
	c.params = params
	if c.presets, err = parseTriggerPresets(f.presets); err != nil {
		return nil, err
	}
	if c.connections, err = parseActivateConnections(f.connections); err != nil {
		return nil, err
	}
	if c.acting, err = parseActingIdentity(f.actingAs); err != nil {
		return nil, err
	}
	if c.hmacSecrets, err = readHMACSecretFiles(f.hmacSecretFiles, cmd.InOrStdin()); err != nil {
		return nil, err
	}
	return c, nil
}

// readHMACSecretFiles reads --hmac-secret-file entries into secrets by
// trigger name. A secret is read from a file, never taken as a flag value,
// so it stays out of shell history and the process list. One trailing line
// ending is dropped: `echo secret > file` must not sign with "secret\n".
func readHMACSecretFiles(entries []string, stdin io.Reader) (map[string]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(entries))
	usedStdin := false
	for _, entry := range entries {
		trigger, path, found := strings.Cut(entry, "=")
		trigger = strings.TrimSpace(trigger)
		if !found || trigger == "" || path == "" {
			return nil, fmt.Errorf("--hmac-secret-file %q must be trigger=path (path - reads standard input)", entry)
		}
		if _, dup := out[trigger]; dup {
			return nil, fmt.Errorf("--hmac-secret-file names trigger %q twice", trigger)
		}
		var raw []byte
		var err error
		if path == "-" {
			if usedStdin {
				return nil, fmt.Errorf("--hmac-secret-file: only one secret can be read from standard input")
			}
			usedStdin = true
			raw, err = io.ReadAll(stdin)
		} else {
			raw, err = os.ReadFile(path)
		}
		if err != nil {
			return nil, fmt.Errorf("--hmac-secret-file %s: reading the secret: %w", trigger, err)
		}
		secret := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
		if secret == "" {
			return nil, fmt.Errorf("--hmac-secret-file %s: the secret is empty", trigger)
		}
		out[trigger] = secret
	}
	return out, nil
}

func (f *activateFlags) enableRequest(c *activateChoices) *reliantv1.EnableWorkflowRequest {
	// A signing secret is per trigger, so it rides as that trigger's
	// override; sorted so the request is the same for the same flags.
	var overrides []*reliantv1.WorkflowTriggerOverride
	for _, name := range slices.Sorted(maps.Keys(c.hmacSecrets)) {
		secret := c.hmacSecrets[name]
		overrides = append(overrides, &reliantv1.WorkflowTriggerOverride{Trigger: name, WebhookHmacSecret: &secret})
	}
	return &reliantv1.EnableWorkflowRequest{
		Overrides:        overrides,
		Workflow:         f.workflow,
		ProjectId:        c.projectID,
		WorktreeId:       c.worktreeID,
		DaemonId:         c.daemonID,
		NoMachine:        c.noMachine,
		ConnectionIds:    c.connections,
		Params:           c.params,
		Presets:          c.presets,
		NotifyOnComplete: f.notify,
		Message:          f.message,
		NamePrefix:       f.namePrefix,
		ActingIdentity:   c.acting,
		Triggers:         f.triggers,
	}
}

// mergeActivateParams reads --params-json, then lays the --param entries
// over it, so a single key can be corrected without retyping the object.
func mergeActivateParams(paramsJSON string, entries []string) (map[string]*structpb.Value, error) {
	out := map[string]*structpb.Value{}
	if strings.TrimSpace(paramsJSON) != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(paramsJSON), &obj); err != nil {
			return nil, fmt.Errorf("--params-json must be a JSON object: %w", err)
		}
		for k, v := range obj {
			value, err := structpb.NewValue(v)
			if err != nil {
				return nil, fmt.Errorf("--params-json key %q is not representable: %w", k, err)
			}
			out[k] = value
		}
	}
	flagParams, err := parseTriggerParams(entries)
	if err != nil {
		return nil, err
	}
	for k, v := range flagParams {
		out[k] = v
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func parseActivateConnections(entries []string) (map[string]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		integration, id, found := strings.Cut(entry, "=")
		if !found || integration == "" || id == "" {
			return nil, fmt.Errorf("--connection %q must be integration=connection-id", entry)
		}
		out[integration] = id
	}
	return out, nil
}

func parseActingIdentity(s string) (reliantv1.ActingIdentity, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return reliantv1.ActingIdentity_ACTING_IDENTITY_UNSPECIFIED, nil
	case "owner":
		return reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER, nil
	case "app":
		return reliantv1.ActingIdentity_ACTING_IDENTITY_APP, nil
	default:
		return 0, fmt.Errorf("--acting-as must be owner or app, got %q", s)
	}
}

func printWorkflowTriggerResults(out io.Writer, results []*reliantv1.WorkflowTriggerResult) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TRIGGER\tOUTCOME\tACTIVATION\tNOTE")
	for _, r := range results {
		activation := "-"
		if a := r.GetActivation(); a != nil {
			activation = fmt.Sprintf("%s (%s)", a.GetName(), a.GetId())
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.GetTrigger(), workflowOutcomeLabel(r.GetOutcome()), activation, orDash(r.GetMessage()))
	}
	_ = w.Flush()
	for _, r := range results {
		a := r.GetActivation()
		switch {
		case r.GetWebhook().GetToken() != "":
			// The one time the server returns the token: a CLI that does
			// not print it leaves the activation uncallable until a rotate.
			printWebhookCredential(out, r.GetTrigger(), a.GetId(), a.GetWebhookUrl(), r.GetWebhook())
		case a.GetWebhookManaged() && r.GetOutcome() == reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ENABLED:
			fmt.Fprintf(out, "\nWebhook %s: Reliant delivers to it itself, so there is no URL or token to configure.\n", r.GetTrigger())
		}
	}
	// The server's warnings carry the funding warning's message too, for a
	// client that predates that field, so printing them covers both.
	for _, r := range results {
		for _, warning := range r.GetWarnings() {
			fmt.Fprintf(out, "\nWarning (%s): %s\n", r.GetTrigger(), warning)
		}
	}
}

// printWebhookCredential prints a webhook trigger's token and the two ways to
// call it with it. label names the trigger; bareURL is Trigger.webhook_url,
// derived from the credential's URL when the server sent none.
func printWebhookCredential(out io.Writer, label, triggerID, bareURL string, cred *reliantv1.WebhookCredential) {
	token := cred.GetToken()
	if bareURL == "" {
		bareURL = strings.TrimSuffix(cred.GetUrl(), "/"+token)
	}
	fmt.Fprintf(out, "\nWebhook %s (the token is shown once; `reliant trigger rotate %s` issues a new one):\n", label, triggerID)
	fmt.Fprintf(out, "  Token:    %s\n", token)
	fmt.Fprintf(out, "  POST to:  %s with the header \"Authorization: Bearer <token>\"\n", bareURL)
	if u := cred.GetUrl(); u != "" {
		fmt.Fprintf(out, "  or to:    %s (for a sender that cannot set headers; keep it as secret as the token)\n", u)
	}
}

func workflowOutcomeLabel(o reliantv1.WorkflowTriggerOutcome) string {
	label := strings.TrimPrefix(o.String(), "WORKFLOW_TRIGGER_OUTCOME_")
	return strings.ToLower(strings.ReplaceAll(label, "_", " "))
}

// activateFailure is the error for an enable that did not complete. "Nothing
// new is running" is true only when no trigger was left enabled: an undo that
// failed (a request deadline mid-write, say) leaves one on, and saying
// otherwise sent people to re-run an activation that was already live.
// builtinRefHint is a suffix naming the built-in a bare --workflow may have
// meant: activate resolves a bare name as a project workflow, never a
// built-in, so `--workflow autopilot-oncall` failed with "no project workflow
// is named autopilot-oncall" and no word of builtin://autopilot-oncall
// (TL-45). Empty when the name has a scheme or no built-in has it.
func builtinRefHint(workflow string) string {
	if ref := normalizeRunWorkflowRef(workflow); ref != workflow {
		return fmt.Sprintf("\n  a built-in workflow is named %q: did you mean --workflow %s?", workflow, ref)
	}
	return ""
}

func activateFailure(workflow string, results []*reliantv1.WorkflowTriggerResult) error {
	var left []string
	for _, r := range results {
		if r.GetOutcome() == reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_LEFT_ENABLED {
			left = append(left, r.GetTrigger())
		}
	}
	if len(left) > 0 {
		return fmt.Errorf("workflow %q was not fully enabled, and these triggers are enabled anyway: %s (see the reasons above; `reliant trigger list` shows what is on)",
			workflow, strings.Join(left, ", "))
	}
	return fmt.Errorf("workflow %q was not enabled; nothing new is running (see the reasons above)", workflow)
}
