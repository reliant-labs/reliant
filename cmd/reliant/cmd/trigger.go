// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/sdk/go/reliant"
)

// errAgentCannotEnable is what create, update, enable and activate say when an
// agent's shell runs them. Enabling an automation is the user's act: in the
// app, or by approving an agent's create_trigger_activation request in chat.
var errAgentCannotEnable = fmt.Errorf("an agent cannot create or enable automations from its shell: request it with the create_trigger_activation tool (the user approves it in chat), or ask the user to turn it on in the app (Workflows, open the workflow, Enable)")

// refuseFromAgentShell is the PreRunE of the commands that start something
// running on its own. See daemonpolicy.AgentShellEnv for why this is a
// guardrail and not a boundary.
func refuseFromAgentShell(*cobra.Command, []string) error {
	if os.Getenv(daemonpolicy.AgentShellEnv) != "" { //nolint:forbidigo // the marker the daemon sets on agent shells (daemonpolicy.AgentShellEnv), a process signal, not configuration
		return errAgentCannotEnable
	}
	return nil
}

// `reliant trigger` manages standing instructions that start runs without a
// human typing. See research/TRIGGERS.md.
//
// Every subcommand takes a trigger by id OR by name, because a human reading
// `trigger list` has the name in front of them and an id nobody can remember.
// Name resolution is a client-side list-and-match: names are unique per user
// per project, so an ambiguous name is reported rather than guessed at.

func newTriggerServiceClient(conn *connection) reliantv1connect.TriggerServiceClient {
	return reliantv1connect.NewTriggerServiceClient(conn.httpClient(), conn.ServerURL)
}

// newTriggerCmd builds the `reliant trigger` command tree.
func newTriggerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trigger",
		Short: "Manage triggers and workflow activations",
		Long: `Manage triggers — standing instructions that start runs without a human typing.

A schedule trigger runs a workflow in a project on a cron or interval
schedule, seeded with a fixed prompt. Its runs are unattended: nobody will
answer a question or approve a request, so the agent is told to decide and
proceed.`,
	}
	cmd.AddCommand(
		newTriggerCreateCmd(),
		newTriggerListCmd(),
		newTriggerGetCmd(),
		newTriggerUpdateCmd(),
		newTriggerEnableCmd(),
		newTriggerDisableCmd(),
		newTriggerDeleteCmd(),
		newTriggerFireCmd(),
		newTriggerEventsCmd(),
		newTriggerActivateCmd(),
		newTriggerDeactivateCmd(),
		newTriggerRotateCmd(),
	)
	cmd.PersistentFlags().Bool("daemon-session", false, "Use this server's existing signed-in daemon account without widening its permissions")
	return cmd
}

// triggerDefinitionFlags are the fields shared by create and update.
type triggerDefinitionFlags struct {
	name        string
	projectPath string
	projectID   string
	worktreeID  string
	daemon      string
	// daemonLister overrides the registry client built from the connection;
	// tests set it.
	daemonLister  triggerDaemonLister
	workflow      string
	message       string
	cron          []string
	interval      string
	timezone      string
	overlap       string
	catchupWindow string
	presets       []string
	params        []string
	disabled      bool
}

func (f *triggerDefinitionFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.name, "name", "", "Trigger name, unique per project (defaults to the workflow name)")
	fl.StringVar(&f.projectPath, "project", "", "Project path (resolved to a project id; defaults to the working directory)")
	fl.StringVar(&f.projectID, "project-id", "", "Project id, if you already have it")
	fl.StringVar(&f.worktreeID, "worktree", "", "Worktree id to run in (default: the project's main worktree)")
	fl.StringVar(&f.daemon, "daemon", "", "Daemon the runs execute on, by id or hostname (required)")
	fl.StringVar(&f.workflow, "workflow", "", "Workflow to run (default: your default workflow)")
	fl.StringVar(&f.message, "message", "", "The prompt each run starts from (required)")
	fl.StringArrayVar(&f.cron, "cron", nil, "5-field cron expression; repeatable, and the union of all of them")
	fl.StringVar(&f.interval, "interval", "", "Fire every interval, as a Go duration (e.g. 30m); at least 1m")
	fl.StringVar(&f.timezone, "timezone", "", "IANA zone the cron expressions are read in (default: UTC)")
	fl.StringVar(&f.overlap, "overlap", "", "What to do when the previous run is still going: skip (default) or allow")
	fl.StringVar(&f.catchupWindow, "catchup-window", "", "How late a fire missed during an outage may still run (default: 10m)")
	fl.StringArrayVar(&f.presets, "preset", nil, "Preset assignment as group=name, or just name for the top level; repeatable")
	fl.StringArrayVar(&f.params, "param", nil, "Workflow input as key=value; repeatable. Values parse as JSON, else as a string")
}

