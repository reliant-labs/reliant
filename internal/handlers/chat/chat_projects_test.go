// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/store"
)

// chatProjectsFixture is a chat on one machine whose primary project is
// installed there, plus helpers for the projects it may attach.
type chatProjectsFixture struct {
	*moveFixture
	daemon  string
	primary string
	mainWT  string
	chatID  string
}

func (f *chatProjectsFixture) exec(query string, args ...any) {
	f.t.Helper()
	_, err := f.repo.ORM().Bun().NewRaw(query, args...).Exec(f.ctx)
	require.NoError(f.t, err)
}

func newChatProjectsFixture(t *testing.T) *chatProjectsFixture {
	t.Helper()
	f := newMoveFixture(t)
	machine := f.machine(moveUser, "ovh-build-1")
	primary, mainWT := f.project(moveUser, "reliant")
	f.install(primary, machine, "/home/workspace/projects/reliant")
	chatID := f.chat(primary, mainWT, &machine)
	return &chatProjectsFixture{moveFixture: f, daemon: machine, primary: primary, mainWT: mainWT, chatID: chatID}
}

// installed is another project of the user's, installed on the chat's machine.
func (f *chatProjectsFixture) installed(name string) string {
	f.t.Helper()
	id, _ := f.project(moveUser, name)
	f.install(id, f.daemon, "/home/workspace/projects/"+name)
	return id
}

func (f *chatProjectsFixture) attach(projectID string) ([]*reliantv1.ChatProject, error) {
	f.t.Helper()
	resp, err := f.service.AttachProjectToChat(f.ctx, connect.NewRequest(&reliantv1.AttachProjectToChatRequest{ChatId: f.chatID, ProjectId: projectID}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.Projects, nil
}

func (f *chatProjectsFixture) list() []*reliantv1.ChatProject {
	f.t.Helper()
	resp, err := f.service.ListChatProjects(f.ctx, connect.NewRequest(&reliantv1.ListChatProjectsRequest{ChatId: f.chatID}))
	require.NoError(f.t, err)
	return resp.Msg.Projects
}

func projectNames(ps []*reliantv1.ChatProject) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.GetName())
	}
	return out
}

func TestChatProjectsListsThePrimaryAlone(t *testing.T) {
	f := newChatProjectsFixture(t)
	got := f.list()
	require.Len(t, got, 1)
	assert.True(t, got[0].GetPrimary())
	assert.Equal(t, f.primary, got[0].GetProjectId())
	assert.Equal(t, f.daemon, got[0].GetDaemonId())
	assert.Equal(t, "/home/workspace/projects/reliant", got[0].GetPath())
}

func TestAttachProjectToChatAttachesAndDetaches(t *testing.T) {
	f := newChatProjectsFixture(t)
	forge := f.installed("forge")
	cp := f.installed("control-plane")

	got, err := f.attach(forge)
	require.NoError(t, err)
	assert.Equal(t, []string{"reliant", "forge"}, projectNames(got))
	assert.Equal(t, "/home/workspace/projects/forge", got[1].GetPath())
	assert.Equal(t, f.daemon, got[1].GetDaemonId(), "the attachment records the machine it was checked on")
	assert.False(t, got[1].GetPrimary())
	assert.NotEmpty(t, got[1].GetAttachedAt())

	_, err = f.attach(cp)
	require.NoError(t, err)
	assert.Equal(t, []string{"reliant", "forge", "control-plane"}, projectNames(f.list()))

	// The primary and a repeat change nothing.
	got, err = f.attach(f.primary)
	require.NoError(t, err)
	assert.Equal(t, []string{"reliant", "forge", "control-plane"}, projectNames(got))
	got, err = f.attach(forge)
	require.NoError(t, err)
	assert.Equal(t, []string{"reliant", "forge", "control-plane"}, projectNames(got))

	resp, err := f.service.DetachProjectFromChat(f.ctx, connect.NewRequest(&reliantv1.DetachProjectFromChatRequest{ChatId: f.chatID, ProjectId: forge}))
	require.NoError(t, err)
	assert.Equal(t, []string{"reliant", "control-plane"}, projectNames(resp.Msg.Projects))

	// The primary cannot be detached.
	_, err = f.service.DetachProjectFromChat(f.ctx, connect.NewRequest(&reliantv1.DetachProjectFromChatRequest{ChatId: f.chatID, ProjectId: f.primary}))
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
}

