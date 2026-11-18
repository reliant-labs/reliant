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

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// `reliant project delete` is Settings → Projects → Remove from the terminal:
// ProjectService/DeleteProject, the rpc the app calls, after the CLI resolves
// the project and asks for its name (reliant#2754).
//
// The server deletes the project's chats, worktree records, settings and
// automations in bounded batches (internal/store/project_delete.go). Nothing
// on disk is touched. The delete is not atomic: one that stops part way leaves
// the project with fewer chats, and running it again resumes from what is
// left, so a timeout says exactly that.

// projectDeleteClient is the slice of the ProjectService client the command
// needs; reliantv1connect.ProjectServiceClient satisfies it.
type projectDeleteClient interface {
	ListProjects(context.Context, *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error)
	DeleteProject(context.Context, *connect.Request[reliantv1.DeleteProjectRequest]) (*connect.Response[reliantv1.DeleteProjectResponse], error)
}

// projectLister is what resolving a project reference needs.
type projectLister interface {
	ListProjects(context.Context, *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error)
}

func newProjectDeleteCmd() *cobra.Command {
	var (
		yes        bool
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "delete <project>",
		Short: "Delete a project and its chats",
		Long: `Deletes a project from Reliant, as Settings → Projects → Remove does.
<project> is the project's id, name or path ('reliant project list').

The project's chats, runs, worktree records, settings and automations are
deleted with it. Nothing on disk is deleted: the folder and its git history
stay where they are, and you can add the folder back at any time.

A hosted deployment of the project (reliant forge env deploy) is not deleted
by this command; 'reliant forge env delete <env>' removes an environment.

The command asks you to type the project's name, unless --yes is given. A
large project is deleted in batches; if the delete stops part way, what is
left is still there, and running the command again resumes it.

Targets --server (else RELIANT_SERVER_URL, else the default), authenticated by
RELIANT_TOKEN or the login 'reliant auth login' stored for that server.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			d := projectDelete{
				client: newProjectServiceClient(conn),
				out:    cmd.OutOrStdout(),
				in:     cmd.InOrStdin(),
				yes:    yes || jsonOutput,
				json:   jsonOutput,
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
	return cmd
}

// projectDelete is one run of the command, its I/O injected for tests.
type projectDelete struct {
	client projectDeleteClient
	out    io.Writer
	in     io.Reader
	yes    bool
	json   bool
}

func (d projectDelete) run(ctx context.Context, ref string) error {
	project, err := findProjectRef(ctx, d.client, ref)
	if err != nil {
		return err
	}
	if !d.yes {
		fmt.Fprintf(d.out, "Delete %s (%s) at %s\n\n", project.GetName(), project.GetId(), project.GetPath())
		fmt.Fprintln(d.out, "  Its chats, runs, settings and automations are deleted with it.")
		fmt.Fprintln(d.out, "  Nothing on disk is deleted.")
		if err := confirmTypedName(d.in, d.out, project.GetName(), "delete it", "deleted"); err != nil {
			return err
		}
	}
	if _, err := d.client.DeleteProject(ctx, connect.NewRequest(&reliantv1.DeleteProjectRequest{ProjectId: project.GetId()})); err != nil {
		switch connect.CodeOf(err) {
		case connect.CodeDeadlineExceeded, connect.CodeUnavailable, connect.CodeCanceled:
			return fmt.Errorf("deleting project %s stopped part way (%w): what is left of it is still there; run the command again to resume", project.GetId(), err)
		}
		return fmt.Errorf("deleting project %s: %w", project.GetId(), err)
	}
	if d.json {
		return json.NewEncoder(d.out).Encode(map[string]any{
			"id": project.GetId(), "name": project.GetName(), "path": project.GetPath(), "deleted": true,
		})
	}
	fmt.Fprintf(d.out, "Deleted %s (%s). Nothing on disk was deleted.\n", project.GetName(), project.GetId())
	return nil
}

// findProjectRef resolves ref to one of the caller's projects: an id first,
// then a name or a path. A path matches as typed and as an absolute path, so
// `reliant project delete .` names the project in the current directory.
func findProjectRef(ctx context.Context, client projectLister, ref string) (*reliantv1.Project, error) {
	resp, err := client.ListProjects(ctx, connect.NewRequest(&reliantv1.ListProjectsRequest{}))
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	projects := resp.Msg.GetProjects()
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
		return nil, fmt.Errorf("no project of yours is named %q: pass its id ('reliant project list')", ref)
	default:
		return nil, fmt.Errorf("%d projects are named %q: pass its id ('reliant project list')", len(named), ref)
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
