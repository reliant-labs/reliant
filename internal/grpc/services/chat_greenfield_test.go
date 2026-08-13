// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"
)

// greenfieldDaemonRouter answers project.code_presence with a canned result and
// records that it was asked.
type greenfieldDaemonRouter struct {
	worktreeTestDaemonRouter
	hasCode     bool
	configFiles []string
	// failWith, when set, is returned instead of a response — the offline
	// daemon case.
	failWith error
	probed   bool
}

func (r *greenfieldDaemonRouter) SendDaemonCommandToDaemon(ctx context.Context, userID, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return r.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

func (r *greenfieldDaemonRouter) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	if commandType != "project.code_presence" {
		return r.worktreeTestDaemonRouter.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
	}
	r.probed = true
	if r.failWith != nil {
		return nil, r.failWith
	}
	return json.Marshal(map[string]any{
		"has_code":     r.hasCode,
		"config_files": r.configFiles,
	})
}

// greenfieldFixture builds a user, project and chat row for the gating tests.
func greenfieldFixture(t *testing.T) (db.Repository, context.Context, *db.Chat) {
	t.Helper()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	userID := "greenfield-user"
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, userID)

	projectID := "greenfield-project-" + uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID:         projectID,
		UserID:     userID,
		Name:       "Greenfield Project",
		Path:       t.TempDir(),
		CreatedAt:  now,
		UpdatedAt:  now,
		LastActive: now,
	}))

	chatID := uuid.NewString()
	workflowName := "chat"
	chat := &db.Chat{
		ID:           chatID,
		UserID:       userID,
		ProjectID:    projectID,
		WorkflowName: &workflowName,
		State:        db.ChatStateIdle,
		CreatedAt:    now,
		UpdatedAt:    now,
		LastActive:   now,
	}
	require.NoError(t, repo.CreateChat(ctx, chat))

	return repo, ctx, chat
}

// The whole point: a chat opening on a directory with no code gets the stack
// guidance, hidden from the transcript but visible to the model.
func TestGreenfieldGuidanceFiresOnEmptyProject(t *testing.T) {
	repo, ctx, chat := greenfieldFixture(t)
	router := &greenfieldDaemonRouter{hasCode: false}
	svc := &ChatService{database: repo, daemonRouter: router}

	msg := svc.maybeGreenfieldGuidance(ctx, chat.UserID, chat)

	require.NotNil(t, msg, "an empty project on its first turn must get stack guidance")
	assert.True(t, router.probed, "the daemon must actually be asked about code presence")
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM, msg.Role,
		"USER-role messages short-circuit to ChatMessage in InterleavedTimeline before displayStyle is read, "+
			"so a USER role here would render harness framing as a chat bubble")
	require.NotNil(t, msg.DisplayStyle)
	assert.Equal(t, reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN, *msg.DisplayStyle,
		"the guidance is framing for the model, not something the user wrote")
}

// A project that already holds code is not a stack question. Nudging there is
// the expensive misfire: it reads as a pitch to someone who already chose.
func TestGreenfieldGuidanceSkipsProjectWithCode(t *testing.T) {
	repo, ctx, chat := greenfieldFixture(t)
	router := &greenfieldDaemonRouter{hasCode: true}
	svc := &ChatService{database: repo, daemonRouter: router}

	assert.Nil(t, svc.maybeGreenfieldGuidance(ctx, chat.UserID, chat),
		"a project with existing code must never get stack guidance")
}

// The guidance is about how to start. Once the conversation is underway the
// user's own words are better evidence than a directory listing, and repeating
// it every turn would be noise.
func TestGreenfieldGuidanceSkipsAfterFirstTurn(t *testing.T) {
	repo, ctx, chat := greenfieldFixture(t)
	router := &greenfieldDaemonRouter{hasCode: false}
	svc := &ChatService{database: repo, daemonRouter: router}

	// A prior turn exists. The root workflow and thread have to exist first —
	// messages hang off a context window, which is scoped to a real thread.
	now := time.Now().UTC()
	threadsSvc := threads.NewService(repo)
	_, _, _, err := threadsSvc.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID:           chat.ID,
			ChatID:       chat.ID,
			WorkflowName: "builtin://agent",
			Thread:       chat.ID,
			Status:       db.WorkflowStatusPending,
			CreatedAt:    now,
		},
		ThreadID: chat.ID,
		ChatID:   chat.ID,
	})
	require.NoError(t, err)

	_, err = repo.SaveMessageToThread(ctx, chat.ID, chat.ID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "build me something", nil, nil, nil)
	require.NoError(t, err)

	assert.Nil(t, svc.maybeGreenfieldGuidance(ctx, chat.UserID, chat),
		"guidance must only fire on the first turn of a chat")
	assert.False(t, router.probed,
		"a chat past its first turn should not even pay for the daemon roundtrip")
}

