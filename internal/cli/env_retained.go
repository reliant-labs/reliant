package cli

// `forge env retained list | purge` — the managed databases that outlived a
// deleted environment (`forge env delete --keep-data`, `forge app teardown
// --keep-data`). They keep running and billing as storage, and until these
// verbs nothing in the CLI showed them: ListRetainedDatabases and
// DeleteRetainedDatabase existed only as rpcs
// (docs/design/unified-shell-and-projects.md §11.1 and L5, in control-plane).
//
// A control plane may stamp each with purge_after (when it purges it by
// itself, L8); an older one does not, and its databases stay until purged
// here.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

const (
	procListRetainedDatabases  = "controlplane.v1.DeployService/ListRetainedDatabases"
	procDeleteRetainedDatabase = "controlplane.v1.DeployService/DeleteRetainedDatabase"
)

// retainedDatabaseWire is controlplane.v1.RetainedDatabase. Declared locally:
// forge does not vendor the control plane's protos.
type retainedDatabaseWire struct {
	ID              string    `json:"id"`
	EnvironmentID   string    `json:"environmentId,omitempty"`
	EnvironmentName string    `json:"environmentName,omitempty"`
	Project         string    `json:"project,omitempty"`
	DatabaseName    string    `json:"databaseName,omitempty"`
	ClusterID       string    `json:"clusterId,omitempty"`
	Namespace       string    `json:"namespace,omitempty"`
	StorageGiB      wireInt64 `json:"storageGib,omitempty"`
	RetainedAt      string    `json:"retainedAt,omitempty"`
	// PurgeAfter is when the control plane purges it by itself. Unset on a
	// control plane that never does (it stays until purged here).
	PurgeAfter string `json:"purgeAfter,omitempty"`
}

// retainedUnservedError explains a control plane with no retained-database
// rpcs.
func retainedUnservedError(endpoint, rpc string) error {
	return fmt.Errorf("%s does not serve DeployService/%s: it is an older control plane with no retained databases to list or purge\n"+
		"fix: a database kept by `forge env delete --keep-data` there is managed by the platform operator", endpoint, rpc)
}

