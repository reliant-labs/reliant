// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
trigger that is already activated is brought to the flags, not duplicated.`,
		Example: `  # Enable a GitHub-event workflow for three repositories on one machine
  reliant trigger activate --workflow pr-review --machine 2aab1465-... \
    --param 'repos=["acme/api","acme/web"]' --name-prefix "PR pipeline · pr-review"

  # Just one of its declared triggers
  reliant trigger activate --workflow release --trigger merged --machine build-box \
    --params-json '{"repos":["acme/api"],"promote_prod":false}'`,
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
				return conn.annotate(fmt.Errorf("enabling workflow %q: %w", f.workflow, err))
			}
			printWorkflowTriggerResults(cmd.OutOrStdout(), resp.Msg.GetResults())
			if !resp.Msg.GetEnabled() {
				return fmt.Errorf("workflow %q was not enabled; nothing new is running (see the reasons above)", f.workflow)
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
	fl.StringVar(&f.message, "message", "", "Prompt for a declared trigger that has none")
	fl.StringVar(&f.namePrefix, "name-prefix", "", "Names new activations \"<prefix> · <trigger>\" (default: the workflow)")
	fl.StringVar(&f.actingAs, "acting-as", "", "Who GitHub writes speak as: app (the Reliant bot) or owner (default: app for a new activation, unchanged otherwise)")
	fl.BoolVar(&f.notify, "notify", false, "Notify you when a run completes")
	return cmd
}

// newTriggerDeactivateCmd pauses every activation of a workflow, the inverse
// of activate. Pausing is the safe direction, so an agent may run it.
func newTriggerDeactivateCmd() *cobra.Command {
	var workflow, projectID string
	cmd := &cobra.Command{
		Use:   "deactivate",
		Short: "Pause every activation of a workflow",
		Long: `Pause every activation of a workflow, as the app's Disable does.

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
			req := &reliantv1.DisableWorkflowRequest{Workflow: workflow}
			if projectID != "" {
				req.ProjectId = &projectID
			}
			resp, err := newTriggerServiceClient(conn).DisableWorkflow(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				return conn.annotate(fmt.Errorf("disabling workflow %q: %w", workflow, err))
			}
			out := cmd.OutOrStdout()
			for _, t := range resp.Msg.GetPaused() {
				fmt.Fprintf(out, "paused  %s (%s)\n", t.GetName(), t.GetId())
			}
			for _, fail := range resp.Msg.GetFailed() {
				fmt.Fprintf(out, "FAILED  %s (%s): %s\n", fail.GetName(), fail.GetTriggerId(), fail.GetMessage())
			}
			if len(resp.Msg.GetFailed()) > 0 {
				return fmt.Errorf("%d activation(s) of %q could not be paused", len(resp.Msg.GetFailed()), workflow)
			}
			if len(resp.Msg.GetPaused()) == 0 {
				fmt.Fprintf(out, "No enabled activations of %q.\n", workflow)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workflow, "workflow", "", "Workflow whose activations to pause (required)")
	cmd.Flags().StringVar(&projectID, "project-id", "", "Pause only this project's activations (default: every project)")
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
	return c, nil
}

func (f *activateFlags) enableRequest(c *activateChoices) *reliantv1.EnableWorkflowRequest {
	return &reliantv1.EnableWorkflowRequest{
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
}

func workflowOutcomeLabel(o reliantv1.WorkflowTriggerOutcome) string {
	label := strings.TrimPrefix(o.String(), "WORKFLOW_TRIGGER_OUTCOME_")
	return strings.ToLower(strings.ReplaceAll(label, "_", " "))
}
