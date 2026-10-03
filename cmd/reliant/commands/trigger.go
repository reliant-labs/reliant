// Copyright (c) 2025 Reliant Labs
package commands

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
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

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
		Short: "Manage schedule triggers",
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
	)
	return cmd
}

// triggerDefinitionFlags are the fields shared by create and update.
type triggerDefinitionFlags struct {
	name          string
	projectPath   string
	projectID     string
	worktreeID    string
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
		Use:   "create",
		Short: "Create a schedule trigger",
		Example: `  # Every weekday at 9am, in the project in the working directory
  reliant trigger create --name "morning audit" --cron "0 9 * * 1-5" \
    --message "Review yesterday's CI failures and open issues for anything actionable."

  # Every 30 minutes, with a workflow and an input
  reliant trigger create --name sweep --interval 30m --workflow builtin://agent \
    --param depth=2 --message "Sweep the dependency tree."`,
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
			return nil
		},
	}
	flags.bind(cmd)
	cmd.Flags().BoolVar(&flags.disabled, "disabled", false, "Create the trigger paused")
	return cmd
}

func newTriggerUpdateCmd() *cobra.Command {
	var flags triggerDefinitionFlags
	cmd := &cobra.Command{
		Use:   "update <id-or-name>",
		Short: "Replace a trigger's definition",
		Long: `Replace a trigger's definition.

This is a full replacement, not a patch: every field is written as given, so
flags you omit are written as their zero value. The exception is enabled,
which is left as it is unless you pass --disabled or --disabled=false.`,
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
			// Default the project to the one the trigger already runs in, so
			// updating a schedule from another directory does not relocate it.
			if flags.projectID == "" && flags.projectPath == "" {
				flags.projectID = existing.GetProjectId()
			}
			def, err := flags.definition(cmd.Context(), cmd, conn, true)
			if err != nil {
				return err
			}
			resp, err := client.UpdateTrigger(cmd.Context(),
				connect.NewRequest(&reliantv1.UpdateTriggerRequest{Id: existing.GetId(), Trigger: def}))
			if err != nil {
				return conn.annotate(fmt.Errorf("updating trigger: %w", err))
			}
			printTriggerDetail(cmd.OutOrStdout(), resp.Msg.GetTrigger())
			return nil
		},
	}
	flags.bind(cmd)
	cmd.Flags().BoolVar(&flags.disabled, "disabled", false, "Pause the trigger (omit to leave its current state alone)")
	return cmd
}

func newTriggerListCmd() *cobra.Command {
	var projectID string
	var allProjects bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your triggers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			req := &reliantv1.ListTriggersRequest{}
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
				if projectID != "" {
					req.ProjectId = &projectID
				}
			}
			resp, err := newTriggerServiceClient(conn).ListTriggers(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				return conn.annotate(fmt.Errorf("listing triggers: %w", err))
			}
			printTriggerTable(cmd.OutOrStdout(), resp.Msg.GetTriggers())
			return nil
		},
	}
	cmd.Flags().StringVar(&projectID, "project-id", "", "List one project's triggers")
	cmd.Flags().BoolVar(&allProjects, "all", false, "List triggers across every project")
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
			trigger, err := resolveTrigger(cmd.Context(), newTriggerServiceClient(conn), args[0])
			if err != nil {
				return conn.annotate(err)
			}
			printTriggerDetail(cmd.OutOrStdout(), trigger)
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
	return &cobra.Command{
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
			return nil
		},
	}
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
			client := newTriggerServiceClient(conn)
			trigger, err := resolveTrigger(cmd.Context(), client, args[0])
			if err != nil {
				return conn.annotate(err)
			}
			resp, err := client.FireTrigger(cmd.Context(),
				connect.NewRequest(&reliantv1.FireTriggerRequest{Id: trigger.GetId()}))
			if err != nil {
				return conn.annotate(fmt.Errorf("firing trigger: %w", err))
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Fired trigger %q\n", trigger.GetName())
			fmt.Fprintf(out, "  Fire workflow ID: %s\n", resp.Msg.GetFireWorkflowId())
			fmt.Fprintf(out, "\nThe firing is asynchronous. See what it did with:\n")
			fmt.Fprintf(out, "  reliant trigger events %s\n", trigger.GetId())
			return nil
		},
	}
}

func newTriggerEventsCmd() *cobra.Command {
	var limit int32
	cmd := &cobra.Command{
		Use:   "events <id-or-name>",
		Short: "List a trigger's firings, newest first",
		Args:  cobra.ExactArgs(1),
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
			resp, err := client.ListTriggerEvents(cmd.Context(),
				connect.NewRequest(&reliantv1.ListTriggerEventsRequest{
					TriggerId: trigger.GetId(),
					Limit:     limit,
				}))
			if err != nil {
				return conn.annotate(fmt.Errorf("listing trigger events: %w", err))
			}
			printTriggerEventTable(cmd.OutOrStdout(), resp.Msg.GetEvents())
			return nil
		},
	}
	cmd.Flags().Int32Var(&limit, "limit", 0, "Maximum firings to return (0 applies the server default)")
	return cmd
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
	fmt.Fprintln(w, "NAME\tSTATE\tSCHEDULE\tWORKFLOW\tNEXT FIRE\tLAST")
	for _, t := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			t.GetName(),
			triggerStateLabel(t),
			scheduleSummary(t.GetSchedule()),
			orDash(t.GetWorkflow()),
			orDash(t.GetNextFireAt()),
			lastEventSummary(t.GetLastEvent()),
		)
	}
	_ = w.Flush()
}

func printTriggerDetail(out io.Writer, t *reliantv1.Trigger) {
	if t == nil {
		return
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Name:\t%s\n", t.GetName())
	fmt.Fprintf(w, "ID:\t%s\n", t.GetId())
	fmt.Fprintf(w, "State:\t%s\n", triggerStateLabel(t))
	fmt.Fprintf(w, "Project:\t%s\n", t.GetProjectId())
	if wt := t.GetWorktreeId(); wt != "" {
		fmt.Fprintf(w, "Worktree:\t%s\n", wt)
	}
	fmt.Fprintf(w, "Workflow:\t%s\n", orDash(t.GetWorkflow()))
	if sched := t.GetSchedule(); sched != nil {
		fmt.Fprintf(w, "Schedule:\t%s\n", scheduleSummary(sched))
		fmt.Fprintf(w, "Timezone:\t%s\n", orDash(sched.GetTimezone()))
		fmt.Fprintf(w, "Overlap:\t%s\n", overlapLabel(sched.GetOverlap()))
		fmt.Fprintf(w, "Catchup window:\t%s\n", orDash(sched.GetCatchupWindow()))
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
	}
}

func printTriggerEventTable(out io.Writer, events []*reliantv1.TriggerEvent) {
	if len(events) == 0 {
		fmt.Fprintln(out, "No firings yet.")
		return
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "OCCURRED AT\tOUTCOME\tCHAT\tDETAIL")
	for _, ev := range events {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			ev.GetOccurredAt(),
			outcomeLabel(ev.GetOutcome()),
			orDash(ev.GetChatId()),
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