// definition builds the wire definition. forUpdate selects the enabled
// semantics: unset means "true" on create and "leave it alone" on update, so
// in neither case does omitting the flag change a trigger's enabled state
// against the user's wishes.
func (f *triggerDefinitionFlags) definition(
	ctx context.Context,
	cmd *cobra.Command,
	conn *connection,
	forUpdate bool,
) (*reliantv1.TriggerDefinition, error) {
	if f.message == "" {
		return nil, fmt.Errorf("--message is required: a trigger with no prompt would start a run with nothing to do")
	}
	if len(f.cron) == 0 && f.interval == "" {
		return nil, fmt.Errorf("a schedule needs --cron or --interval")
	}

	projectID, err := f.resolveProjectID(ctx, cmd, conn)
	if err != nil {
		return nil, err
	}
	if f.daemon == "" {
		return nil, fmt.Errorf("--daemon is required: a trigger must name the daemon its runs execute on")
	}
	lister := f.daemonLister
	if lister == nil {
		lister = newDaemonRegistryClient(conn)
	}
	daemonID, err := resolveTriggerDaemon(ctx, lister, f.daemon)
	if err != nil {
		return nil, err
	}

	presets, err := parseTriggerPresets(f.presets)
	if err != nil {
		return nil, err
	}
	params, err := parseTriggerParams(f.params)
	if err != nil {
		return nil, err
	}

	overlap, err := parseTriggerOverlap(f.overlap)
	if err != nil {
		return nil, err
	}

	src := &reliantv1.ScheduleSource{
		Cron:     f.cron,
		Timezone: f.timezone,
		Overlap:  overlap,
	}
	if f.interval != "" {
		interval := f.interval
		src.Interval = &interval
	}
	if f.catchupWindow != "" {
		catchup := f.catchupWindow
		src.CatchupWindow = &catchup
	}

	name := f.name
	if name == "" {
		name = f.workflow
	}
	if name == "" {
		return nil, fmt.Errorf("--name is required when --workflow is not given")
	}

	def := &reliantv1.TriggerDefinition{
		Name:      name,
		ProjectId: projectID,
		DaemonId:  daemonID,
		Workflow:  f.workflow,
		Presets:   presets,
		Params:    params,
		Message:   f.message,
		Source:    &reliantv1.TriggerDefinition_Schedule{Schedule: src},
	}
	if f.worktreeID != "" {
		def.WorktreeId = &f.worktreeID
	}

	// Leave enabled UNSET unless the user said something. The server reads nil
	// as true on create and as "unchanged" on update, so an update that is
	// only editing the schedule cannot silently resume a paused trigger.
	if f.disabled {
		enabled := false
		def.Enabled = &enabled
	} else if cmd.Flags().Changed("disabled") {
		enabled := true
		def.Enabled = &enabled
	}
	return def, nil
}

func (f *triggerDefinitionFlags) resolveProjectID(
	ctx context.Context,
	cmd *cobra.Command,
	conn *connection,
) (string, error) {
	if f.projectID != "" {
		return f.projectID, nil
	}
	path := f.projectPath
	if path == "" {
		path = "."
	}
	id, err := resolveProjectPathToID(ctx, conn, path)
	if err != nil {
		return "", fmt.Errorf("resolving project %q: %w", path, err)
	}
	if id == "" {
		return "", fmt.Errorf("could not resolve a project for %q; pass --project-id", path)
	}
	return id, nil
}

func newTriggerCreateCmd() *cobra.Command {
	var flags triggerDefinitionFlags
	cmd := &cobra.Command{
		Use:     "create",
		PreRunE: refuseFromAgentShell,
		Short:   "Create a schedule trigger",
		Example: `  # Every weekday at 9am, in the project in the working directory, on the
  # machine with hostname "build-box"
  reliant trigger create --name "morning audit" --cron "0 9 * * 1-5" --daemon build-box \
    --message "Review yesterday's CI failures and open issues for anything actionable."

  # Every 30 minutes, with a workflow and an input
  reliant trigger create --name sweep --interval 30m --daemon build-box \
    --workflow builtin://agent --param depth=2 --message "Sweep the dependency tree."`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			def, err := flags.definition(cmd.Context(), cmd, conn, false)
			if err != nil {
				return err
			}
			resp, err := newTriggerServiceClient(conn).CreateTrigger(cmd.Context(),
				connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: def}))
			if err != nil {
				return conn.annotate(fmt.Errorf("creating trigger: %w", err))
			}
			printTriggerDetail(cmd.OutOrStdout(), resp.Msg.GetTrigger())
			printFundingWarning(cmd.OutOrStdout(), resp.Msg.GetFundingWarning())
			return nil
		},
	}
	flags.bind(cmd)
	cmd.Flags().BoolVar(&flags.disabled, "disabled", false, "Create the trigger paused")
	return cmd
}