func TestAttachProjectToChatRefusesAProjectNotOnTheChatsMachine(t *testing.T) {
	f := newChatProjectsFixture(t)
	elsewhere := f.machine(moveUser, "laptop")
	forge, _ := f.project(moveUser, "forge")
	f.install(forge, elsewhere, "/Users/me/src/forge")
	f.exec(`UPDATE projects SET remote_url = 'https://github.com/reliant-labs/forge.git' WHERE id = ?`, forge)

	_, err := f.attach(forge)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Contains(t, err.Error(), "clone it onto", "the refusal says how to set it up on the chat's machine")

	// The detail is what the client's "set it up on this machine" reads.
	var ce *connect.Error
	require.True(t, errors.As(svcerr.ToConnect(err), &ce))
	var missing *reliantv1.ProjectCheckoutMissing
	for _, d := range ce.Details() {
		if v, derr := d.Value(); derr == nil {
			if m, ok := v.(*reliantv1.ProjectCheckoutMissing); ok {
				missing = m
			}
		}
	}
	require.NotNil(t, missing, "the refusal carries a ProjectCheckoutMissing detail")
	assert.Equal(t, forge, missing.GetProjectId())
	assert.Equal(t, f.daemon, missing.GetDaemonId())
	assert.Equal(t, []string{elsewhere}, missing.GetAvailableDaemonIds())
	assert.Equal(t, []string{"reliant"}, projectNames(f.list()), "nothing was attached")

	// Once it is set up there, the attach goes through.
	f.install(forge, f.daemon, "/home/workspace/projects/forge")
	_, err = f.attach(forge)
	require.NoError(t, err)
}

func TestAttachProjectToChatRefusals(t *testing.T) {
	f := newChatProjectsFixture(t)

	t.Run("another user's project reads as not found", func(t *testing.T) {
		theirs, _ := f.project("someone-else", "theirs")
		f.install(theirs, f.daemon, "/srv/theirs")
		_, err := f.attach(theirs)
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	})

	t.Run("another user's chat reads as not found", func(t *testing.T) {
		forge := f.installed("forge-" + t.Name())
		_, err := f.service.AttachProjectToChat(context.WithValue(f.ctx, auth.UserIDContextKey, "someone-else"),
			connect.NewRequest(&reliantv1.AttachProjectToChatRequest{ChatId: f.chatID, ProjectId: forge}))
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))
	})

	t.Run("a project of another org", func(t *testing.T) {
		g := newMoveFixture(t).withOrgs()
		acme := "acme"
		box := g.machine(moveUser, "box")
		primary, main := g.orgProject("acme-app", &acme)
		g.install(primary, box, "/srv/acme-app")
		// No org yet: it belongs to the caller's default org, not acme.
		legacy, _ := g.orgProject("legacy", nil)
		g.install(legacy, box, "/srv/legacy")
		chatID := g.chat(primary, main, &box)
		_, err := g.service.AttachProjectToChat(g.ctx, connect.NewRequest(&reliantv1.AttachProjectToChatRequest{ChatId: chatID, ProjectId: legacy}))
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		assert.Contains(t, err.Error(), "organization")
	})

	t.Run("a chat with no machine", func(t *testing.T) {
		forge := f.installed("forge-nomachine")
		chatID := f.chat(f.primary, f.mainWT, nil)
		f.exec(`UPDATE chats SET no_machine = true WHERE id = ?`, chatID)
		_, err := f.service.AttachProjectToChat(f.ctx, connect.NewRequest(&reliantv1.AttachProjectToChatRequest{ChatId: chatID, ProjectId: forge}))
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	})

	t.Run("an archived chat", func(t *testing.T) {
		forge := f.installed("forge-archived")
		chatID := f.chat(f.primary, f.mainWT, &f.daemon)
		require.NoError(t, f.repo.UpdateChatState(f.ctx, chatID, db.ChatStateArchived, "test"))
		_, err := f.service.AttachProjectToChat(f.ctx, connect.NewRequest(&reliantv1.AttachProjectToChatRequest{ChatId: chatID, ProjectId: forge}))
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	})
}

func TestAttachProjectToChatCap(t *testing.T) {
	f := newChatProjectsFixture(t)
	for i := 0; i < core.MaxChatProjects; i++ {
		_, err := f.attach(f.installed(fmt.Sprintf("p%02d", i)))
		require.NoError(t, err)
	}
	_, err := f.attach(f.installed("one-too-many"))
	assert.Equal(t, connect.CodeResourceExhausted, svcerr.Code(err))
}

func TestMoveChatToAnAttachedProjectDropsTheAttachment(t *testing.T) {
	f := newChatProjectsFixture(t)
	forge := f.installed("forge")
	_, err := f.attach(forge)
	require.NoError(t, err)

	_, err = f.move(&reliantv1.MoveChatToProjectRequest{ChatId: f.chatID, ProjectId: forge})
	require.NoError(t, err)
	got := f.list()
	require.Len(t, got, 1, "the new primary is not also an attachment")
	assert.Equal(t, forge, got[0].GetProjectId())
	assert.True(t, got[0].GetPrimary())
}

