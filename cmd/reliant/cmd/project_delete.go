// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// `reliant project delete` is Settings → Projects → Remove from the terminal:
// ProjectService/DeleteProject, the rpc the app calls, after the CLI resolves
// the project and asks for its name (reliant#2754).
//
// The server schedules the delete: the project is hidden at once, its
// automations are paused, and it is purged 7 days later; until then
// `reliant project restore` brings it back (control-plane
// docs/design/unified-shell-and-projects.md §11.3). --now is the immediate,
// irreversible delete (PurgeProjectNow), and the only behaviour a server from
// before the restore window has: there, DeleteProject deletes in bounded
// batches (internal/store/project_delete.go), and one that stops part way
// leaves the project with fewer chats, which running it again resumes.
//
// Nothing on disk is touched unless --remove-from-machines asks for it.

// projectDeleteClient is the slice of the ProjectService client the command
// needs; reliantv1connect.ProjectServiceClient satisfies it.
type projectDeleteClient interface {
	ListProjects(context.Context, *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error)
	DeleteProject(context.Context, *connect.Request[reliantv1.DeleteProjectRequest]) (*connect.Response[reliantv1.DeleteProjectResponse], error)
	PurgeProjectNow(context.Context, *connect.Request[reliantv1.PurgeProjectNowRequest]) (*connect.Response[reliantv1.PurgeProjectNowResponse], error)
}

// projectLister is what resolving a project reference needs.
type projectLister interface {
	ListProjects(context.Context, *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error)
}