// keepIdentityOf defaults an update's name and project to the trigger's own,
// so an update that does not mean to rename or move a trigger does neither.
// Without it an update with --workflow and no --name renamed the trigger to
// its workflow's name (create's default), and the next `trigger fire <name>`
// found nothing by its old name.
func (f *triggerDefinitionFlags) keepIdentityOf(existing *reliantv1.Trigger) {
	if f.name == "" {
		f.name = existing.GetName()
	}
	// Updating a schedule from another directory does not relocate it.
	if f.projectID == "" && f.projectPath == "" {
		f.projectID = existing.GetProjectId()
	}
}

func newTriggerUpdateCmd() *cobra.Command {
	var flags triggerDefinitionFlags
	var orgAccountsOnly bool
	cmd := &cobra.Command{
		Use:     "update <id-or-name>",
		PreRunE: refuseFromAgentShell,
		Short:   "Replace a trigger's definition",
		Long: `Replace a trigger's definition.

This is a full replacement, not a patch: every field is written as given, so
flags you omit are written as their zero value. The exceptions are enabled,
which is left as it is unless you pass --disabled or --disabled=false, and the
trigger's name and project, which are kept unless you pass --name or
--project-id / --project.

--org-accounts-only makes the trigger's runs spend only its organization's AI
accounts, never your own. Given alone, it changes only that and keeps the rest
of the stored definition. The CLI only tightens: letting an automation spend
your own accounts is done in the app.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			client := newTriggerServiceClient(conn)
			existing, err := resolveTrigger(cmd.Context(), client, args[0])
			if err != nil {
				return conn.annotate(err)
			}
			flags.keepIdentityOf(existing)
			def, err := updateDefinition(cmd, orgAccountsOnly, existing, func() (*reliantv1.TriggerDefinition, error) {
				return flags.definition(cmd.Context(), cmd, conn, true)
			})
			if err != nil {
				return err
			}
			resp, err := client.UpdateTrigger(cmd.Context(),
				connect.NewRequest(&reliantv1.UpdateTriggerRequest{Id: existing.GetId(), Trigger: def}))
			if err != nil {
				return conn.annotate(fmt.Errorf("updating trigger: %w", err))
			}
			printTriggerDetail(cmd.OutOrStdout(), resp.Msg.GetTrigger())
			printFundingWarning(cmd.OutOrStdout(), resp.Msg.GetFundingWarning())
			return nil
		},
	}
	flags.bind(cmd)
	cmd.Flags().BoolVar(&flags.disabled, "disabled", false, "Pause the trigger (omit to leave its current state alone)")
	cmd.Flags().BoolVar(&orgAccountsOnly, orgAccountsOnlyFlag, false, "Run only on the trigger's organization's AI accounts, never your own (alone: changes only that)")
	return cmd
}

func newTriggerListCmd() *cobra.Command {
	var projectID string
	var allProjects, asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your triggers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			filter := reliant.ListTriggers{}
			if !allProjects {
				if projectID == "" {
					// Default to the project in the working directory, which
					// is what someone standing in a repo means by "my
					// triggers". --all crosses projects explicitly.
					id, err := resolveProjectPathToID(cmd.Context(), conn, ".")
					if err == nil && id != "" {
						projectID = id
					}
				}
				filter.ProjectID = projectID
			}
			client, err := conn.sdkClient()
			if err != nil {
				return err
			}
			list, err := client.Triggers.List(cmd.Context(), filter).All()
			if err != nil {
				return conn.annotate(fmt.Errorf("listing triggers: %w", sdkCause(err)))
			}
			if asJSON {
				data, err := (protojson.MarshalOptions{UseProtoNames: true, EmitDefaultValues: true}).Marshal(&reliantv1.ListTriggersResponse{Triggers: list})
				if err != nil {
					return fmt.Errorf("encoding triggers: %w", err)
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
				return err
			}
			printTriggerTable(cmd.OutOrStdout(), list)
			return nil
		},
	}
	cmd.Flags().StringVar(&projectID, "project-id", "", "List one project's triggers")
	cmd.Flags().BoolVar(&allProjects, "all", false, "List triggers across every project")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Write the trigger list as protobuf JSON (snake_case fields)")
	return cmd
}

func newTriggerGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id-or-name>",
		Short: "Show one trigger",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			client, err := conn.sdkClient()
			if err != nil {
				return err
			}
			trigger, err := client.Triggers.Get(cmd.Context(), args[0])
			if err != nil {
				return conn.annotate(triggerLookupError(args[0], err))
			}
			label := describeTriggerConnection(cmd.Context(), newConnectionServiceClient(conn), trigger)
			printTriggerDetailWith(cmd.OutOrStdout(), trigger, label)
			return nil
		},
	}
}

func newTriggerEnableCmd() *cobra.Command {
	return newTriggerSetEnabledCmd("enable", "Resume a trigger's schedule", true)
}

func newTriggerDisableCmd() *cobra.Command {
	return newTriggerSetEnabledCmd("disable", "Pause a trigger's schedule", false)
}

func newTriggerSetEnabledCmd(verb, short string, enabled bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   verb + " <id-or-name>",
		Short: short,
		Long: short + `.

Disabling pauses the Temporal schedule rather than deleting it, so the
definition and firing history survive and re-enabling needs no re-derivation.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			client := newTriggerServiceClient(conn)
			trigger, err := resolveTrigger(cmd.Context(), client, args[0])
			if err != nil {
				return conn.annotate(err)
			}
			resp, err := client.SetTriggerEnabled(cmd.Context(),
				connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: trigger.GetId(), Enabled: enabled}))
			if err != nil {
				return conn.annotate(fmt.Errorf("%s trigger: %w", verb, err))
			}
			printTriggerDetail(cmd.OutOrStdout(), resp.Msg.GetTrigger())
			printFundingWarning(cmd.OutOrStdout(), resp.Msg.GetFundingWarning())
			return nil
		},
	}
	// Pausing is the safe direction and stays open to an agent; resuming is
	// enabling.
	if enabled {
		cmd.PreRunE = refuseFromAgentShell
	}
	return cmd
}

func newTriggerDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id-or-name>",
		Short: "Delete a trigger and its schedule",
		Long: `Delete a trigger and its schedule.

The trigger's firing history survives, with its trigger id cleared — the runs
it started are real and still exist.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			client := newTriggerServiceClient(conn)
			trigger, err := resolveTrigger(cmd.Context(), client, args[0])
			if err != nil {
				return conn.annotate(err)
			}
			if _, err := client.DeleteTrigger(cmd.Context(),
				connect.NewRequest(&reliantv1.DeleteTriggerRequest{Id: trigger.GetId()})); err != nil {
				return conn.annotate(fmt.Errorf("deleting trigger: %w", err))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted trigger %q (%s)\n", trigger.GetName(), trigger.GetId())
			return nil
		},
	}
}

// newTriggerRotateCmd replaces a webhook trigger's token, as the app's Rotate
// token does. It is refused in an agent's shell: a new token is a new way in,
// and the person who will hand it to the sender runs it.
func newTriggerRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rotate <id-or-name>",
		PreRunE: refuseFromAgentShell,
		Short:   "Replace a webhook trigger's token",
		Long: `Replace a webhook trigger's token, and print the new one once.

The old token stops working at once, so give the new one to the sender. A
webhook Reliant delivers to itself (a managed webhook, such as alerting) has
no token to print: rotating re-provisions its sender with a new one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			client := newTriggerServiceClient(conn)
			trigger, err := resolveTrigger(cmd.Context(), client, args[0])
			if err != nil {
				return conn.annotate(err)
			}
			resp, err := client.RotateWebhookToken(cmd.Context(),
				connect.NewRequest(&reliantv1.RotateWebhookTokenRequest{Id: trigger.GetId()}))
			if err != nil {
				return conn.annotate(fmt.Errorf("rotating the token of trigger %q: %w", trigger.GetName(), err))
			}
			out := cmd.OutOrStdout()
			rotated := resp.Msg.GetTrigger()
			if rotated == nil {
				rotated = trigger
			}
			if cred := resp.Msg.GetWebhook(); cred.GetToken() != "" {
				fmt.Fprintf(out, "Rotated the token of trigger %q (%s); the old token no longer works.\n", rotated.GetName(), rotated.GetId())
				printWebhookCredential(out, rotated.GetName(), rotated.GetId(), rotated.GetWebhookUrl(), cred)
				return nil
			}
			fmt.Fprintf(out, "Rotated the token of trigger %q (%s). Reliant delivers to this webhook itself, so the new token went to its sender and there is nothing to configure.\n",
				rotated.GetName(), rotated.GetId())
			return nil
		},
	}
}

func newTriggerFireCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fire <id-or-name>",
		Short: "Run a trigger now",
		Long: `Run a trigger now, out of band with its schedule.

A manual fire skips the enabled and overlap checks: asking for it is the
decision those policies exist to make for the unattended case. The firing is
asynchronous — use ` + "`trigger events`" + ` to see what it did.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			client, err := conn.sdkClient()
			if err != nil {
				return err
			}
			trigger, err := client.Triggers.Get(cmd.Context(), args[0])
			if err != nil {
				return conn.annotate(triggerLookupError(args[0], err))
			}
			fired, err := client.Triggers.Fire(cmd.Context(), trigger.GetId())
			if err != nil {
				return conn.annotate(fmt.Errorf("firing trigger: %w", sdkCause(err)))
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Fired trigger %q\n", trigger.GetName())
			fmt.Fprintf(out, "  Fire workflow ID: %s\n", fired.FireWorkflowID)
			fmt.Fprintf(out, "\nThe firing is asynchronous. See what it did with:\n")
			fmt.Fprintf(out, "  reliant trigger events %s\n", trigger.GetId())
			return nil
		},
	}
}

func newTriggerEventsCmd() *cobra.Command {
	var limit int32
	var outcomeFlags []string
	cmd := &cobra.Command{
		Use:   "events <id-or-name>",
		Short: "List a trigger's firings, newest first",
		Long: `Prints one page: at most --limit firings.

