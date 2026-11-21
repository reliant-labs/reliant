// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/oauth2"
	"github.com/spf13/cobra"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	daemonv1 "github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1"
	"github.com/reliant-labs/reliant/gen/controlplane/services/daemon/v1/controlplanev1connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/machinepool"
)

// `reliant machine` manages the user's own machines exactly as Settings ->
// Machines does, through the control plane's DaemonService. The control
// plane accepts the CLI login for it only when the login carries
// daemon:manage — a person-only scope no daemon's credential can hold — so a
// login made before this command existed is told to log in again.
//
// An agent's shell is refused start/stop/resize (refuseMachineFromAgentShell):
// agents use the manage_machine tool, whose start and resize need the user's
// approval card in chat.
//
// label and pool are the machine's durable labels and the owner's machine
// pools, which the Reliant API owns (DaemonRegistryService), not the control
// plane: they need only the CLI login. An agent's shell is refused the writes
// (refuseLabelsFromAgentShell): a label decides which pool, and so which
// runs, a machine takes, which is the owner's call.

var errAgentCannotManageMachines = errors.New("an agent cannot start, stop or resize machines from its shell: use the manage_machine tool (start and resize need the user's approval card in chat), or ask the user to do it in Settings -> Machines")

func refuseMachineFromAgentShell(*cobra.Command, []string) error {
	if inAgentShell() {
		return errAgentCannotManageMachines
	}
	return nil
}

var errAgentCannotChangeLabels = errors.New("an agent cannot change machine labels or pools from its shell: use the manage_machine tool (the change needs the user's approval card in chat), or ask the user to do it in Settings -> Machines")

func refuseLabelsFromAgentShell(*cobra.Command, []string) error {
	if inAgentShell() {
		return errAgentCannotChangeLabels
	}
	return nil
}

func inAgentShell() bool {
	return os.Getenv(daemonpolicy.AgentShellEnv) != "" //nolint:forbidigo // the marker the daemon sets on agent shells (daemonpolicy.AgentShellEnv), a process signal, not configuration
}

// machineSizes maps the size names Settings -> Machines shows to the control
// plane's enum, smallest first.
var machineSizes = []struct {
	name string
	size controlplanev1.DaemonSize
}{
	{"s1", controlplanev1.DaemonSize_DAEMON_SIZE_S1},
	{"s2", controlplanev1.DaemonSize_DAEMON_SIZE_S2},
	{"m", controlplanev1.DaemonSize_DAEMON_SIZE_M},
	{"l", controlplanev1.DaemonSize_DAEMON_SIZE_L},
	{"xl", controlplanev1.DaemonSize_DAEMON_SIZE_XL},
	{"2xl", controlplanev1.DaemonSize_DAEMON_SIZE_2XL},
	{"3xl", controlplanev1.DaemonSize_DAEMON_SIZE_3XL},
}

func parseMachineSize(s string) (controlplanev1.DaemonSize, error) {
	names := make([]string, 0, len(machineSizes))
	for _, m := range machineSizes {
		if strings.EqualFold(strings.TrimSpace(s), m.name) {
			return m.size, nil
		}
		names = append(names, m.name)
	}
	return 0, fmt.Errorf("unknown size %q: want one of %s", s, strings.Join(names, ", "))
}

func machineSizeName(s controlplanev1.DaemonSize) string {
	for _, m := range machineSizes {
		if m.size == s {
			return m.name
		}
	}
	return ""
}

func machineStatusName(s controlplanev1.DaemonStatus) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "DAEMON_STATUS_"))
}

func machineTypeName(t controlplanev1.DaemonType) string {
	switch t {
	case controlplanev1.DaemonType_DAEMON_TYPE_MANAGED:
		return "managed"
	case controlplanev1.DaemonType_DAEMON_TYPE_EXTERNAL:
		return "connected"
	}
	return ""
}

