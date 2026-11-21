// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// `reliant worktree list` and `reliant worktree archive` are the app's
// workspace list and its Archive action, through the server's WorktreeService
// (ListWorktrees, ListArchivedWorktrees, GetWorktree, ArchiveWorktree). A
// workspace is one row of the project's worktrees: the checkout of every repo
// in the project on one branch. Archiving one is the same act as in the app:
// the row is archived at once, and its machine removes the directory later,
// only once it is clean, pushed or merged, and unused (anything else it keeps
// and lists in the Inbox).

// worktreeClient is what the workspace commands call.
type worktreeClient interface {
	ListWorktrees(context.Context, *connect.Request[reliantv1.ListWorktreesRequest]) (*connect.Response[reliantv1.ListWorktreesResponse], error)
	ListArchivedWorktrees(context.Context, *connect.Request[reliantv1.ListArchivedWorktreesRequest]) (*connect.Response[reliantv1.ListArchivedWorktreesResponse], error)
	GetWorktree(context.Context, *connect.Request[reliantv1.GetWorktreeRequest]) (*connect.Response[reliantv1.GetWorktreeResponse], error)
	ArchiveWorktree(context.Context, *connect.Request[reliantv1.ArchiveWorktreeRequest]) (*connect.Response[reliantv1.ArchiveWorktreeResponse], error)
}

// errAgentCannotArchiveWorkspaces is what `worktree archive` says in an
// agent's shell. An agent archives a workspace with its worktree tool, which
// refuses one a chat is working in; see daemonpolicy.AgentShellEnv for why
// this is a guardrail and not a boundary.
var errAgentCannotArchiveWorkspaces = errors.New("an agent cannot archive a workspace from its shell: use the worktree tool (action delete), which refuses one a chat is working in, or ask the user to run `reliant worktree archive` in their own terminal or archive it in the app")

func refuseArchiveFromAgentShell(*cobra.Command, []string) error {
	if inAgentShell() {
		return errAgentCannotArchiveWorkspaces
	}
	return nil
}

// worktreeMaxPages bounds how many pages a list reads: 100 pages of the
// server's default 100 is more workspaces than any project has.
const worktreeMaxPages = 100

func newWorktreeListCmd() *cobra.Command {
	var (
		project  string
		archived bool
		asJSON   bool
	)
	c := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List a project's workspaces (its worktrees in the app)",
		Long: `List the project's workspaces, as the app's workspace list does: the main
checkout first, then the rest, most recently active first. --archived lists
the archived ones instead, most recently archived first.

--project names the project by id, name or path ('reliant project list');
without it, the project whose folder holds the current directory.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			p, err := resolveWorkspaceProject(cmd.Context(), newProjectServiceClient(conn), project)
			if err != nil {
				return conn.annotate(err)
			}
			list, err := listWorkspaces(cmd.Context(), reliantv1connect.NewWorktreeServiceClient(conn.httpClient(), conn.ServerURL), p.GetId(), archived)
			if err != nil {
				return conn.annotate(err)
			}
			if asJSON {
				return writeWorktreesJSON(cmd.OutOrStdout(), list)
			}
			return printWorkspaces(cmd.OutOrStdout(), list, archived)
		},
	}
	c.Flags().StringVar(&project, "project", "", "the project, by id, name or path (default: the one holding the current directory)")
	c.Flags().BoolVar(&archived, "archived", false, "list the archived workspaces instead")
	c.Flags().BoolVar(&asJSON, "json", false, `print {"worktrees": [...]}, each in the API's JSON form`)
	return c
}