// A stale handler read must not re-attach a project that has become primary.
func TestAttachAfterMoveWithStaleChat(t *testing.T) {
	f := newChatProjectsFixture(t)
	project := f.installed("forge")
	stale, err := store.GetChat(f.ctx, f.repo.ORM(), f.chatID)
	require.NoError(t, err)
	primary, err := f.repo.GetProject(f.ctx, f.primary)
	require.NoError(t, err)
	_, err = f.move(&reliantv1.MoveChatToProjectRequest{ChatId: f.chatID, ProjectId: project})
	require.NoError(t, err)
	require.NoError(t, f.service.attachProject(f.ctx, moveUser, stale, primary, project))
	rows, err := f.repo.ListChatProjects(f.ctx, f.chatID)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestChatProjectsOmitsCrossOrgAttachments(t *testing.T) {
	for _, movedPrimary := range []bool{false, true} {
		t.Run(fmt.Sprint(movedPrimary), func(t *testing.T) {
			f := newChatProjectsFixture(t)
			f.withOrgs()
			project := f.installed("forge")
			_, err := f.attach(project)
			require.NoError(t, err)
			// Live membership permits both projects; only their orgs differ.
			f.exec(`UPDATE projects SET org_id = 'acme' WHERE id IN (?, ?)`, f.primary, project)
			f.exec(`UPDATE chats SET org_id = 'acme' WHERE id = ?`, f.chatID)
			if movedPrimary {
				f.exec(`UPDATE projects SET org_id = 'home' WHERE id = ?`, f.primary)
				f.exec(`UPDATE chats SET org_id = 'home' WHERE id = ?`, f.chatID)
			} else {
				f.exec(`UPDATE projects SET org_id = 'home' WHERE id = ?`, project)
			}
			assert.Equal(t, []string{"reliant"}, projectNames(f.list()))
		})
	}
}

func TestChatProjectsMachineAndPathFollowChatPin(t *testing.T) {
	f := newChatProjectsFixture(t)
	project := f.installed("forge")
	_, err := f.attach(project)
	require.NoError(t, err)
	machine := f.machine(moveUser, "laptop")
	f.install(project, machine, "/Users/me/forge")
	f.exec(`UPDATE chats SET active_daemon_id = ? WHERE id = ?`, machine, f.chatID)
	got := f.list()
	require.Len(t, got, 2)
	assert.Equal(t, machine, got[1].GetDaemonId())
	assert.Equal(t, "/Users/me/forge", got[1].GetPath())
}

func TestStartChatAttachesProjects(t *testing.T) {
	e := newStartDaemonEnv(t)
	machine := e.daemon(t, "test-user")
	require.NoError(t, e.repo.UpsertProjectDaemon(e.ctx, e.projectID, machine, "/work/primary", nil))
	attachable, _ := newInvariantTestProject(t, e.ctx, e.repo, true)
	require.NoError(t, e.repo.UpsertProjectDaemon(e.ctx, attachable, machine, "/work/forge", nil))
	notHere, _ := newInvariantTestProject(t, e.ctx, e.repo, true)
	require.NoError(t, e.repo.UpsertProjectDaemon(e.ctx, notHere, e.daemon(t, "test-user"), "/work/elsewhere", nil))

	t.Run("one not on the chat's machine refuses the whole start", func(t *testing.T) {
		_, err := e.start(t, func(r *reliantv1.StartChatRequest) {
			r.DaemonId = ptr.Of(machine)
			r.AttachedProjectIds = []string{attachable, notHere}
		})
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
		assert.Empty(t, e.temporal.inputs, "a refused start launches nothing")
		chats, err := store.ListChats(e.ctx, e.repo.ORM(), db.ChatFilters{UserID: "test-user", ProjectID: &e.projectID})
		require.NoError(t, err)
		assert.Empty(t, chats, "a refused start writes no chat")
	})

	t.Run("attached with the chat, before its first turn", func(t *testing.T) {
		resp, err := e.start(t, func(r *reliantv1.StartChatRequest) {
			r.DaemonId = ptr.Of(machine)
			r.AttachedProjectIds = []string{attachable, e.projectID, attachable}
		})
		require.NoError(t, err)
		rows, err := e.repo.ListChatProjects(e.ctx, resp.Msg.GetChat().GetId())
		require.NoError(t, err)
		require.Len(t, rows, 1, "the primary and a repeat are ignored")
		assert.Equal(t, attachable, rows[0].ProjectID)
		require.NotNil(t, rows[0].DaemonID)
		assert.Equal(t, machine, *rows[0].DaemonID)
	})
}