--outcome keeps only firings with that outcome: ` + triggerEventOutcomeNames + `.
Repeat it, or separate values with commas, to keep several. The server filters
before it applies --limit, so "--outcome pending --limit 20" is the 20 newest
pending firings however long the trigger's history is.`,
		Example: `  reliant trigger events nightly
  reliant trigger events nightly --outcome pending
  reliant trigger events nightly --outcome failed,skipped --limit 10`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			outcomes, err := parseTriggerEventOutcomes(outcomeFlags)
			if err != nil {
				return err
			}
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			client, err := conn.sdkClient()
			if err != nil {
				return err
			}
			trigger, err := client.Triggers.Get(cmd.Context(), args[0])
			if err != nil {
				return conn.annotate(triggerLookupError(args[0], err))
			}
			// One page, as it always was: a zero limit is the server's default
			// page, and the SDK's iterator would otherwise follow every page.
			if limit <= 0 {
				limit = defaultTriggerEventLimit
			}
			events, err := client.Triggers.Events(cmd.Context(), trigger.GetId(),
				reliant.EventFilter{Outcomes: outcomes, PageSize: int(limit), Limit: int(limit)}).All()
			if err != nil {
				return conn.annotate(fmt.Errorf("listing trigger events: %w", sdkCause(err)))
			}
			// Every v1 server filters by outcome (the request field shipped in
			// #415), but a server that ignored it would return a page that only
			// looks filtered. Keep the matches and say so rather than print it.
			events, dropped := keepTriggerEventOutcomes(events, outcomes)
			if dropped > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: the server ignored --outcome and returned %d other firing(s); "+
					"showing only the matches from its page, which may leave out older matching firings\n", dropped)
			}
			if len(events) == 0 && len(outcomes) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No firings with that outcome.")
				return nil
			}
			printTriggerEventTable(cmd.OutOrStdout(), events)
			return nil
		},
	}
	cmd.Flags().Int32Var(&limit, "limit", 0, "Maximum firings to return (0 applies the server default)")
	cmd.Flags().StringSliceVar(&outcomeFlags, "outcome", nil,
		"Only firings with this outcome: "+triggerEventOutcomeNames+" (repeatable or comma-separated)")
	return cmd
}

// triggerEventOutcomes maps the words --outcome takes to the outcomes
// ListTriggerEvents filters on; the words are the ones the OUTCOME column prints.
var triggerEventOutcomes = map[string]reliantv1.TriggerEventOutcome{
	"pending":  reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_PENDING,
	"launched": reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_LAUNCHED,
	"skipped":  reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_SKIPPED,
	"failed":   reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_FAILED,
}

const triggerEventOutcomeNames = "pending, launched, skipped, failed"