func newWorktreeArchiveCmd() *cobra.Command {
	var (
		project      string
		deleteBranch bool
		asJSON       bool
	)
	c := &cobra.Command{
		Use:     "archive <name|id>",
		PreRunE: refuseArchiveFromAgentShell,
		Short:   "Archive a workspace, as the app's Archive does",
		Long: `Archive one of the project's workspaces, named by its id or its name
('reliant worktree list'), as the app's Archive does. Its chats keep their
history. The machine that holds its checkout removes the directory later, only
once it is clean, its work is pushed or merged, and nothing uses it; otherwise
the directory is kept and listed in the Inbox. --delete-branch also deletes its
local branch. The main checkout cannot be archived.

A name is looked up in --project (by id, name or path), or in the project
whose folder holds the current directory.

An agent's shell is refused: an agent archives a workspace with its worktree
tool, which refuses one a chat is working in.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			client := reliantv1connect.NewWorktreeServiceClient(conn.httpClient(), conn.ServerURL)
			wt, err := resolveArchiveWorkspace(ctx, client, newProjectServiceClient(conn), strings.TrimSpace(args[0]), project)
			if err != nil {
				return conn.annotate(err)
			}
			switch {
			case wt.GetIsMain():
				return fmt.Errorf("%s is the project's main checkout, which cannot be archived", workspaceLabel(wt))
			case wt.GetDeletedAt() != "":
				return fmt.Errorf("%s is already archived", workspaceLabel(wt))
			}
			resp, err := client.ArchiveWorktree(ctx, connect.NewRequest(&reliantv1.ArchiveWorktreeRequest{WorktreeId: wt.GetId(), DeleteGitBranch: deleteBranch}))
			if err != nil {
				return conn.annotate(fmt.Errorf("archiving %s: %w", workspaceLabel(wt), err))
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"worktree_id": wt.GetId(), "name": wt.GetName(), "branch": wt.GetBranch(),
					"message": resp.Msg.GetMessage(), "deleted_branch": resp.Msg.GetDeletedBranch(),
				})
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Archived %s.\n", workspaceLabel(wt))
			if m := strings.TrimSpace(resp.Msg.GetMessage()); m != "" {
				fmt.Fprintln(out, m)
			}
			if resp.Msg.GetDeletedBranch() {
				fmt.Fprintf(out, "Deleted its branch %s.\n", wt.GetBranch())
			}
			return nil
		},
	}
	c.Flags().StringVar(&project, "project", "", "the project a name is looked up in, by id, name or path (default: the one holding the current directory)")
	c.Flags().BoolVar(&deleteBranch, "delete-branch", false, "also delete the workspace's local branch")
	c.Flags().BoolVar(&asJSON, "json", false, "print the result as JSON")
	return c
}

// resolveWorkspaceProject is the project ref names (matchProjectRef), or,
// with no ref, the project whose folder holds the current directory, the
// deepest when folders nest.
func resolveWorkspaceProject(ctx context.Context, client projectLister, ref string) (*reliantv1.Project, error) {
	resp, err := client.ListProjects(ctx, connect.NewRequest(&reliantv1.ListProjectsRequest{}))
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	projects := resp.Msg.GetProjects()
	if ref != "" {
		return matchProjectRef(projects, ref, "no project of yours is named %q: pass its id ('reliant project list')")
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(wd); err == nil {
		wd = real
	}
	var best *reliantv1.Project
	for _, p := range projects {
		dir := filepath.Clean(p.GetPath())
		if p.GetPath() == "" {
			continue
		}
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dir = real
		}
		if (wd == dir || strings.HasPrefix(wd, dir+string(filepath.Separator))) && (best == nil || len(dir) > len(filepath.Clean(best.GetPath()))) {
			best = p
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no project of yours holds %s: pass --project <id|name|path> ('reliant project list')", wd)
	}
	return best, nil
}

// listWorkspaces reads every page of the project's live (or archived)
// workspaces.
func listWorkspaces(ctx context.Context, client worktreeClient, projectID string, archived bool) ([]*reliantv1.Worktree, error) {
	var all []*reliantv1.Worktree
	token := ""
	for range worktreeMaxPages {
		var (
			page []*reliantv1.Worktree
			next string
		)
		if archived {
			req := &reliantv1.ListArchivedWorktreesRequest{ProjectId: projectID}
			if token != "" {
				req.PageToken = &token
			}
			resp, err := client.ListArchivedWorktrees(ctx, connect.NewRequest(req))
			if connect.CodeOf(err) == connect.CodeUnimplemented {
				// A server older than ListArchivedWorktrees: say so, and
				// that the live list still works, rather than a bare
				// "unimplemented".
				return nil, fmt.Errorf("listing archived workspaces: this Reliant server predates the archived list; 'reliant worktree list' without --archived still works: %w", err)
			}
			if err != nil {
				return nil, fmt.Errorf("listing archived workspaces: %w", err)
			}
			page, next = resp.Msg.GetWorktrees(), resp.Msg.GetNextPageToken()
		} else {
			req := &reliantv1.ListWorktreesRequest{ProjectId: projectID}
			if token != "" {
				req.PageToken = &token
			}
			resp, err := client.ListWorktrees(ctx, connect.NewRequest(req))
			if err != nil {
				return nil, fmt.Errorf("listing workspaces: %w", err)
			}
			page, next = resp.Msg.GetWorktrees(), resp.Msg.GetNextPageToken()
		}
		all = append(all, page...)
		if next == "" {
			return all, nil
		}
		token = next
	}
	return all, nil
}

// resolveArchiveWorkspace is the workspace ref names: an id first (GetWorktree),
// then a live workspace's name, then its branch, in the project.
func resolveArchiveWorkspace(ctx context.Context, client worktreeClient, projects projectLister, ref, project string) (*reliantv1.Worktree, error) {
	if ref == "" {
		return nil, errors.New("name a workspace by its id or name ('reliant worktree list')")
	}
	resp, err := client.GetWorktree(ctx, connect.NewRequest(&reliantv1.GetWorktreeRequest{WorktreeId: ref}))
	switch code := connect.CodeOf(err); {
	case err == nil && resp.Msg.GetWorktree().GetId() != "":
		return resp.Msg.GetWorktree(), nil
	case err != nil && code != connect.CodeNotFound && code != connect.CodeInvalidArgument:
		return nil, fmt.Errorf("reading workspace %s: %w", ref, err)
	}
	p, err := resolveWorkspaceProject(ctx, projects, project)
	if err != nil {
		return nil, err
	}
	list, err := listWorkspaces(ctx, client, p.GetId(), false)
	if err != nil {
		return nil, err
	}
	for _, field := range []func(*reliantv1.Worktree) string{(*reliantv1.Worktree).GetName, (*reliantv1.Worktree).GetBranch} {
		var named []*reliantv1.Worktree
		for _, w := range list {
			if field(w) == ref {
				named = append(named, w)
			}
		}
		switch len(named) {
		case 0:
			continue
		case 1:
			return named[0], nil
		default:
			ids := make([]string, len(named))
			for i, w := range named {
				ids[i] = w.GetId()
			}
			return nil, fmt.Errorf("%d workspaces of %s match %q: name one by id (%s)", len(named), p.GetName(), ref, strings.Join(ids, ", "))
		}
	}
	return nil, fmt.Errorf("no live workspace of %s has the id, name or branch %q ('reliant worktree list --project %s')", p.GetName(), ref, p.GetId())
}

func workspaceLabel(w *reliantv1.Worktree) string {
	if w.GetName() != "" {
		return fmt.Sprintf("workspace %s (%s)", w.GetName(), w.GetId())
	}
	return "workspace " + w.GetId()
}

func workspaceStatus(w *reliantv1.Worktree) string {
	return strings.ToLower(strings.TrimPrefix(w.GetStatus().String(), "WORKTREE_STATUS_"))
}

func printWorkspaces(out io.Writer, list []*reliantv1.Worktree, archived bool) error {
	if len(list) == 0 {
		if archived {
			_, err := fmt.Fprintln(out, "No archived workspaces.")
			return err
		}
		_, err := fmt.Fprintln(out, "No workspaces.")
		return err
	}
	when, whenOf := "LAST ACTIVE", (*reliantv1.Worktree).GetLastActive
	if archived {
		when, whenOf = "ARCHIVED", (*reliantv1.Worktree).GetDeletedAt
	}
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintf(tw, "ID\tNAME\tBRANCH\tSTATUS\t%s\tPATH\n", when)
	for _, w := range list {
		name := w.GetName()
		if w.GetIsMain() {
			name += " (main)"
		}
		at := ""
		if t := whenOf(w); t != "" {
			at = displayTime(t)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", w.GetId(), name, w.GetBranch(), workspaceStatus(w), at, w.GetPath())
	}
	return tw.Flush()
}

// writeWorktreesJSON prints {"worktrees": [...]}, each in the proto's JSON
// form, as `chat list --json` prints chats.
func writeWorktreesJSON(out io.Writer, list []*reliantv1.Worktree) error {
	items := make([]json.RawMessage, 0, len(list))
	for _, w := range list {
		b, err := chatJSON.Marshal(w)
		if err != nil {
			return err
		}
		items = append(items, b)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string][]json.RawMessage{"worktrees": items})
}
