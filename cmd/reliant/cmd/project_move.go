// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// `reliant project move` moves a project to another org (control-plane
// docs/design/org-project-move.md §4.2), as Settings → Projects → Danger zone
// does: the server's preview, then MoveProjectToOrg with one request key, then
// GetProjectOrgMove until it settles.
//
// A move is a person's decision. The server takes it only from a person's
// credential (authz.ProjectOrgMove; reason person_required), and the CLI login
// is an access token every agent shell inherits, so on a deployment with an
// identity provider the server refuses it and this command says where to make
// the move instead. It refuses up front where it can tell already: in an agent's
// shell, and with --daemon-session (a machine's credential).

var errAgentCannotMoveProjects = errors.New("an agent cannot move a project to another organization from its shell: ask the user to move it in Settings → Projects → Danger zone")

// moveSettleTimeout bounds how long the command follows a move the server
// answered before it settled.
var moveSettleTimeout = 2 * time.Minute

// movePollInterval is how often an unsettled move is read again.
var movePollInterval = 1500 * time.Millisecond

// projectMoveClient is the slice of the ProjectService client the command
// needs; reliantv1connect.ProjectServiceClient satisfies it.
type projectMoveClient interface {
	ListProjects(context.Context, *connect.Request[reliantv1.ListProjectsRequest]) (*connect.Response[reliantv1.ListProjectsResponse], error)
	PreviewProjectOrgMove(context.Context, *connect.Request[reliantv1.PreviewProjectOrgMoveRequest]) (*connect.Response[reliantv1.PreviewProjectOrgMoveResponse], error)
	MoveProjectToOrg(context.Context, *connect.Request[reliantv1.MoveProjectToOrgRequest]) (*connect.Response[reliantv1.MoveProjectToOrgResponse], error)
	GetProjectOrgMove(context.Context, *connect.Request[reliantv1.GetProjectOrgMoveRequest]) (*connect.Response[reliantv1.GetProjectOrgMoveResponse], error)
}