// parseTriggerEventOutcomes turns --outcome values into the request's outcome
// filter, rejecting an unknown word before any request is made. Duplicates
// collapse; no values is no filter.
func parseTriggerEventOutcomes(values []string) ([]reliantv1.TriggerEventOutcome, error) {
	var out []reliantv1.TriggerEventOutcome
	seen := map[reliantv1.TriggerEventOutcome]bool{}
	for _, v := range values {
		word := strings.ToLower(strings.TrimSpace(v))
		o, ok := triggerEventOutcomes[word]
		if !ok {
			return nil, fmt.Errorf("invalid --outcome %q: want one of %s", v, triggerEventOutcomeNames)
		}
		if !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	return out, nil
}

// keepTriggerEventOutcomes keeps the events whose outcome is one of outcomes
// (all of them when outcomes is empty) and counts the ones it dropped.
func keepTriggerEventOutcomes(events []*reliantv1.TriggerEvent, outcomes []reliantv1.TriggerEventOutcome) ([]*reliantv1.TriggerEvent, int) {
	if len(outcomes) == 0 {
		return events, 0
	}
	kept := events[:0:0]
	for _, ev := range events {
		for _, o := range outcomes {
			if ev.GetOutcome() == o {
				kept = append(kept, ev)
				break
			}
		}
	}
	return kept, len(events) - len(kept)
}

// defaultTriggerEventLimit is how many firings the server returns when asked for
// none (internal/handlers/trigger: defaultTriggerEventLimit).
const defaultTriggerEventLimit = 50

// triggerLookupError words a failed id-or-name lookup the way resolveTrigger
// always has, from the SDK's error.
func triggerLookupError(idOrName string, err error) error {
	var re *reliant.Error
	if errors.As(err, &re) {
		switch {
		case errors.Is(err, reliant.ErrAmbiguous):
			return fmt.Errorf("%s", ambiguousTriggerMessage(re.Message))
		case errors.Is(err, reliant.ErrNotFound) && re.Code == connect.CodeUnknown:
			return fmt.Errorf("no trigger with id or name %q", idOrName)
		}
	}
	return fmt.Errorf("looking up trigger %q: %w", idOrName, sdkCause(err))
}

// ambiguousTriggerMessage lays the SDK's one-line ambiguity error out the way
// the CLI prints it: the ids one per line.
func ambiguousTriggerMessage(msg string) string {
	const marker = "use an id: "
	head, ids, ok := strings.Cut(msg, marker)
	if !ok {
		return msg
	}
	return head + "use an id:\n  " + strings.Join(strings.Split(ids, ", "), "\n  ")
}

// resolveTrigger takes an id or a name. It tries the id first, because an id
// is unambiguous and costs one round trip; a name needs a list and a match.
func resolveTrigger(
	ctx context.Context,
	client reliantv1connect.TriggerServiceClient,
	idOrName string,
) (*reliantv1.Trigger, error) {
	resp, err := client.GetTrigger(ctx, connect.NewRequest(&reliantv1.GetTriggerRequest{Id: idOrName}))
	if err == nil {
		return resp.Msg.GetTrigger(), nil
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		return nil, fmt.Errorf("looking up trigger %q: %w", idOrName, err)
	}

	list, err := client.ListTriggers(ctx, connect.NewRequest(&reliantv1.ListTriggersRequest{}))
	if err != nil {
		return nil, fmt.Errorf("looking up trigger %q by name: %w", idOrName, err)
	}
	var matches []*reliantv1.Trigger
	for _, t := range list.Msg.GetTriggers() {
		if t.GetName() == idOrName {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no trigger with id or name %q", idOrName)
	case 1:
		return matches[0], nil
	default:
		// Names are unique per PROJECT, so the same name can exist in two
		// projects. Report that rather than picking one.
		var ids []string
		for _, m := range matches {
			ids = append(ids, fmt.Sprintf("%s (project %s)", m.GetId(), m.GetProjectId()))
		}
		return nil, fmt.Errorf("%q matches %d triggers across projects; use an id:\n  %s",
			idOrName, len(matches), strings.Join(ids, "\n  "))
	}
}

func parseTriggerPresets(entries []string) (map[string]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		group, name, found := strings.Cut(entry, "=")
		if !found {
			// A bare name is the top-level preset, whose group key is "".
			group, name = "", entry
		}
		if name == "" {
			return nil, fmt.Errorf("--preset %q has no preset name", entry)
		}
		out[group] = name
	}
	return out, nil
}

func parseTriggerParams(entries []string) (map[string]*structpb.Value, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(map[string]*structpb.Value, len(entries))
	for _, entry := range entries {
		key, raw, found := strings.Cut(entry, "=")
		if !found || key == "" {
			return nil, fmt.Errorf("--param %q must be key=value", entry)
		}
		// JSON first so numbers, booleans and objects keep their types, then
		// fall back to the literal string — which is what `--param msg=hello`
		// obviously means.
		var parsed any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			parsed = raw
		}
		value, err := structpb.NewValue(parsed)
		if err != nil {
			return nil, fmt.Errorf("--param %q is not representable: %w", entry, err)
		}
		out[key] = value
	}
	return out, nil
}

func parseTriggerOverlap(overlap string) (reliantv1.TriggerOverlapPolicy, error) {
	switch strings.ToLower(strings.TrimSpace(overlap)) {
	case "":
		return reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_UNSPECIFIED, nil
	case "skip":
		return reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_SKIP, nil
	case "allow":
		return reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_ALLOW, nil
	default:
		return 0, fmt.Errorf("--overlap must be skip or allow, got %q", overlap)
	}
}

func printTriggerTable(out io.Writer, list []*reliantv1.Trigger) {
	if len(list) == 0 {
		fmt.Fprintln(out, "No triggers.")
		return
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATE\tHEALTH\tSCHEDULE\tDAEMON\tWORKFLOW\tNEXT FIRE\tLAST")
	for _, t := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			t.GetName(),
			triggerStateLabel(t),
			triggerHealthLabel(t),
			scheduleSummary(t.GetSchedule()),
			orDash(t.GetDaemonId()),
			orDash(t.GetWorkflow()),
			orDash(t.GetNextFireAt()),
			lastEventSummary(t.GetLastEvent()),
		)
	}
	_ = w.Flush()
}

func printTriggerDetail(out io.Writer, t *reliantv1.Trigger) {
	printTriggerDetailWith(out, t, "")
}