func newProjectDeleteCmd() *cobra.Command {
	var (
		yes          bool
		jsonOutput   bool
		now          bool
		fromMachines bool
	)
	cmd := &cobra.Command{
		Use:   "delete <project>",
		Short: "Delete a project and its chats (restorable for 7 days)",
		Long: `Deletes a project from Reliant, as Settings → Projects → Remove does.
<project> is the project's id, name or path ('reliant project list').

The project is hidden at once and its automations are paused. Its chats,
runs, worktree records, settings and automations are deleted for good 7
days later; until then 'reliant project restore <project>' brings it all
back. --now deletes it immediately instead, which cannot be undone.

Nothing on disk is deleted: the folder and its git history stay where they
are. --remove-from-machines also removes the project's checkout from each of
your machines that holds one, and reports each; a machine that is offline,
or has a workspace open in the project, keeps its copy. A checkout can hold
work you have not committed or pushed, so check before you ask for it.

A hosted deployment of the project (reliant forge env deploy) is not deleted
by this command; 'reliant forge env delete <env>' removes an environment.

The command asks you to type the project's name, unless --yes is given.

Targets --server (else RELIANT_SERVER_URL, else the default), authenticated by
RELIANT_TOKEN or the login 'reliant auth login' stored for that server.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			d := projectDelete{
				client:       newProjectServiceClient(conn),
				out:          cmd.OutOrStdout(),
				in:           cmd.InOrStdin(),
				yes:          yes || jsonOutput,
				json:         jsonOutput,
				now:          now,
				fromMachines: fromMachines,
			}
			if jsonOutput && !yes {
				return errors.New("--json needs --yes: there is no prompt to answer in JSON mode")
			}
			if err := d.run(cmd.Context(), args[0]); err != nil {
				return conn.annotate(err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Delete without asking to type the project's name")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print the deleted project as JSON (requires --yes)")
	cmd.Flags().BoolVar(&now, "now", false, "Delete immediately and for good, with no restore window")
	cmd.Flags().BoolVar(&fromMachines, "remove-from-machines", false, "Also remove the project's checkout from each of your machines")
	return cmd
}

// projectDelete is one run of the command, its I/O injected for tests.
type projectDelete struct {
	client       projectDeleteClient
	out          io.Writer
	in           io.Reader
	yes          bool
	json         bool
	now          bool
	fromMachines bool
}

func (d projectDelete) run(ctx context.Context, ref string) error {
	project, err := findProjectRef(ctx, d.client, ref)
	if err != nil {
		return err
	}
	if !d.yes {
		fmt.Fprintf(d.out, "Delete %s (%s) at %s\n\n", project.GetName(), project.GetId(), project.GetPath())
		fmt.Fprintln(d.out, "  Its chats, runs, settings and automations are deleted with it.")
		if d.now {
			fmt.Fprintln(d.out, "  It is deleted now, for good: this cannot be undone.")
		} else {
			fmt.Fprintln(d.out, "  It can be restored for 7 days: reliant project restore.")
		}
		if d.fromMachines {
			fmt.Fprintln(d.out, "  Its checkout is removed from each of your machines that holds one.")
		} else {
			fmt.Fprintln(d.out, "  Nothing on disk is deleted.")
		}
		if err := confirmTypedName(d.in, d.out, project.GetName(), "delete it", "deleted"); err != nil {
			return err
		}
	}
	var (
		deleteAfter string
		removals    []*reliantv1.ProjectMachineRemoval
	)
	if d.now {
		removals, err = d.purgeNow(ctx, project)
	} else {
		var resp *connect.Response[reliantv1.DeleteProjectResponse]
		resp, err = d.client.DeleteProject(ctx, connect.NewRequest(&reliantv1.DeleteProjectRequest{
			ProjectId: project.GetId(), RemoveFromMachines: d.fromMachines,
		}))
		if err == nil {
			deleteAfter, removals = resp.Msg.GetDeleteAfter(), resp.Msg.GetMachineRemovals()
		}
	}
	if err != nil {
		switch connect.CodeOf(err) {
		case connect.CodeDeadlineExceeded, connect.CodeUnavailable, connect.CodeCanceled:
			return fmt.Errorf("deleting project %s stopped part way (%w): what is left of it is still there; run the command again to resume", project.GetId(), err)
		}
		return fmt.Errorf("deleting project %s: %w", project.GetId(), err)
	}
	if d.json {
		out := map[string]any{"id": project.GetId(), "name": project.GetName(), "path": project.GetPath(), "deleted": true}
		if deleteAfter != "" {
			out["delete_after"] = deleteAfter
		}
		if len(removals) > 0 {
			out["machine_removals"] = machineRemovalsJSON(removals)
		}
		return json.NewEncoder(d.out).Encode(out)
	}
	if deleteAfter != "" {
		fmt.Fprintf(d.out, "Deleted %s (%s). It can be restored until %s: reliant project restore %s\n",
			project.GetName(), project.GetId(), displayTime(deleteAfter), project.GetId())
	} else {
		// --now, or a server from before the restore window, which deletes
		// at once.
		fmt.Fprintf(d.out, "Deleted %s (%s) for good.\n", project.GetName(), project.GetId())
	}
	if d.fromMachines {
		printMachineRemovals(d.out, removals)
	} else {
		fmt.Fprintln(d.out, "Nothing on disk was deleted.")
	}
	return nil
}

// purgeNow deletes the project immediately. A server from before the restore
// window has no PurgeProjectNow, and its DeleteProject is the immediate
// delete, so that is what it asks there.
func (d projectDelete) purgeNow(ctx context.Context, project *reliantv1.Project) ([]*reliantv1.ProjectMachineRemoval, error) {
	resp, err := d.client.PurgeProjectNow(ctx, connect.NewRequest(&reliantv1.PurgeProjectNowRequest{
		ProjectId: project.GetId(), ConfirmName: project.GetName(), RemoveFromMachines: d.fromMachines,
	}))
	if connect.CodeOf(err) == connect.CodeUnimplemented {
		if d.fromMachines {
			return nil, errors.New("this server cannot remove a project's code from machines; delete it without --remove-from-machines, or remove each checkout from Settings → Machines")
		}
		_, err = d.client.DeleteProject(ctx, connect.NewRequest(&reliantv1.DeleteProjectRequest{ProjectId: project.GetId()}))
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetMachineRemovals(), nil
}

// printMachineRemovals reports, per machine, whether the checkout was removed.
func printMachineRemovals(out io.Writer, removals []*reliantv1.ProjectMachineRemoval) {
	if len(removals) == 0 {
		fmt.Fprintln(out, "No machine held a checkout of it.")
		return
	}
	for _, r := range removals {
		name := r.GetMachineName()
		if name == "" {
			name = r.GetDaemonId()
		}
		switch r.GetStatus() {
		case reliantv1.ProjectMachineRemovalStatus_PROJECT_MACHINE_REMOVAL_STATUS_REMOVED:
			fmt.Fprintf(out, "  %s: removed %s\n", name, r.GetPath())
		case reliantv1.ProjectMachineRemovalStatus_PROJECT_MACHINE_REMOVAL_STATUS_OFFLINE:
			fmt.Fprintf(out, "  %s: offline, keeps its copy at %s\n", name, r.GetPath())
		default:
			fmt.Fprintf(out, "  %s: keeps its copy at %s (%s)\n", name, r.GetPath(), r.GetMessage())
		}
	}
}

func machineRemovalsJSON(removals []*reliantv1.ProjectMachineRemoval) []map[string]string {
	out := make([]map[string]string, len(removals))
	for i, r := range removals {
		status := strings.ToLower(strings.TrimPrefix(r.GetStatus().String(), "PROJECT_MACHINE_REMOVAL_STATUS_"))
		out[i] = map[string]string{"daemon_id": r.GetDaemonId(), "machine": r.GetMachineName(), "path": r.GetPath(), "status": status, "message": r.GetMessage()}
	}
	return out
}

// displayTime shows an RFC 3339 time in the reader's local time, or as given
// when it does not parse.
func displayTime(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return t.Local().Format("Mon 2 Jan 2006 15:04 MST")
}

// findProjectRef resolves ref to one of the caller's projects: an id first,
// then a name or a path. A path matches as typed and as an absolute path, so
// `reliant project delete .` names the project in the current directory.
func findProjectRef(ctx context.Context, client projectLister, ref string) (*reliantv1.Project, error) {
	resp, err := client.ListProjects(ctx, connect.NewRequest(&reliantv1.ListProjectsRequest{}))
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	return matchProjectRef(resp.Msg.GetProjects(), ref, "no project of yours is named %q: pass its id ('reliant project list')")
}

// matchProjectRef picks ref out of projects: an id first, then a name or a
// path (as typed, or made absolute). notFound formats the refusal when none
// matches, with ref.
func matchProjectRef(projects []*reliantv1.Project, ref, notFound string) (*reliantv1.Project, error) {
	for _, p := range projects {
		if p.GetId() == ref {
			return p, nil
		}
	}
	abs := ""
	if a, err := filepath.Abs(ref); err == nil {
		abs = a
	}
	var named []*reliantv1.Project
	for _, p := range projects {
		if p.GetName() == ref || p.GetPath() == ref || (abs != "" && p.GetPath() == abs) {
			named = append(named, p)
		}
	}
	switch len(named) {
	case 1:
		return named[0], nil
	case 0:
		return nil, fmt.Errorf(notFound, ref)
	default:
		return nil, fmt.Errorf("%d projects are named %q: pass its id", len(named), ref)
	}
}

// confirmTypedName asks for name, typed. action completes "Type <name> to …",
// and done completes "nothing …" when the answer is wrong.
func confirmTypedName(in io.Reader, out io.Writer, name, action, done string) error {
	fmt.Fprintf(out, "\nType %s to %s: ", name, action)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("not confirmed: type the project's name, or pass --yes")
	}
	if strings.TrimSpace(line) != name {
		return fmt.Errorf("not confirmed: that is not the project's name; nothing %s", done)
	}
	return nil
}