// machineClient is the control plane's DaemonService, reached as the CLI
// login. The control plane's origin is the authorization server the API
// server names (RFC 8414), the same one `reliant auth login` signed in at.
func machineClient(ctx context.Context, cmd *cobra.Command) (controlplanev1connect.DaemonServiceClient, error) {
	conn, err := resolveConnection(cmd)
	if err != nil {
		return nil, err
	}
	meta, err := oauth2.DiscoverAuthorizationServer(ctx, conn.httpClientWithBearer(""), conn.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("finding the control plane for %s: %w", conn.ServerURL, err)
	}
	issuer := strings.TrimRight(strings.TrimSpace(meta.Issuer), "/")
	if issuer == "" {
		return nil, fmt.Errorf("%s names no control plane; machines are managed only on hosted Reliant", conn.ServerURL)
	}
	return controlplanev1connect.NewDaemonServiceClient(conn.httpClient(), issuer), nil
}

// explainMachineErr turns the control plane's refusal of a login without
// daemon:manage into the fix.
func explainMachineErr(err error) error {
	if connect.CodeOf(err) == connect.CodeUnauthenticated {
		return fmt.Errorf("%w\n  hint: managing machines needs a login made by this release: run 'reliant auth login'", err)
	}
	return err
}

// resolveMachine finds one of the user's machines by id or name.
func resolveMachine(ctx context.Context, client controlplanev1connect.DaemonServiceClient, ref string) (*controlplanev1.Daemon, error) {
	resp, err := client.ListDaemons(ctx, connect.NewRequest(&daemonv1.ListDaemonsRequest{}))
	if err != nil {
		return nil, explainMachineErr(err)
	}
	var byName []*controlplanev1.Daemon
	for _, d := range resp.Msg.GetDaemons() {
		if d.GetId() == ref {
			return d, nil
		}
		if d.GetName() == ref {
			byName = append(byName, d)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return nil, fmt.Errorf("no machine %q: 'reliant machine list' shows yours", ref)
	}
	return nil, fmt.Errorf("%d machines are named %q: pass the id instead", len(byName), ref)
}

func newMachineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "machine",
		Aliases: []string{"machines"},
		Short:   "List, start, stop, resize, rename and label your machines, and manage machine pools",
		Long: `Manage your own machines, exactly as Settings -> Machines does.

A machine is billed while it runs. start wakes a suspended machine; stop
suspends a running one; resize changes a SUSPENDED machine's size (s1, s2,
m, l, xl, 2xl, 3xl), which takes effect when it next starts. To resize a
running machine: stop, resize, start. rename changes what a machine is
called, and nothing else.

label sets a machine's durable labels, and pool defines machine pools: a
pool is the set of your machines whose labels contain its selector, and a
trigger or workflow that targets the pool runs on one of them. A machine can
also declare its own labels when it starts ('reliant daemon start --label').

start, stop, resize and rename need a login made by this release ('reliant
auth login'): it carries the daemon:manage permission, which only a person's
login can hold. An agent's shell is refused start, stop, resize, label and
the pool changes.`,
	}
	cmd.AddCommand(newMachineListCmd(), newMachineStartCmd(), newMachineStopCmd(), newMachineResizeCmd(), newMachineRenameCmd(),
		newMachineLabelCmd(), newMachinePoolCmd())
	return cmd
}

func newMachineListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List your machines",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := machineClient(cmd.Context(), cmd)
			if err != nil {
				return err
			}
			resp, err := client.ListDaemons(cmd.Context(), connect.NewRequest(&daemonv1.ListDaemonsRequest{}))
			if err != nil {
				return explainMachineErr(err)
			}
			return printMachines(cmd.OutOrStdout(), resp.Msg.GetDaemons())
		},
	}
}

func printMachines(out io.Writer, daemons []*controlplanev1.Daemon) error {
	if len(daemons) == 0 {
		_, err := fmt.Fprintln(out, "You have no machines.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tID\tTYPE\tSTATUS\tSIZE")
	for _, d := range daemons {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.GetName(), d.GetId(), machineTypeName(d.GetDaemonType()),
			machineStatusName(d.GetStatus()), machineSizeName(d.GetSize()))
	}
	return w.Flush()
}

func newMachineStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "start <name|id>",
		Short:   "Start (wake) a suspended machine",
		Args:    cobra.ExactArgs(1),
		PreRunE: refuseMachineFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := machineClient(cmd.Context(), cmd)
			if err != nil {
				return err
			}
			d, err := resolveMachine(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			if _, err := client.ResumeDaemon(cmd.Context(), connect.NewRequest(&daemonv1.ResumeDaemonRequest{DaemonId: d.GetId()})); err != nil {
				if connect.CodeOf(err) == connect.CodeFailedPrecondition {
					return fmt.Errorf("machine %s is not suspended: %w", d.GetName(), err)
				}
				return explainMachineErr(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Starting %s (%s). It takes a minute or so to come up.\n", d.GetName(), d.GetId())
			return err
		},
	}
}

func newMachineStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "stop <name|id>",
		Short:   "Stop (suspend) a running machine",
		Args:    cobra.ExactArgs(1),
		PreRunE: refuseMachineFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := machineClient(cmd.Context(), cmd)
			if err != nil {
				return err
			}
			d, err := resolveMachine(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			if _, err := client.SuspendDaemon(cmd.Context(), connect.NewRequest(&daemonv1.SuspendDaemonRequest{DaemonId: d.GetId()})); err != nil {
				return explainMachineErr(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Stopping %s (%s).\n", d.GetName(), d.GetId())
			return err
		},
	}
}

func newMachineResizeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resize <name|id> <s1|s2|m|l|xl|2xl|3xl>",
		Short: "Change a suspended machine's size",
		Long: `Change a SUSPENDED machine's size; it takes effect when the machine next
starts. A running machine is refused: stop it first. Disks only grow, so a
downsize keeps the larger disk.`,
		Args:    cobra.ExactArgs(2),
		PreRunE: refuseMachineFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			size, err := parseMachineSize(args[1])
			if err != nil {
				return err
			}
			client, err := machineClient(cmd.Context(), cmd)
			if err != nil {
				return err
			}
			d, err := resolveMachine(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			if _, err := client.ResizeDaemon(cmd.Context(), connect.NewRequest(&daemonv1.ResizeDaemonRequest{DaemonId: d.GetId(), Size: size})); err != nil {
				if connect.CodeOf(err) == connect.CodeFailedPrecondition {
					return fmt.Errorf("%w\n  hint: stop it first: reliant machine stop %s", err, d.GetName())
				}
				return explainMachineErr(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s (%s) is now %s; it takes effect when it next starts.\n", d.GetName(), d.GetId(), args[1])
			return err
		},
	}
}

func newMachineRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <name|id> <new-name>",
		Short: "Rename a machine",
		Long: `Rename one of your machines, cloud or self-hosted, running or not. The name
is a label only: the machine's id and its workspace do not change, so chats,
automations and links that point at it keep working.

A name is 1-63 lowercase letters, digits or hyphens, starting and ending with
a letter or digit, and no two of your machines share one.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			newName := strings.TrimSpace(args[1])
			client, err := machineClient(cmd.Context(), cmd)
			if err != nil {
				return err
			}
			d, err := resolveMachine(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			if _, err := client.UpdateDaemon(cmd.Context(), connect.NewRequest(&daemonv1.UpdateDaemonRequest{DaemonId: d.GetId(), NewName: newName})); err != nil {
				return explainMachineErr(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Renamed %s to %s (%s).\n", d.GetName(), newName, d.GetId())
			return err
		},
	}
}

// registryClient is the Reliant API's DaemonRegistryService, reached as the
// CLI login: it owns machine labels and pools.
func registryClient(cmd *cobra.Command) (reliantv1connect.DaemonRegistryServiceClient, error) {
	conn, err := resolveConnection(cmd)
	if err != nil {
		return nil, err
	}
	return newDaemonRegistryClient(conn), nil
}

// explainRegistryErr adds the fix to a refusal of the CLI login.
func explainRegistryErr(err error) error {
	if connect.CodeOf(err) == connect.CodeUnauthenticated {
		return fmt.Errorf("%w\n  hint: log in first: reliant auth login", err)
	}
	return err
}

// machineDisplayName is what Settings -> Machines calls a machine: its name,
// else its hostname.
func machineDisplayName(d *reliantv1.DaemonInfo) string {
	if d.GetName() != "" {
		return d.GetName()
	}
	return d.GetHostname()
}

// resolveRegistryMachine finds one of the user's machines in the registry by
// id, else by name (a machine with no name by hostname).
func resolveRegistryMachine(ctx context.Context, client machineLister, ref string) (*reliantv1.DaemonInfo, error) {
	resp, err := client.ListDaemons(ctx, connect.NewRequest(&reliantv1.ListDaemonsRequest{}))
	if err != nil {
		return nil, explainRegistryErr(err)
	}
	var byName []*reliantv1.DaemonInfo
	for _, d := range resp.Msg.GetDaemons() {
		if d.GetDaemonId() == ref {
			return d, nil
		}
		if machineDisplayName(d) == ref {
			byName = append(byName, d)
		}
	}
	switch len(byName) {
	case 1:
		return byName[0], nil
	case 0:
		return nil, fmt.Errorf("no machine %q: 'reliant machine list' shows yours", ref)
	}
	return nil, fmt.Errorf("%d machines are named %q: pass the id instead", len(byName), ref)
}

// formatLabels renders labels as sorted key=value pairs.
func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+labels[k])
	}
	return strings.Join(pairs, ",")
}

// nextMachineLabels is what `machine label` writes: the current labels with
// set merged over them (set alone with replace), less remove.
func nextMachineLabels(current, set map[string]string, remove []string, replace bool) map[string]string {
	next := map[string]string{}
	if !replace {
		for k, v := range current {
			next[k] = v
		}
	}
	for k, v := range set {
		next[k] = v
	}
	for _, k := range remove {
		delete(next, k)
	}
	return next
}

func newMachineLabelCmd() *cobra.Command {
	var (
		replace bool
		remove  []string
	)
	cmd := &cobra.Command{
		Use:   "label <name|id> [key=value...]",
		Short: "Set a machine's durable labels (which decide its machine pools)",
		Long: `Set one of your machines' durable labels. A machine pool is the set of your
machines whose labels contain its selector, so labelling a machine is how it
joins or leaves a pool ('reliant machine pool ls' shows the pools).

By default the labels you name are merged into the machine's current labels;
--remove drops keys, and --replace makes the named labels the machine's only
ones. Keys and values are lowercase slugs (a-z, 0-9, '.', '_', '-'), at most
63 characters, and a machine carries at most 32 labels. Prints the labels the
machine has afterwards.

  reliant machine label build-box pool=pipeline gate=yes
  reliant machine label build-box --remove gate
  reliant machine label build-box --replace pool=gate

A machine can also declare labels itself: 'reliant daemon start --label
k=v' merges them in on every connect.`,
		Args:    cobra.MinimumNArgs(1),
		PreRunE: refuseLabelsFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			set, err := machinepool.ParseLabels(args[1:], "labels")
			if err != nil {
				return err
			}
			for _, k := range remove {
				if !machinepool.IsSlug(k) {
					return fmt.Errorf("--remove %q: not a label key", k)
				}
			}
			if len(set) == 0 && len(remove) == 0 && !replace {
				return errors.New("name labels to set (key=value), keys to --remove, or --replace to clear them")
			}
			client, err := registryClient(cmd)
			if err != nil {
				return err
			}
			d, err := resolveRegistryMachine(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			next := nextMachineLabels(d.GetLabels(), set, remove, replace)
			if err := machinepool.ValidateLabels(next, "labels"); err != nil {
				return err
			}
			resp, err := client.SetMachineLabels(cmd.Context(), connect.NewRequest(&reliantv1.SetMachineLabelsRequest{
				DaemonId: d.GetDaemonId(), Labels: next,
			}))
			if err != nil {
				return explainRegistryErr(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s (%s) labels: %s\n", machineDisplayName(d), d.GetDaemonId(),
				formatLabels(resp.Msg.GetDaemon().GetLabels()))
			return err
		},
	}
	cmd.Flags().BoolVar(&replace, "replace", false, "Make the named labels the machine's only labels, instead of merging them in")
	cmd.Flags().StringSliceVar(&remove, "remove", nil, "Label keys to remove (repeatable, or comma-separated)")
	return cmd
}

func newMachinePoolCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "pool",
		Aliases: []string{"pools"},
		Short:   "List, define and delete machine pools",
		Long: `A machine pool is a named set of your machines: those whose durable labels
contain the pool's selector (by default pool=<name>). A trigger or workflow
that targets a pool runs on the connected member with the fewest live runs,
waking a suspended one when none is free. Members are never listed by hand:
label a machine ('reliant machine label', or 'reliant daemon start --label')
to make it one.`,
	}
	cmd.AddCommand(newMachinePoolListCmd(), newMachinePoolPutCmd(), newMachinePoolRemoveCmd())
	return cmd
}

func newMachinePoolListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List your machine pools and their members",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := registryClient(cmd)
			if err != nil {
				return err
			}
			resp, err := client.ListMachinePools(cmd.Context(), connect.NewRequest(&reliantv1.ListMachinePoolsRequest{}))
			if err != nil {
				return explainRegistryErr(err)
			}
			return printMachinePools(cmd.OutOrStdout(), resp.Msg.GetPools())
		},
	}
}

// uintOrDash renders a limit whose zero means "none".
func uintOrDash(n uint32) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprint(n)
}

func printMachinePools(out io.Writer, pools []*reliantv1.MachinePool) error {
	if len(pools) == 0 {
		_, err := fmt.Fprintln(out, "You have no machine pools: 'reliant machine pool put <name>' defines one.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "POOL\tSELECTOR\tRUNS/MEMBER\tMEMBERS\tMIN\tMAX\tSIZE\tIDLE")
	for _, p := range pools {
		s := p.GetSpec()
		size := s.GetMemberSize()
		if size == "" {
			size = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n", s.GetName(), formatLabels(s.GetSelector()),
			uintOrDash(s.GetMaxRunsPerMember()), len(p.GetMembers()), uintOrDash(s.GetMinMembers()),
			uintOrDash(s.GetMaxMembers()), size, s.GetMemberIdleTimeout())
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, p := range pools {
		fmt.Fprintf(out, "\n%s:\n", p.GetSpec().GetName())
		if len(p.GetMembers()) == 0 {
			fmt.Fprintf(out, "  no members: label a machine %s\n", formatLabels(p.GetSpec().GetSelector()))
		} else {
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "  MEMBER\tID\tSTATE\tLIVE RUNS\tSIZE")
			for _, m := range p.GetMembers() {
				fmt.Fprintf(w, "  %s\t%s\t%s\t%d\t%s\n", m.GetName(), m.GetDaemonId(), m.GetState(), m.GetLiveRuns(), m.GetSize())
			}
			if err := w.Flush(); err != nil {
				return err
			}
		}
		for _, warning := range p.GetWarnings() {
			fmt.Fprintf(out, "  warning: %s\n", warning)
		}
	}
	return nil
}

func newMachinePoolPutCmd() *cobra.Command {
	var (
		selector         = &labelsFlag{name: "selector"}
		maxRunsPerMember uint32
		minMembers       uint32
		maxMembers       uint32
		memberSize       string
		idleTimeout      string
	)
	cmd := &cobra.Command{
		Use:   "put <name>",
		Short: "Define a machine pool, or change one",
		Long: `Define the machine pool <name>, or change it. For a pool that exists, only
the flags you pass change; the rest keep their current values.

--selector is the labels a member's labels must contain (repeatable, or
comma-separated key=value); without one a new pool selects pool=<name>.
--max-runs-per-member bounds the live runs the pool places on one member (0:
no bound). --min-members is advisory (warned about when unmet);
--max-members and --member-size are how far, and at what size, a scale-up
may grow the pool. --member-idle-timeout is how long a member may sit idle
before it sleeps (1m-4h; default 10m).

  reliant machine pool put pipeline --max-runs-per-member 2
  reliant machine pool put gate --selector pool=pipeline,gate=yes`,
		Args:    cobra.ExactArgs(1),
		PreRunE: refuseLabelsFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if !machinepool.IsSlug(name) {
				return fmt.Errorf("pool name %q: a lowercase slug (a-z, 0-9, '.', '_', '-'), at most 63 characters", name)
			}
			sel, err := selector.labels()
			if err != nil {
				return err
			}
			client, err := registryClient(cmd)
			if err != nil {
				return err
			}
			// PutMachinePool replaces the whole definition, so start from the
			// stored one: a flag not passed keeps its value.
			spec := &reliantv1.MachinePoolSpec{Name: name}
			existing, err := client.ListMachinePools(cmd.Context(), connect.NewRequest(&reliantv1.ListMachinePoolsRequest{}))
			if err != nil {
				return explainRegistryErr(err)
			}
			for _, p := range existing.Msg.GetPools() {
				if p.GetSpec().GetName() == name {
					spec = p.GetSpec()
				}
			}
			flags := cmd.Flags()
			if flags.Changed("selector") {
				spec.Selector = sel
			}
			if flags.Changed("max-runs-per-member") {
				spec.MaxRunsPerMember = maxRunsPerMember
			}
			if flags.Changed("min-members") {
				spec.MinMembers = minMembers
			}
			if flags.Changed("max-members") {
				spec.MaxMembers = maxMembers
			}
			if flags.Changed("member-size") {
				spec.MemberSize = strings.TrimSpace(memberSize)
			}
			if flags.Changed("member-idle-timeout") {
				spec.MemberIdleTimeout = strings.TrimSpace(idleTimeout)
			}
			resp, err := client.PutMachinePool(cmd.Context(), connect.NewRequest(&reliantv1.PutMachinePoolRequest{Pool: spec}))
			if err != nil {
				return explainRegistryErr(err)
			}
			return printMachinePools(cmd.OutOrStdout(), []*reliantv1.MachinePool{resp.Msg.GetPool()})
		},
	}
	cmd.Flags().Var(selector, "selector", "Labels a member must carry, key=value (repeatable, or comma-separated; default pool=<name>)")
	cmd.Flags().Uint32Var(&maxRunsPerMember, "max-runs-per-member", 0, "Most live runs the pool places on one member (0: no bound)")
	cmd.Flags().Uint32Var(&minMembers, "min-members", 0, "Fewest members the pool should have (advisory: warned about when unmet)")
	cmd.Flags().Uint32Var(&maxMembers, "max-members", 0, "Most members a scale-up may grow the pool to (0: it never grows)")
	cmd.Flags().StringVar(&memberSize, "member-size", "", "Machine size a scale-up creates (s1, s2, m, l, xl, 2xl, 3xl)")
	cmd.Flags().StringVar(&idleTimeout, "member-idle-timeout", "", "How long a member may sit idle before it sleeps, 1m-4h (default 10m)")
	return cmd
}

func newMachinePoolRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"delete"},
		Short:   "Delete a machine pool (its machines and their labels are untouched)",
		Long: `Delete the machine pool <name>. Its machines and their labels are untouched.
Refused while a trigger still runs on the pool: point the trigger elsewhere
first.`,
		Args:    cobra.ExactArgs(1),
		PreRunE: refuseLabelsFromAgentShell,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := registryClient(cmd)
			if err != nil {
				return err
			}
			name := strings.TrimSpace(args[0])
			if _, err := client.DeleteMachinePool(cmd.Context(), connect.NewRequest(&reliantv1.DeleteMachinePoolRequest{Name: name})); err != nil {
				return explainRegistryErr(err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Deleted machine pool %s.\n", name)
			return err
		},
	}
}