// printTriggerDetailWith is printTriggerDetail with the connection described
// as connection says (describeTriggerConnection); "" describes it from the
// trigger alone.
func printTriggerDetailWith(out io.Writer, t *reliantv1.Trigger, connection string) {
	if t == nil {
		return
	}
	if connection == "" {
		connection = triggerConnectionLabel(t, nil)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Name:\t%s\n", t.GetName())
	fmt.Fprintf(w, "ID:\t%s\n", t.GetId())
	fmt.Fprintf(w, "State:\t%s\n", triggerStateLabel(t))
	if reason := pausedReasonLabel(t); reason != "" {
		fmt.Fprintf(w, "Paused by Reliant:\t%s\n", reason)
	}
	fmt.Fprintf(w, "Health:\t%s\n", triggerHealthLabel(t))
	if detail := t.GetHealth().GetLastFailureDetail(); detail != "" {
		fmt.Fprintf(w, "Health detail:\t%s\n", detail)
	}
	fmt.Fprintf(w, "Project:\t%s\n", t.GetProjectId())
	if wt := t.GetWorktreeId(); wt != "" {
		fmt.Fprintf(w, "Worktree:\t%s\n", wt)
	}
	fmt.Fprintf(w, "Daemon:\t%s\n", orDash(t.GetDaemonId()))
	fmt.Fprintf(w, "Workflow:\t%s\n", orDash(t.GetWorkflow()))
	fmt.Fprintf(w, "Acting as:\t%s\n", actingIdentityLabel(t.GetActingIdentity()))
	if connection != "" {
		fmt.Fprintf(w, "Connection:\t%s\n", connection)
	}
	if org := t.GetOrgId(); org != "" {
		fmt.Fprintf(w, "Organization:\t%s\n", org)
		fmt.Fprintf(w, "Unattended funding:\t%s\n", unattendedFundingLabel(t.GetUnattendedFunding()))
	}
	if src := t.GetDefinitionSource(); src != nil {
		fmt.Fprintf(w, "Definition:\t%s\n", src.GetSummary())
		for _, warning := range src.GetWarnings() {
			fmt.Fprintf(w, "Warning:\t%s\n", warning)
		}
	}
	if sched := t.GetSchedule(); sched != nil {
		fmt.Fprintf(w, "Schedule:\t%s\n", scheduleSummary(sched))
		fmt.Fprintf(w, "Timezone:\t%s\n", orDash(sched.GetTimezone()))
		fmt.Fprintf(w, "Overlap:\t%s\n", overlapLabel(sched.GetOverlap()))
		fmt.Fprintf(w, "Catchup window:\t%s\n", orDash(sched.GetCatchupWindow()))
	}
	if u := t.GetWebhookUrl(); u != "" && t.GetWebhook() != nil {
		// The token is never readable again; rotate issues a new one.
		fmt.Fprintf(w, "Webhook URL:\t%s (token: shown once at enable; `reliant trigger rotate` issues a new one)\n", u)
	} else if u != "" {
		// An integration whose provider the user points at Reliant per
		// resource (Twilio's number webhook): no token of the trigger's.
		fmt.Fprintf(w, "Webhook URL:\t%s\n", u)
	} else if t.GetWebhookManaged() {
		fmt.Fprintf(w, "Webhook URL:\tnone to configure: Reliant delivers to this webhook itself\n")
	}
	fmt.Fprintf(w, "Next fire:\t%s\n", orDash(t.GetNextFireAt()))
	for group, name := range t.GetPresets() {
		label := group
		if label == "" {
			label = "(top level)"
		}
		fmt.Fprintf(w, "Preset %s:\t%s\n", label, name)
	}
	for key, value := range t.GetParams() {
		fmt.Fprintf(w, "Param %s:\t%v\n", key, value.AsInterface())
	}
	fmt.Fprintf(w, "Created:\t%s\n", t.GetCreatedAt())
	fmt.Fprintf(w, "Updated:\t%s\n", t.GetUpdatedAt())
	_ = w.Flush()

	fmt.Fprintf(out, "\nMessage:\n  %s\n", t.GetMessage())

	if ev := t.GetLastEvent(); ev != nil {
		fmt.Fprintf(out, "\nLast firing: %s\n", lastEventSummary(ev))
		if detail := ev.GetOutcomeDetail(); detail != "" {
			fmt.Fprintf(out, "  %s\n", detail)
		}
		if chatID := ev.GetChatId(); chatID != "" {
			fmt.Fprintf(out, "  Chat: %s\n", chatID)
		}
		if def := ev.GetDefinition(); def != nil {
			fmt.Fprintf(out, "  Definition: %s\n", def.GetSummary())
		}
	}
}

func printTriggerEventTable(out io.Writer, events []*reliantv1.TriggerEvent) {
	if len(events) == 0 {
		fmt.Fprintln(out, "No firings yet.")
		return
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "OCCURRED AT\tOUTCOME\tCHAT\tDEFINITION\tDETAIL")
	for _, ev := range events {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			ev.GetOccurredAt(),
			outcomeLabel(ev.GetOutcome()),
			orDash(ev.GetChatId()),
			orDash(ev.GetDefinition().GetSummary()),
			orDash(ev.GetOutcomeDetail()),
		)
	}
	_ = w.Flush()
}