// An unreachable daemon is the common case during onboarding, not an anomaly.
// It must cost the user nothing.
func TestGreenfieldGuidanceSkipsWhenDaemonUnavailable(t *testing.T) {
	repo, ctx, chat := greenfieldFixture(t)
	router := &greenfieldDaemonRouter{failWith: errors.New("daemon offline")}
	svc := &ChatService{database: repo, daemonRouter: router}

	assert.Nil(t, svc.maybeGreenfieldGuidance(ctx, chat.UserID, chat),
		"an offline daemon must skip the guidance, never fail the send")
}

// Without a router there is no filesystem to ask. The replay harness and any
// other daemon-less construction take this path.
func TestGreenfieldGuidanceSkipsWithoutRouter(t *testing.T) {
	repo, ctx, chat := greenfieldFixture(t)
	svc := &ChatService{database: repo}

	assert.Nil(t, svc.maybeGreenfieldGuidance(ctx, chat.UserID, chat))
}

// "No code" is not "no opinion". A .gitignore listing node_modules/ or a
// .vscode settings file pinning a Python interpreter is a stack declaration,
// and the model is told to read them before recommending anything.
func TestGreenfieldGuidanceNamesStackDeclaringConfig(t *testing.T) {
	repo, ctx, chat := greenfieldFixture(t)
	router := &greenfieldDaemonRouter{
		hasCode:     false,
		configFiles: []string{".gitignore", ".vscode/settings.json"},
	}
	svc := &ChatService{database: repo, daemonRouter: router}

	msg := svc.maybeGreenfieldGuidance(ctx, chat.UserID, chat)
	require.NotNil(t, msg)

	assert.Contains(t, msg.Content, ".gitignore")
	assert.Contains(t, msg.Content, ".vscode/settings.json")
}

// The message must leave the decision with the model and be explicit about the
// cases where forge is the wrong answer. Without the negative space this
// becomes a standing preference that fires on every empty directory, which is
// the failure mode that makes a suggestion feel like an ad.
func TestGreenfieldGuidanceContent(t *testing.T) {
	content := buildGreenfieldGuidance(nil)

	assert.Contains(t, content, "When this chat started",
		"phrasing must be point-in-time: the row persists, and the project will not stay empty")

	assert.Contains(t, content, "Propose it, do not impose it")
	assert.Contains(t, content, "Do NOT suggest forge when")

	// The commitment forge implies must be stated — silence about a language
	// is not consent to Go + Next.js + Postgres.
	for _, want := range []string{"Go", "Next.js", "Postgres"} {
		assert.Contains(t, content, want,
			"the guidance must name what adopting forge commits the project to")
	}

	// The escape hatches that keep this from over-firing.
	for _, want := range []string{"data science", "embedded", "CLI", "library"} {
		assert.True(t, strings.Contains(content, want),
			"the guidance must name %q as a case where forge is the wrong answer", want)
	}

	assert.Contains(t, content, "reliant forge",
		"the model needs the actual command to hand off to")
}

// Forge scaffolds React Native frontends (packages/ui-native, --frontend-workspaces)
// alongside web ones, so listing mobile as a reason to look elsewhere sent the model
// away from a case forge actually serves. A roofing app whose crews work off phones
// is the example that surfaced it.
func TestGreenfieldGuidanceDoesNotExcludeMobile(t *testing.T) {
	content := buildGreenfieldGuidance(nil)

	_, negative, found := strings.Cut(content, "Do NOT suggest forge when")
	require.True(t, found)
	assert.NotContains(t, negative, "mobile",
		"mobile is a supported frontend target, not a reason to steer away from forge")

	assert.Contains(t, content, "React Native",
		"the model cannot offer mobile if the guidance never says forge does it")
}

// "Production-shaped app on day zero" undersold the ceiling: the same project grows
// to many services, several frontends and non-k3d deploy targets. A model that thinks
// forge is a starter kit will propose it and then migrate off it.
func TestGreenfieldGuidanceStatesTheCeiling(t *testing.T) {
	content := buildGreenfieldGuidance(nil)

	assert.Contains(t, content, "scales the whole way up",
		"the guidance must say forge is not just a scaffold you outgrow")

	// Deploy is not k3d-only; forge.External covers the common managed targets.
	assert.True(t,
		strings.Contains(content, "Fly") || strings.Contains(content, "Cloud Run"),
		"the guidance must show deploy reaches past the local cluster")
}

// The guidance is hidden from the user, which is what makes disclosure
// load-bearing rather than a nicety: if the model adopts forge silently, the
// user gets an opinionated stack they never chose, from a message they cannot
// see, with no name to look up. The instruction to announce it and link the
// repo is the only thing closing that gap.
func TestGreenfieldGuidanceRequiresDisclosure(t *testing.T) {
	content := buildGreenfieldGuidance(nil)

	assert.Contains(t, content, forgeRepoURL,
		"the guidance must carry the forge repo link for the model to show the user")
	assert.Contains(t, content, "https://github.com/reliant-labs/forge",
		"the link must be the real repo URL, not a placeholder")
	assert.Contains(t, content, "the user cannot see this message",
		"the model needs to know WHY it must announce the choice, or it will treat it as optional")
	assert.Contains(t, content, "say so explicitly in your reply",
		"disclosure must be an instruction about the reply, not a vague suggestion")
}
