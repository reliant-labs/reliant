// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// `reliant project restore` brings back a project `reliant project delete`
// (or Settings → Projects → Remove) deleted, until its purge 7 days later:
// ProjectService/RestoreProject, after resolving the project among the ones
// the caller may restore (ListDeletedProjects).

// projectRestoreClient is the slice of the ProjectService client the command
// needs; reliantv1connect.ProjectServiceClient satisfies it.
type projectRestoreClient interface {
	ListDeletedProjects(context.Context, *connect.Request[reliantv1.ListDeletedProjectsRequest]) (*connect.Response[reliantv1.ListDeletedProjectsResponse], error)
	RestoreProject(context.Context, *connect.Request[reliantv1.RestoreProjectRequest]) (*connect.Response[reliantv1.RestoreProjectResponse], error)
}

func newProjectRestoreCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "restore [project]",
		Short: "Restore a deleted project, within 7 days of deleting it",
		Long: `Restores a project deleted with 'reliant project delete' (or Settings →
Projects → Remove), with its chats, runs, settings and automations. The
automations the delete paused are turned back on.

<project> is the deleted project's id, name or path. With no argument, lists
the projects you can restore and when each is deleted for good.

A project can be restored until its restore window ends, 7 days after it was
deleted; then it is deleted for good. One deleted with --now cannot be.

Targets --server (else RELIANT_SERVER_URL, else the default), authenticated by
RELIANT_TOKEN or the login 'reliant auth login' stored for that server.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			r := projectRestore{client: newProjectServiceClient(conn), out: cmd.OutOrStdout(), json: jsonOutput}
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			if err := r.run(cmd.Context(), ref); err != nil {
				return conn.annotate(err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print the result as JSON")
	return cmd
}

// projectRestore is one run of the command, its I/O injected for tests.
type projectRestore struct {
	client projectRestoreClient
	out    io.Writer
	json   bool
}

func (r projectRestore) run(ctx context.Context, ref string) error {
	resp, err := r.client.ListDeletedProjects(ctx, connect.NewRequest(&reliantv1.ListDeletedProjectsRequest{}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeUnimplemented {
			return fmt.Errorf("this server deletes projects at once, so there is nothing to restore: %w", err)
		}
		return fmt.Errorf("listing deleted projects: %w", err)
	}
	deleted := resp.Msg.GetProjects()
	if ref == "" {
		return r.list(deleted)
	}
	project, err := matchProjectRef(deleted, ref, "no deleted project of yours is named %q: 'reliant project restore' lists the ones you can restore")
	if err != nil {
		return err
	}
	restored, err := r.client.RestoreProject(ctx, connect.NewRequest(&reliantv1.RestoreProjectRequest{ProjectId: project.GetId()}))
	if err != nil {
		return fmt.Errorf("restoring project %s: %w", project.GetId(), err)
	}
	resumed := restored.Msg.GetTriggersResumed()
	if r.json {
		return json.NewEncoder(r.out).Encode(map[string]any{
			"id": project.GetId(), "name": project.GetName(), "restored": true, "automations_resumed": resumed,
		})
	}
	fmt.Fprintf(r.out, "Restored %s (%s).", project.GetName(), project.GetId())
	if resumed > 0 {
		fmt.Fprintf(r.out, " %d automation(s) the delete paused are on again.", resumed)
	}
	fmt.Fprintln(r.out)
	return nil
}

// list prints the projects the caller can restore.
func (r projectRestore) list(deleted []*reliantv1.Project) error {
	if r.json {
		out := make([]map[string]string, len(deleted))
		for i, p := range deleted {
			out[i] = map[string]string{"id": p.GetId(), "name": p.GetName(), "path": p.GetPath(), "delete_after": p.GetDeleteAfter()}
		}
		return json.NewEncoder(r.out).Encode(out)
	}
	if len(deleted) == 0 {
		fmt.Fprintln(r.out, "No deleted projects to restore.")
		return nil
	}
	fmt.Fprintln(r.out, "Deleted projects you can restore ('reliant project restore <id>'):")
	for _, p := range deleted {
		fmt.Fprintf(r.out, "  %s  %s  deleted for good %s\n", p.GetId(), p.GetName(), displayTime(p.GetDeleteAfter()))
	}
	return nil
}