func triggerStateLabel(t *reliantv1.Trigger) string {
	if t.GetEnabled() {
		return "enabled"
	}
	return "paused"
}

// actingIdentityLabel says whose name a trigger's GitHub writes carry, in the
// words --acting-as takes; "-" when it is unset.
func actingIdentityLabel(a reliantv1.ActingIdentity) string {
	switch a {
	case reliantv1.ActingIdentity_ACTING_IDENTITY_APP:
		return "app (the Reliant GitHub App's bot)"
	case reliantv1.ActingIdentity_ACTING_IDENTITY_OWNER:
		return "owner (the activation owner's own GitHub account)"
	default:
		return "-"
	}
}

// triggerHealthLabel is TriggerHealth.status in the words the app uses
// (healthy, broken, needs setup, …); "-" when the server sent none.
func triggerHealthLabel(t *reliantv1.Trigger) string {
	status := t.GetHealth().GetStatus()
	if status == reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_UNSPECIFIED {
		return "-"
	}
	label := strings.TrimPrefix(status.String(), "TRIGGER_HEALTH_STATUS_")
	return strings.ToLower(strings.ReplaceAll(label, "_", " "))
}

// scheduleSummary renders cron and interval together, because they are a union
// — a trigger with both fires at every time either matches, and showing only
// one would misstate what it does.
func scheduleSummary(sched *reliantv1.ScheduleSource) string {
	if sched == nil {
		return "-"
	}
	var parts []string
	parts = append(parts, sched.GetCron()...)
	if interval := sched.GetInterval(); interval != "" {
		parts = append(parts, "every "+interval)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func overlapLabel(p reliantv1.TriggerOverlapPolicy) string {
	if p == reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_ALLOW {
		return "allow"
	}
	return "skip"
}

func outcomeLabel(o reliantv1.TriggerEventOutcome) string {
	switch o {
	case reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_LAUNCHED:
		return "launched"
	case reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_SKIPPED:
		return "skipped"
	case reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_FAILED:
		return "failed"
	case reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_PENDING:
		// Received and past its filter, not launched yet: typically queued
		// behind the workflow's concurrency cap. It settles on its own.
		return "pending"
	default:
		return "unknown"
	}
}

func lastEventSummary(ev *reliantv1.TriggerEvent) string {
	if ev == nil {
		return "never"
	}
	return fmt.Sprintf("%s at %s", outcomeLabel(ev.GetOutcome()), ev.GetOccurredAt())
}

// triggerDaemonLister is the one daemon-registry call --daemon needs.
type triggerDaemonLister interface {
	ListDaemons(context.Context, *connect.Request[reliantv1.ListDaemonsRequest]) (*connect.Response[reliantv1.ListDaemonsResponse], error)
}

func newDaemonRegistryClient(conn *connection) reliantv1connect.DaemonRegistryServiceClient {
	return reliantv1connect.NewDaemonRegistryServiceClient(conn.httpClient(), conn.ServerURL)
}

// resolveTriggerDaemon turns --daemon into a daemon id. An exact id wins;
// otherwise a hostname that matches exactly one daemon resolves to it, and a
// hostname shared by several is reported with its candidates rather than
// guessed at.
func resolveTriggerDaemon(ctx context.Context, client triggerDaemonLister, idOrHostname string) (string, error) {
	resp, err := client.ListDaemons(ctx, connect.NewRequest(&reliantv1.ListDaemonsRequest{}))
	if err != nil {
		return "", fmt.Errorf("listing daemons to resolve --daemon %q: %w", idOrHostname, err)
	}
	daemons := resp.Msg.GetDaemons()
	for _, d := range daemons {
		if d.GetDaemonId() == idOrHostname {
			return d.GetDaemonId(), nil
		}
	}
	var matches []*reliantv1.DaemonInfo
	for _, d := range daemons {
		if d.GetHostname() == idOrHostname {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 0:
		// Name the machines there are: no other command lists daemon ids
		// with a plain login (TL-59).
		var known []string
		for _, d := range daemons {
			known = append(known, fmt.Sprintf("%s  %s (%s)", d.GetDaemonId(), d.GetHostname(), d.GetDaemonType()))
		}
		if len(known) == 0 {
			return "", fmt.Errorf("no daemon with id or hostname %q: you have no machines", idOrHostname)
		}
		return "", fmt.Errorf("no daemon with id or hostname %q; your machines:\n  %s", idOrHostname, strings.Join(known, "\n  "))
	case 1:
		return matches[0].GetDaemonId(), nil
	default:
		var lines []string
		for _, m := range matches {
			lines = append(lines, fmt.Sprintf("%s (%s)", m.GetDaemonId(), m.GetDaemonType()))
		}
		return "", fmt.Errorf("hostname %q matches %d daemons; use an id:\n  %s",
			idOrHostname, len(matches), strings.Join(lines, "\n  "))
	}
}