func newProjectMoveCmd() *cobra.Command {
	var (
		toOrg  string
		dryRun bool
		yes    bool
	)
	cmd := &cobra.Command{
		Use:   "move <project> --to-org <org-id>",
		Short: "Move a project to another organization",
		Long: `Moves a project, with its chats, runs and automations, to another organization
you belong to. <project> is the project's id, name or path.

The command first shows what the move does, as the app's preview does, and
what running work refuses it. --dry-run stops there. Otherwise it asks you to
type the project's name, unless --yes is given.

A move is a person's decision: the server accepts it only from someone signed
in to the Reliant app. The CLI login is an access token any agent's shell can
use, so where the server refuses it, the command tells you to move the project
in the app (Settings → Projects → Danger zone).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv(daemonpolicy.AgentShellEnv) != "" { //nolint:forbidigo // the marker the daemon sets on agent shells (daemonpolicy.AgentShellEnv), a process signal, not configuration
				return errAgentCannotMoveProjects
			}
			if use, _ := cmd.Flags().GetBool("daemon-session"); use {
				return errors.New("--daemon-session is a machine's credential, and only a person can move a project: run this signed in as yourself, or move it in the app")
			}
			if strings.TrimSpace(toOrg) == "" {
				return errors.New("--to-org is required: the id of the organization to move the project to")
			}
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			mv := projectMove{
				client: newProjectServiceClient(conn),
				out:    cmd.OutOrStdout(),
				in:     cmd.InOrStdin(),
				dryRun: dryRun,
				yes:    yes,
			}
			if err := mv.run(cmd.Context(), args[0], strings.TrimSpace(toOrg)); err != nil {
				return conn.annotate(err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&toOrg, "to-org", "", "Id of the organization to move the project to")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what the move would do, and move nothing")
	cmd.Flags().BoolVar(&yes, "yes", false, "Move without asking to type the project's name")
	return cmd
}

// projectMove is one run of the command, its I/O injected for tests.
type projectMove struct {
	client projectMoveClient
	out    io.Writer
	in     io.Reader
	dryRun bool
	yes    bool
}

func (m projectMove) run(ctx context.Context, ref, toOrg string) error {
	project, err := m.findProject(ctx, ref)
	if err != nil {
		return err
	}
	pv, err := m.client.PreviewProjectOrgMove(ctx, connect.NewRequest(&reliantv1.PreviewProjectOrgMoveRequest{ProjectId: project.GetId(), ToOrgId: toOrg}))
	if err != nil {
		return moveRefusal("checking the move", err)
	}
	preview := pv.Msg
	printMovePreview(m.out, project, preview)
	if m.dryRun {
		fmt.Fprintln(m.out, "\nDry run: nothing moved.")
		return nil
	}
	if b := preview.GetBlockers(); b.GetChatsTotal() > 0 || b.GetLiveRuns() > 0 || b.GetBackgroundProcesses() > 0 {
		return errors.New("running work refuses the move: stop it (or let it finish), then run this again")
	}
	if !m.yes {
		if err := confirmProjectName(m.in, m.out, project.GetName()); err != nil {
			return err
		}
	}
	resp, err := m.client.MoveProjectToOrg(ctx, connect.NewRequest(&reliantv1.MoveProjectToOrgRequest{
		ProjectId:         project.GetId(),
		ToOrgId:           toOrg,
		ExpectedFromOrgId: preview.GetFromOrgId(),
		RequestKey:        uuid.NewString(),
	}))
	if err != nil {
		return moveRefusal("moving the project", err)
	}
	move, err := m.follow(ctx, resp.Msg.GetMove())
	if err != nil {
		return err
	}
	switch move.GetState() {
	case reliantv1.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_ABORTED:
		if d := move.GetDetail(); d != "" {
			return fmt.Errorf("the project was not moved: %s", d)
		}
		return errors.New("the project was not moved; nothing changed")
	case reliantv1.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_COMPLETED:
		fmt.Fprintf(m.out, "\nMoved %s to %s (move %s).\n", project.GetName(), move.GetToOrgId(), move.GetId())
	case reliantv1.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_MOVED:
		fmt.Fprintf(m.out, "\nMoved %s to %s (move %s); its automations are being turned back on.\n", project.GetName(), move.GetToOrgId(), move.GetId())
	default:
		fmt.Fprintf(m.out, "\nThe move is still under way (move %s, %s). The app shows when it finishes.\n", move.GetId(), moveStateName(move.GetState()))
	}
	return nil
}

// findProject resolves ref as a project id, then a name, then a path.
func (m projectMove) findProject(ctx context.Context, ref string) (*reliantv1.Project, error) {
	resp, err := m.client.ListProjects(ctx, connect.NewRequest(&reliantv1.ListProjectsRequest{}))
	if err != nil {
		return nil, fmt.Errorf("listing projects: %w", err)
	}
	projects := resp.Msg.GetProjects()
	for _, p := range projects {
		if p.GetId() == ref {
			return p, nil
		}
	}
	var named []*reliantv1.Project
	for _, p := range projects {
		if p.GetName() == ref || p.GetPath() == ref {
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

// follow reads an unsettled move again until it settles or the wait runs out.
func (m projectMove) follow(ctx context.Context, move *reliantv1.ProjectOrgMove) (*reliantv1.ProjectOrgMove, error) {
	if move == nil {
		return nil, errors.New("the server did not say what happened to the move")
	}
	deadline := time.Now().Add(moveSettleTimeout)
	for !moveSettled(move.GetState()) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(movePollInterval):
		}
		got, err := m.client.GetProjectOrgMove(ctx, connect.NewRequest(&reliantv1.GetProjectOrgMoveRequest{MoveId: move.GetId()}))
		if err != nil {
			return nil, fmt.Errorf("reading the move: %w", err)
		}
		move = got.Msg.GetMove()
	}
	return move, nil
}

func moveSettled(s reliantv1.ProjectOrgMoveState) bool {
	return s == reliantv1.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_MOVED ||
		s == reliantv1.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_COMPLETED ||
		s == reliantv1.ProjectOrgMoveState_PROJECT_ORG_MOVE_STATE_ABORTED
}

func moveStateName(s reliantv1.ProjectOrgMoveState) string {
	return strings.ToLower(strings.TrimPrefix(s.String(), "PROJECT_ORG_MOVE_STATE_"))
}

// moveRefusal turns the server's refusal into what to do next. A person-only
// refusal names the app, where the move can be made.
func moveRefusal(doing string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		switch ce.Meta().Get(svcerr.ReasonHeader) {
		case "person_required":
			return fmt.Errorf("%s: only a person signed in to the Reliant app can move a project, and the CLI login is an access token an agent can use. Move it in the app: Settings → Projects → Danger zone", doing)
		case "project_moves_disabled":
			return fmt.Errorf("%s: moving projects between organizations is not available on this server yet", doing)
		}
		if msg := ce.Message(); msg != "" {
			return fmt.Errorf("%s: %s", doing, msg)
		}
	}
	return fmt.Errorf("%s: %w", doing, err)
}

func printMovePreview(w io.Writer, project *reliantv1.Project, p *reliantv1.PreviewProjectOrgMoveResponse) {
	e := p.GetEffects()
	fmt.Fprintf(w, "Move %s from %s to %s\n\n", project.GetName(), p.GetFromOrgId(), p.GetToOrgId())
	fmt.Fprintf(w, "  %d chats and %d runs move with it; past usage stays billed to %s.\n", e.GetChats(), e.GetRuns(), p.GetFromOrgId())
	if e.GetTriggers() > 0 {
		fmt.Fprintf(w, "  %d automations move with it; %d enabled ones are paused and turned back on once they check out.\n", e.GetTriggers(), e.GetTriggersToPause())
	}
	for _, t := range e.GetTriggersNeedingSetup() {
		fmt.Fprintf(w, "  %s stays paused until it is set up again in %s (%s).\n", firstNonEmpty(t.GetName(), t.GetTriggerId()), p.GetToOrgId(), t.GetReason())
	}
	if n := e.GetOrgBindingsToClear(); n > 0 {
		fmt.Fprintf(w, "  %d automations lose their link to a connection of %s.\n", n, p.GetFromOrgId())
	}
	if n := e.GetSharesToClear(); n > 0 {
		fmt.Fprintf(w, "  %d shares with %s become private.\n", n, p.GetFromOrgId())
	}
	if name := e.GetForgeProjectName(); name != "" {
		fmt.Fprintf(w, "  A hosted deployment of %s stays in %s; the next deploy goes to %s.\n", name, p.GetFromOrgId(), p.GetToOrgId())
	}
	if n := len(e.GetMachines()); n > 0 {
		fmt.Fprintf(w, "  Nothing about its %d machines changes.\n", n)
	}
	b := p.GetBlockers()
	if b.GetChatsTotal() == 0 && b.GetLiveRuns() == 0 && b.GetBackgroundProcesses() == 0 {
		return
	}
	fmt.Fprintf(w, "\nRunning work refuses the move: %d chats, %d runs, %d background processes.\n", b.GetChatsTotal(), b.GetLiveRuns(), b.GetBackgroundProcesses())
	for _, c := range b.GetChats() {
		fmt.Fprintf(w, "  %s  %s\n", c.GetChatId(), firstNonEmpty(c.GetTitle(), "Untitled chat"))
	}
}

// confirmProjectName asks for the project's name, typed.
func confirmProjectName(in io.Reader, out io.Writer, name string) error {
	fmt.Fprintf(out, "\nType %s to move it: ", name)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("not confirmed: type the project's name, or pass --yes")
	}
	if strings.TrimSpace(line) != name {
		return errors.New("not confirmed: that is not the project's name; nothing moved")
	}
	return nil
}
