// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage Reliant projects",
		Long: `Create and list Reliant projects.

A project is the unit of ownership a workflow run executes against. Every
'reliant workflow run' needs a project — use 'reliant project create' to mint
one (or let 'reliant workflow run --project-path' resolve it for you).`,
	}

	cmd.AddCommand(newProjectCreateCmd())
	cmd.AddCommand(newProjectListCmd())
	cmd.AddCommand(newProjectMoveCmd())
	cmd.PersistentFlags().Bool("daemon-session", false, "Use this server's existing signed-in daemon account without widening its permissions")

	return cmd
}

func newProjectCreateCmd() *cobra.Command {
	var (
		name        string
		path        string
		description string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a project and print its ID",
		Long: `Creates a project via the Reliant cloud API and prints the new project ID.

If --name is omitted it defaults to the base name of --path. Creation is
idempotent by path: if a project already exists at --path, its existing ID is
printed instead of erroring, so this doubles as an "ensure project exists"
one-liner.

Targets --server (else RELIANT_SERVER_URL, else the default), authenticated by
RELIANT_TOKEN or the login 'reliant auth login' stored for that server.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			client := newProjectServiceClient(conn)

			absPath, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolving path: %w", err)
			}
			if name == "" {
				name = filepath.Base(absPath)
			}

			req := &reliantv1.CreateProjectRequest{Name: name, Path: absPath}
			if description != "" {
				req.Description = &description
			}

			resp, err := client.CreateProject(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				// Idempotent-by-path: surface the existing project's ID instead
				// of failing, so re-runs and scripts stay a clean one-liner.
				if connect.CodeOf(err) == connect.CodeAlreadyExists {
					id, ferr := findProjectIDByPath(cmd.Context(), client, absPath)
					if ferr == nil && id != "" {
						fmt.Fprintf(cmd.ErrOrStderr(), "Project already exists at %s\n", absPath)
						fmt.Fprintln(cmd.OutOrStdout(), id)
						return nil
					}
				}
				return conn.annotate(fmt.Errorf("creating project: %w", err))
			}

			fmt.Fprintln(cmd.OutOrStdout(), resp.Msg.GetProject().GetId())
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Project name (defaults to the base name of --path)")
	cmd.Flags().StringVar(&path, "path", ".", "Project directory path")
	cmd.Flags().StringVar(&description, "description", "", "Optional project description")

	return cmd
}

func newProjectListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your projects",
		Long: `Lists the projects owned by the authenticated user.

Targets --server (else RELIANT_SERVER_URL, else the default), authenticated by
RELIANT_TOKEN or the login 'reliant auth login' stored for that server.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}

			resp, err := newProjectServiceClient(conn).ListProjects(cmd.Context(), connect.NewRequest(&reliantv1.ListProjectsRequest{}))
			if err != nil {
				return conn.annotate(fmt.Errorf("listing projects: %w", err))
			}
			projects := resp.Msg.GetProjects()

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				out := make([]map[string]string, 0, len(projects))
				for _, p := range projects {
					out = append(out, map[string]string{
						"id":   p.GetId(),
						"name": p.GetName(),
						"path": p.GetPath(),
					})
				}
				return enc.Encode(out)
			}

			if len(projects) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No projects found")
				return nil
			}

			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tPATH")
			for _, p := range projects {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", p.GetId(), p.GetName(), p.GetPath())
			}
			return tw.Flush()
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

// projectDaemonLister is the slice of the ProjectService client that
// checkProjectOnDaemon needs; reliantv1connect.ProjectServiceClient satisfies it.
type projectDaemonLister interface {
	ListProjectDaemons(context.Context, *connect.Request[reliantv1.ListProjectDaemonsRequest]) (*connect.Response[reliantv1.ListProjectDaemonsResponse], error)
}

// checkProjectOnDaemon refuses `workflow run --project-id --daemon` unless the
// project is checked out on that machine. Only --project-path looks a folder
// up, or attaches it, on the named machine; a project id names a project
// wherever it already is, so a run sent to a machine that does not hold it
// could only fail once it touched the project.
func checkProjectOnDaemon(ctx context.Context, client projectDaemonLister, projectID, daemonID string) error {
	resp, err := client.ListProjectDaemons(ctx, connect.NewRequest(&reliantv1.ListProjectDaemonsRequest{}))
	if err != nil {
		return fmt.Errorf("checking that project %s is on machine %s: %w", projectID, daemonID, err)
	}
	for _, pd := range resp.Msg.GetProjectDaemons() {
		if pd.GetProjectId() != projectID || pd.GetDaemonId() != daemonID {
			continue
		}
		switch pd.GetInstallState() {
		case reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_INSTALLED,
			// Rows written before install_state existed read as installed.
			reliantv1.ProjectInstallState_PROJECT_INSTALL_STATE_UNSPECIFIED:
			return nil
		}
		return fmt.Errorf("project %s is not ready on machine %s (%s): wait for it to finish, or pass --project-path to attach the folder there",
			projectID, daemonID, pd.GetInstallState())
	}
	return fmt.Errorf("project %s is not on machine %s: pass --project-path with the folder on that machine (it is looked up or attached there), or drop --daemon to run on the project's own machine",
		projectID, daemonID)
}