func listRetainedDatabases(ctx context.Context, t appTarget) ([]retainedDatabaseWire, error) {
	var resp struct {
		RetainedDatabases []retainedDatabaseWire `json:"retainedDatabases"`
	}
	if err := t.Client.Call(ctx, procListRetainedDatabases, map[string]any{}, &resp); err != nil {
		if rpcUnserved(err) {
			return nil, retainedUnservedError(t.Endpoint, "ListRetainedDatabases")
		}
		return nil, fmt.Errorf("list retained databases: %w", err)
	}
	dbs := resp.RetainedDatabases
	slices.SortFunc(dbs, func(a, b retainedDatabaseWire) int {
		if c := strings.Compare(a.Project, b.Project); c != 0 {
			return c
		}
		if c := strings.Compare(a.EnvironmentName, b.EnvironmentName); c != 0 {
			return c
		}
		if c := strings.Compare(a.DatabaseName, b.DatabaseName); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return dbs, nil
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// runEnvRetainedList prints the retained databases of this app (all: of the
// organization).
func runEnvRetainedList(ctx context.Context, t appTarget, all bool, out io.Writer) error {
	if !all {
		if err := requireAppName(t); err != nil {
			return fmt.Errorf("%w\n  or list every retained database of your organization: `forge env retained list --all`", err)
		}
	}
	dbs, err := listRetainedDatabases(ctx, t)
	if err != nil {
		return err
	}
	if !all {
		dbs = slices.DeleteFunc(dbs, func(d retainedDatabaseWire) bool { return d.Project != t.Project })
	}
	if len(dbs) == 0 {
		if all {
			fmt.Fprintf(out, "no retained databases in your organization on %s\n", t.Endpoint)
		} else {
			fmt.Fprintf(out, "no retained databases for app %q on %s (`--all` lists your organization's)\n", t.Project, t.Endpoint)
		}
		return nil
	}
	// The PURGE AFTER column only when the control plane sends the date: an
	// older one never purges by itself, and an empty column would read as
	// "never" for a reason forge cannot know.
	withPurge := slices.ContainsFunc(dbs, func(d retainedDatabaseWire) bool { return d.PurgeAfter != "" })
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	head := "ID\tDATABASE\tENV\tPROJECT\tSTORAGE\tRETAINED"
	if withPurge {
		head += "\tPURGE AFTER"
	}
	fmt.Fprintln(tw, head)
	var total int64
	for _, d := range dbs {
		total += int64(d.StorageGiB)
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%d GiB\t%s", d.ID, dashIfEmpty(d.DatabaseName), dashIfEmpty(d.EnvironmentName),
			dashIfEmpty(d.Project), int64(d.StorageGiB), dashIfEmpty(wireTime(d.RetainedAt)))
		if withPurge {
			row += "\t" + dashIfEmpty(wireTime(d.PurgeAfter))
		}
		fmt.Fprintln(tw, row)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%d retained database(s), %d GiB: each keeps billing as storage until purged. A re-deploy does not reattach one.\n", len(dbs), total)
	if withPurge {
		fmt.Fprintln(out, "One with a PURGE AFTER date is purged by the control plane then.")
	}
	fmt.Fprintln(out, "Purge one now (IRREVERSIBLE): forge env retained purge <id>")
	return nil
}

// findRetainedDatabase resolves ref — a retained database's id, or its
// database name when exactly one in scope has it. The scope is this app's
// retained databases (project), or with all the organization's.
//
// An id is matched in the SAME scope as a name. The list rpc is org-wide, so
// an id-first match across it would let `purge <id>` run from app A destroy
// app B's database; one outside the app is refused and its owner named.
func findRetainedDatabase(dbs []retainedDatabaseWire, ref, project string, all bool) (retainedDatabaseWire, error) {
	inScope := func(d retainedDatabaseWire) bool { return all || d.Project == project }
	scope := fmt.Sprintf("app %q", project)
	if all {
		scope = "your organization"
	}
	for _, d := range dbs {
		if d.ID != ref {
			continue
		}
		if !inScope(d) {
			return retainedDatabaseWire{}, fmt.Errorf("retained database %s belongs to app %q, not this app (%q); nothing was purged\n"+
				"fix: purge it from that app's checkout, or name your organization's scope: `forge env retained purge %s --all`",
				ref, d.Project, project, ref)
		}
		return d, nil
	}
	var named []retainedDatabaseWire
	for _, d := range dbs {
		if d.DatabaseName == ref && inScope(d) {
			named = append(named, d)
		}
	}
	switch len(named) {
	case 1:
		return named[0], nil
	case 0:
		return retainedDatabaseWire{}, fmt.Errorf("no retained database of %s has id or name %q; nothing was purged\n"+
			"fix: `forge env retained list` lists them (`--all` for your organization's); purge one by its id", scope, ref)
	}
	ids := make([]string, 0, len(named))
	for _, d := range named {
		ids = append(ids, fmt.Sprintf("%s (app %s, env %s)", d.ID, dashIfEmpty(d.Project), dashIfEmpty(d.EnvironmentName)))
	}
	return retainedDatabaseWire{}, fmt.Errorf("%d retained databases of %s are named %q: %s; nothing was purged\n"+
		"fix: purge one by its id", len(named), scope, ref, strings.Join(ids, ", "))
}

func runEnvRetainedPurge(ctx context.Context, t appTarget, ref string, all, yes bool, in io.Reader, out io.Writer) error {
	if !all {
		if err := requireAppName(t); err != nil {
			return fmt.Errorf("%w\n  or purge from your organization's retained databases: `forge env retained purge %s --all`", err, ref)
		}
	}
	dbs, err := listRetainedDatabases(ctx, t)
	if err != nil {
		return err
	}
	d, err := findRetainedDatabase(dbs, ref, t.Project, all)
	if err != nil {
		return err
	}
	name := d.DatabaseName
	if name == "" {
		name = d.ID
	}
	fmt.Fprintf(out, "This will permanently delete retained database %q of app %q:\n", name, dashIfEmpty(d.Project))
	fmt.Fprintf(out, "  id:             %s\n", d.ID)
	fmt.Fprintf(out, "  project:        %s\n", dashIfEmpty(d.Project))
	env := dashIfEmpty(d.EnvironmentName)
	if d.EnvironmentID != "" {
		env += " (deleted; id " + d.EnvironmentID + ")"
	}
	fmt.Fprintf(out, "  environment:    %s\n", env)
	fmt.Fprintf(out, "  storage:        %d GiB\n", int64(d.StorageGiB))
	fmt.Fprintf(out, "  retained since: %s\n", dashIfEmpty(wireTime(d.RetainedAt)))
	if d.PurgeAfter != "" {
		fmt.Fprintf(out, "  purge after:    %s\n", wireTime(d.PurgeAfter))
	}
	if d.ClusterID != "" || d.Namespace != "" {
		fmt.Fprintf(out, "  location:       cluster %s, namespace %s\n", dashIfEmpty(d.ClusterID), dashIfEmpty(d.Namespace))
	}
	fmt.Fprintln(out, "\nIts data, volumes and credentials are destroyed and its storage stops billing. IRREVERSIBLE.")
	if !yes && !confirmTypedName(in, out, "database", name) {
		return fmt.Errorf("confirmation did not match %q; nothing was purged", name)
	}
	if err := t.Client.Call(ctx, procDeleteRetainedDatabase, map[string]any{"id": d.ID}, &struct{}{}); err != nil {
		if rpcUnserved(err) {
			return retainedUnservedError(t.Endpoint, "DeleteRetainedDatabase")
		}
		return fmt.Errorf("the control plane refused to purge retained database %s: %s\n"+
			"fix: purging a retained database needs an org owner or admin; nothing was purged", d.ID, appCallMessage(err))
	}
	fmt.Fprintf(out, "\nPurged retained database %s (%s); the platform is destroying its data and volumes.\n", name, d.ID)
	return nil
}

func newEnvRetainedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retained",
		Short: "List or purge the databases kept from deleted HOSTED environments (they bill storage)",
		Long: `A hosted env deleted with --keep-data (` + "`forge env delete`" + `, ` + "`forge app teardown`" + `)
leaves its managed databases behind as RETAINED databases: still running, still
billing as storage, and never reattached by a later deploy. These verbs show
and remove them, on the control plane this project is bound to
(.forge/hosted-projects.json) or the one --endpoint names.

  forge env retained list              this app's retained databases
  forge env retained list --all        your organization's
  forge env retained purge <id>        destroy one of this app's (IRREVERSIBLE; type its name to confirm)
  forge env retained purge <id> --all  destroy one of your organization's

A control plane that purges them by itself shows the date in a PURGE AFTER
column.`,
	}
	cmd.AddCommand(newEnvRetainedListCmd())
	cmd.AddCommand(newEnvRetainedPurgeCmd())
	return cmdutil.StrictGroup(cmd)
}

func newEnvRetainedListCmd() *cobra.Command {
	var all bool
	var token, endpoint string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the retained databases of this app (--all: of your organization)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := resolveAppTarget("forge env retained list", token, endpoint)
			if err != nil {
				return err
			}
			return runEnvRetainedList(cmd.Context(), t, all, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "List every retained database of your organization, not only this app's")
	addAppTargetFlags(cmd, &token, &endpoint)
	return cmd
}

func newEnvRetainedPurgeCmd() *cobra.Command {
	var yes, all bool
	var token, endpoint string
	cmd := &cobra.Command{
		Use:   "purge <id | database name>",
		Short: "Destroy a retained database: its data, volumes and credentials (IRREVERSIBLE)",
		Long: `Destroy one retained database: its data, volumes and credentials, and its
storage stops billing. IRREVERSIBLE.

  forge env retained purge <id>          prompts: type the database name to confirm
  forge env retained purge <id> --yes    no prompt (required with no TTY or CI=1)

The argument is the id ` + "`forge env retained list`" + ` prints, or the database's
name when exactly one of this app's retained databases has it. Either is
resolved among THIS app's retained databases: one of another app is refused,
and named. --all resolves among your organization's (` + "`forge env retained list --all`" + `),
and the confirmation names the app that owns it. Purging needs an org owner or
admin.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := strings.TrimSpace(args[0])
			if ref == "" {
				return errors.New("name the retained database to purge\nfix: `forge env retained purge <id>`; `forge env retained list` lists them")
			}
			if !yes && !lifecycleInteractive() {
				return fmt.Errorf("refusing to purge retained database %q without confirmation: no interactive terminal (or CI is set)\n"+
					"fix: re-run with `--yes` to confirm non-interactively", ref)
			}
			t, err := resolveAppTarget("forge env retained purge "+ref, token, endpoint)
			if err != nil {
				return err
			}
			return runEnvRetainedPurge(cmd.Context(), t, ref, all, yes, lifecycleStdin, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Resolve the id or name among your organization's retained databases, not only this app's")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm without the typed-name prompt (required when non-interactive)")
	addAppTargetFlags(cmd, &token, &endpoint)
	return cmd
}
