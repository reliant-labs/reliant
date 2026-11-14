// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"

	"github.com/reliant-labs/forge/pkg/svcerr"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
	"github.com/reliant-labs/reliant/internal/runs"
)

// noAutoOrg is a control plane where org-floor sets chat_mode_floor NO_AUTO.
type noAutoOrg struct{}

func (noAutoOrg) ResolveOrgPolicy(_ context.Context, orgID string) (accesstokenclient.OrgPolicy, error) {
	if orgID != "org-floor" {
		return accesstokenclient.OrgPolicy{}, nil
	}
	return accesstokenclient.OrgPolicy{Version: 3, Policy: &controlplanev1.OrgPolicy{Approvals: &controlplanev1.ApprovalsPolicy{
		Mode: controlplanev1.OrgPolicyMode_ORG_POLICY_MODE_ENFORCE, ChatModeFloor: controlplanev1.ChatModeFloor_CHAT_MODE_FLOOR_NO_AUTO,
	}}}, nil
}

// floorMembers makes floor-user a member of every org.
type floorMembers struct{}

func (floorMembers) IsMember(_ context.Context, userID, _ string) (bool, bool, error) {
	return userID == "floor-user", true, nil
}

// orgChatFixture is a running agent chat of org (one with no org for "").
func orgChatFixture(t *testing.T, repo *db.Repo, org string) (context.Context, string) {
	t.Helper()
	const userID = "floor-user"
	ctx := authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, userID), floorMembers{})
	// Fixture timestamps only; nothing here depends on the clock.
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	var orgID *string
	if org != "" {
		orgID = &org
	}
	projectID := "floor-project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: projectID, UserID: userID, Name: "Floor", OrgID: orgID,
		Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now}))
	chatID := uuid.NewString()
	workflowName := "builtin://agent"
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, UserID: userID, Title: "Floor", ProjectID: projectID, OrgID: orgID,
		State: db.ChatStateIdle, WorkflowID: &chatID, WorkflowName: &workflowName, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	_, err := repo.CreateThread(ctx, &db.Thread{ID: chatID, ChatID: chatID, Origin: db.ThreadOriginMain, Status: db.ThreadStatusRunning, CreatedAt: now})
	require.NoError(t, err)
	_, err = repo.CreateContextWindow(ctx, &db.ContextWindow{ID: uuid.NewString(), ThreadID: chatID, Sequence: 0, CreatedAt: now})
	require.NoError(t, err)
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{ID: chatID, ChatID: chatID, WorkflowName: workflowName,
		Thread: chatID, Status: db.Active(), CreatedAt: now}))
	return orgpolicy.WithResolver(ctx, orgpolicy.New(noAutoOrg{})), chatID
}

// plainMessage is a send with no params at all: the path that used to carry
// no input update to the run.
func plainMessage(chatID string) *connect.Request[reliantv1.SendMessageRequest] {
	return connect.NewRequest(&reliantv1.SendMessageRequest{ChatId: chatID, Messages: []*reliantv1.InputMessage{{
		Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: "carry on",
	}}})
}

// stateUpdateModes are the modes the send's update_workflow_state signals set.
func stateUpdateModes(c *queryRecorder) []interface{} {
	var modes []interface{}
	for _, s := range c.signals {
		if s.Name != "update_workflow_state" {
			continue
		}
		if update, ok := s.Arg.(map[string]interface{}); ok {
			if mode, set := update["mode"]; set {
				modes = append(modes, mode)
			}
		}
	}
	return modes
}

// A chat that began in auto before its org set NO_AUTO is held to manual at
// the next turn by a plain message, with no params: the run is signalled
// mode manual before the message wakes it. plan is never touched, and a chat
// of an org without the floor is as before.
func TestSendMessage_LiveAutoRunHeldToTheOrgModeFloor(t *testing.T) {
	prev := orgpolicy.EnforcementOf(orgpolicy.PointApprovals)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointApprovals, orgpolicy.EnforcementEnforce)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointApprovals, prev) })

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	for _, tc := range []struct {
		name, org, runMode string
		want               []interface{}
	}{
		{"auto in a NO_AUTO org becomes manual", "org-floor", "auto", []interface{}{"manual"}},
		{"plan is never coerced", "org-floor", "plan", nil},
		{"manual stays as it is", "org-floor", "manual", nil},
		{"an org without the floor", "org-free", "auto", nil},
		{"a chat outside any org", "", "auto", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, chatID := orgChatFixture(t, repo, tc.org)
			temporal := &queryRecorder{
				wakeTestTemporalClient: wakeTestTemporalClient{absorbTestTemporalClient: absorbTestTemporalClient{
					exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
				}},
				inputs: map[string]interface{}{"mode": tc.runMode},
			}
			service := &Service{deps: Deps{Repo: repo, Temporal: temporal}, runs: runs.NewService(repo, temporal, nil)}
			_, err := service.SendMessage(ctx, plainMessage(chatID))
			require.NoError(t, err)
			assert.Equal(t, tc.want, stateUpdateModes(temporal))
		})
	}

	// In audit the floor changes nothing.
	orgpolicy.SetProcessEnforcement(orgpolicy.PointApprovals, orgpolicy.EnforcementAudit)
	ctx, chatID := orgChatFixture(t, repo, "org-floor")
	temporal := &queryRecorder{
		wakeTestTemporalClient: wakeTestTemporalClient{absorbTestTemporalClient: absorbTestTemporalClient{
			exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
		}},
		inputs: map[string]interface{}{"mode": "auto"},
	}
	service := &Service{deps: Deps{Repo: repo, Temporal: temporal}, runs: runs.NewService(repo, temporal, nil)}
	_, err := service.SendMessage(ctx, plainMessage(chatID))
	require.NoError(t, err)
	assert.Empty(t, stateUpdateModes(temporal))
}

// When the floor is enforced and neither the run nor the chat can say which
// mode it runs in, the send is refused retryably: neither woken unpoliced
// (it may be auto) nor forced to manual (it may be plan).
func TestWithLiveModeFloor_UnknownModeRefusesTheSend(t *testing.T) {
	prev := orgpolicy.EnforcementOf(orgpolicy.PointApprovals)
	orgpolicy.SetProcessEnforcement(orgpolicy.PointApprovals, orgpolicy.EnforcementEnforce)
	t.Cleanup(func() { orgpolicy.SetProcessEnforcement(orgpolicy.PointApprovals, prev) })

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, chatID := orgChatFixture(t, repo, "org-floor")
	chat, err := repo.GetChat(ctx, chatID)
	require.NoError(t, err)
	// The run is closed (its inputs cannot be queried), and the chat's
	// workflow cannot be built (its mode cannot be resolved).
	temporal := &queryRecorder{closed: true}
	service := &Service{deps: Deps{Repo: repo, Temporal: temporal}, runs: runs.NewService(repo, temporal, nil)}
	update, err := service.withLiveModeFloor(ctx, "floor-user", chat, "builtin://no-such-workflow", nil)
	require.Error(t, err)
	assert.Nil(t, update)
	assert.Equal(t, connect.CodeUnavailable, svcerr.ToConnect(err).Code())
}