// projectResolverClient is the narrow slice of the ProjectService client that
// project resolution needs. Declaring it locally keeps resolveProjectID unit
// testable with a small fake — reliantv1connect.ProjectServiceClient satisfies
// it directly.
type projectResolverClient interface {
	ListProjects(context.Context, *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error)
	CreateProject(context.Context, *connect.Request[reliantv1.CreateProjectRequest]) (*connect.Response[reliantv1.CreateProjectResponse], error)
}

// newProjectServiceClient builds a ProjectService Connect client for the
// resolved connection — same server and same bearer as every other cloud
// command, so 'project list' and 'workflow run' can never disagree about which
// server they are talking to.
func newProjectServiceClient(conn *connection) reliantv1connect.ProjectServiceClient {
	return reliantv1connect.NewProjectServiceClient(conn.httpClient(), conn.ServerURL)
}

// resolveProjectPathToID resolves a filesystem path to a project ID on the
// connection's server, creating the project if needed. This is the entry point
// 'workflow run --project-path' calls.
func resolveProjectPathToID(ctx context.Context, conn *connection, path string) (string, error) {
	return resolveProjectPathToIDOn(ctx, conn, path, "")
}

// resolveProjectPathToIDOn is resolveProjectPathToID for the folder on one
// machine (daemonID; "" is the user's default machine): the project is found,
// attached or created there (CreateProjectRequest.daemon_id).
func resolveProjectPathToIDOn(ctx context.Context, conn *connection, path, daemonID string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving project path: %w", err)
	}
	return resolveProjectIDOn(ctx, newProjectServiceClient(conn), absPath, daemonID)
}

// resolveProjectID maps an absolute path to a project ID: it returns the ID of
// an existing project at that path, or creates one and returns its ID.
//
// Create-then-find, not find-then-create. Asking the server first looks like a
// wasted round trip, but CreateProject is the only find-or-create authority
// that can see the daemon's filesystem — the CLI cannot stat a path that lives
// in a cloud workspace pod. Resolving by listing rows locally would hand back
// the id of a projects row whose directory has been deleted, and the run would
// bind to a phantom path, execute nothing, and report success. So every
// resolution goes through the server: AlreadyExists is the "found" answer, and
// by the time the server returns it, it has confirmed the directory is there.
// It also closes the find-then-create race that AlreadyExists existed to
// paper over.
func resolveProjectID(ctx context.Context, client projectResolverClient, absPath string) (string, error) {
	return resolveProjectIDOn(ctx, client, absPath, "")
}

// resolveProjectIDOn is resolveProjectID on the machine daemonID ("" is the
// user's default machine, as resolveProjectID).
func resolveProjectIDOn(ctx context.Context, client projectResolverClient, absPath, daemonID string) (string, error) {
	req := &reliantv1.CreateProjectRequest{Name: filepath.Base(absPath), Path: absPath}
	if daemonID != "" {
		req.DaemonId = &daemonID
	}
	resp, err := client.CreateProject(ctx, connect.NewRequest(req))
	if err == nil {
		return resp.Msg.GetProject().GetId(), nil
	}
	if connect.CodeOf(err) == connect.CodeAlreadyExists {
		if id, ferr := findProjectIDByPath(ctx, client, absPath); ferr == nil && id != "" {
			return id, nil
		}
		return "", fmt.Errorf("project already exists at %s but could not be resolved to an ID: %w", absPath, err)
	}
	return "", fmt.Errorf("resolving project for path %s: %w", absPath, err)
}

// findProjectIDByPath returns the ID of the caller's project whose path exactly
// matches absPath, or an empty string if none match.
func findProjectIDByPath(ctx context.Context, client projectResolverClient, absPath string) (string, error) {
	resp, err := client.ListProjects(ctx, connect.NewRequest(&reliantv1.ListProjectsRequest{}))
	if err != nil {
		return "", err
	}
	for _, p := range resp.Msg.GetProjects() {
		if p.GetPath() == absPath {
			return p.GetId(), nil
		}
	}
	return "", nil
}
