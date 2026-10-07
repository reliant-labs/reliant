// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"
)

// codePresenceDaemon answers project.code_presence with a canned result and
// records who it was asked through.
type codePresenceDaemon struct {
	hasCode     bool
	configFiles []string
	// failWith is returned instead of a response — the offline daemon.
	failWith error
	// scanError is the daemon-side scan failure carried in the response.
	scanError string

	mu        sync.Mutex
	probes    int
	daemonIDs []string
	paths     []string
}

func (d *codePresenceDaemon) SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return d.SendDaemonCommandToDaemon(ctx, userID, "", commandType, payload, timeoutMs)
}

func (d *codePresenceDaemon) SendDaemonCommandToDaemon(_ context.Context, _ string, daemonID string, commandType string, payload []byte, _ int32) ([]byte, error) {
	if commandType != "project.code_presence" {
		return nil, fmt.Errorf("unexpected command %q", commandType)
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.probes++
	d.daemonIDs = append(d.daemonIDs, daemonID)
	d.paths = append(d.paths, req.Path)
	d.mu.Unlock()
	if d.failWith != nil {
		return nil, d.failWith
	}
	return json.Marshal(map[string]any{
		"has_code":     d.hasCode,
		"config_files": d.configFiles,
		"error":        d.scanError,
	})
}

func (d *codePresenceDaemon) probeCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.probes
}

// greenfieldRun is a chat on its first turn: the root thread holds the user's
// first message and nothing else, exactly what the probe runs against.
type greenfieldRun struct {
	repo  *db.Repo
	ctx   context.Context
	input GreenfieldProbeInput
}

func newGreenfieldRun(t *testing.T, noMachine bool) *greenfieldRun {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	now := time.Now().UTC()

	userID := "greenfield-user"
	projectID := "greenfield-project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: projectID, UserID: userID, Name: "Greenfield", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	chatID := uuid.NewString()
	workflowName := "builtin://agent"
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, UserID: userID, ProjectID: projectID, WorkflowName: &workflowName,
		State: db.ChatStateIdle, NoMachine: noMachine, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	_, _, _, err := threads.NewService(repo).CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID: chatID, ChatID: chatID, WorkflowName: workflowName, Thread: chatID,
			Status: db.Active(), CreatedAt: now,
		},
		ThreadID: chatID,
		ChatID:   chatID,
	})
	require.NoError(t, err)
	_, err = repo.SaveMessageToThread(ctx, chatID, chatID,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_USER), "build me a landing page", &chatID, nil, nil)
	require.NoError(t, err)

	return &greenfieldRun{repo: repo, ctx: ctx, input: GreenfieldProbeInput{
		ChatID: chatID, WorkflowID: chatID, Thread: chatID, ProjectPath: "/home/workspace/projects/my-project",
	}}
}

func (r *greenfieldRun) messages(t *testing.T) []*db.Message {
	t.Helper()
	thread := r.input.Thread
	messages, err := r.repo.ListMessages(r.ctx, r.input.ChatID, db.MessageListOptions{Thread: &thread, Limit: 10})
	require.NoError(t, err)
	return messages
}

func (r *greenfieldRun) text(t *testing.T, messageID string) string {
	t.Helper()
	blocks, err := r.repo.ListContentBlocks(r.ctx, messageID)
	require.NoError(t, err)
	var text string
	for _, block := range blocks {
		if block.Content != nil {
			text += *block.Content
		}
	}
	return text
}

// The whole point: a chat opening on a directory with no code gets the stack
// guidance before the model first reads it — hidden from the transcript,
// visible to the model, and after the user's ask.
func TestGreenfieldProbeSeedsGuidanceWhenTheDirectoryHasNoCode(t *testing.T) {
	run := newGreenfieldRun(t, false)
	daemon := &codePresenceDaemon{configFiles: []string{".gitignore"}}

	out, err := NewGreenfieldProbeActivity(run.repo, daemon).Execute(run.ctx, run.input)
	require.NoError(t, err)
	assert.Equal(t, GreenfieldOutcomeGuidanceSeeded, out.Outcome)
	assert.Equal(t, []string{"/home/workspace/projects/my-project"}, daemon.paths,
		"the daemon must be asked about the run's working directory")

	messages := run.messages(t)
	require.Len(t, messages, 2)
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, messages[0].Role,
		"the user's first message stays first; the guidance follows the ask it is about")
	guidance := messages[1]
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM, guidance.Role,
		"USER-role messages short-circuit to ChatMessage in InterleavedTimeline before displayStyle is read, "+
			"so a USER role here would render harness framing as a chat bubble")
	require.NotNil(t, guidance.DisplayStyle)
	assert.Equal(t, reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN, *guidance.DisplayStyle,
		"the guidance is framing for the model, not something the user wrote")
	assert.Equal(t, BuildGreenfieldGuidance([]string{".gitignore"}), run.text(t, guidance.ID))
}

// Replaying the run from before the probe (a reset) runs the activity again.
// The thread is seeded once, and the daemon is not asked twice.
func TestGreenfieldProbeSeedsAThreadOnlyOnce(t *testing.T) {
	run := newGreenfieldRun(t, false)
	daemon := &codePresenceDaemon{}
	probe := NewGreenfieldProbeActivity(run.repo, daemon)

	first, err := probe.Execute(run.ctx, run.input)
	require.NoError(t, err)
	require.Equal(t, GreenfieldOutcomeGuidanceSeeded, first.Outcome)

	again, err := probe.Execute(run.ctx, run.input)
	require.NoError(t, err)
	assert.Equal(t, GreenfieldOutcomeAlreadySeeded, again.Outcome)
	assert.Equal(t, 1, daemon.probeCount())
	assert.Len(t, run.messages(t), 2)
}

// A project that already holds code is not a stack question. Nudging there is
// the expensive misfire: it reads as a pitch to someone who already chose.
func TestGreenfieldProbeSeedsNothingWhenTheDirectoryHasCode(t *testing.T) {
	run := newGreenfieldRun(t, false)

	out, err := NewGreenfieldProbeActivity(run.repo, &codePresenceDaemon{hasCode: true}).Execute(run.ctx, run.input)
	require.NoError(t, err)
	assert.Equal(t, GreenfieldOutcomeHasCode, out.Outcome)
	assert.Len(t, run.messages(t), 1, "a project with existing code must never get stack guidance")
}

// An unreachable or failing daemon is the common case during onboarding, not an
// anomaly. It costs the run the guidance and nothing else: no error, no message.
func TestGreenfieldProbeSeedsNothingWhenTheDaemonFails(t *testing.T) {
	for name, daemon := range map[string]*codePresenceDaemon{
		"daemon unreachable": {failWith: errors.New("no daemon connected")},
		"scan failed":        {scanError: "permission denied"},
	} {
		t.Run(name, func(t *testing.T) {
			run := newGreenfieldRun(t, false)

			out, err := NewGreenfieldProbeActivity(run.repo, daemon).Execute(run.ctx, run.input)
			require.NoError(t, err, "a failed probe is an outcome, never an activity failure")
			assert.Equal(t, GreenfieldOutcomeProbeFailed, out.Outcome)
			assert.Len(t, run.messages(t), 1)
		})
	}
}

// A daemon that never answers is bounded by the probe's own timeout, not by the
// caller's patience.
func TestGreenfieldProbeGivesUpOnASilentDaemon(t *testing.T) {
	run := newGreenfieldRun(t, false)
	silent := &silentDaemon{}

	started := time.Now()
	out, err := NewGreenfieldProbeActivity(run.repo, silent).Execute(run.ctx, run.input)
	require.NoError(t, err)
	assert.Equal(t, GreenfieldOutcomeProbeFailed, out.Outcome)
	assert.Less(t, time.Since(started), greenfieldProbeTimeout+time.Second)
	assert.Len(t, run.messages(t), 1)
}

// silentDaemon holds every command until the caller's deadline.
type silentDaemon struct{}

func (silentDaemon) SendDaemonCommand(ctx context.Context, _ string, _ string, _ []byte, _ int32) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s silentDaemon) SendDaemonCommandToDaemon(ctx context.Context, userID string, _ string, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return s.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
}

// A run with no machine never reaches one, not even to ask a question.
func TestGreenfieldProbeNeverAsksForANoMachineChat(t *testing.T) {
	run := newGreenfieldRun(t, true)
	daemon := &codePresenceDaemon{}

	out, err := NewGreenfieldProbeActivity(run.repo, daemon).Execute(run.ctx, run.input)
	require.NoError(t, err)
	assert.Equal(t, GreenfieldOutcomeNoMachine, out.Outcome)
	assert.Zero(t, daemon.probeCount())
	assert.Len(t, run.messages(t), 1)
}

// The probe asks the machine the run's tools execute on when the run has
// pinned one, and the user's default daemon otherwise.
func TestGreenfieldProbeAsksTheRunsDaemon(t *testing.T) {
	run := newGreenfieldRun(t, false)
	daemon := &codePresenceDaemon{hasCode: true}
	input := run.input
	input.DaemonID = "daemon-session"

	_, err := NewGreenfieldProbeActivity(run.repo, daemon).Execute(run.ctx, input)
	require.NoError(t, err)
	assert.Equal(t, []string{"daemon-session"}, daemon.daemonIDs)
}

// A worker with no daemon router has no filesystem to ask.
func TestGreenfieldProbeSkipsWithoutADaemonRouter(t *testing.T) {
	run := newGreenfieldRun(t, false)

	out, err := NewGreenfieldProbeActivity(run.repo, nil).Execute(run.ctx, run.input)
	require.NoError(t, err)
	assert.Equal(t, GreenfieldOutcomeNoDaemonRouter, out.Outcome)
	assert.Len(t, run.messages(t), 1)
}

// "No code" is not "no opinion". A .gitignore listing node_modules/ or a
// .vscode settings file pinning a Python interpreter is a stack declaration,
// and the model is told to read them before recommending anything.
func TestGreenfieldGuidanceNamesStackDeclaringConfig(t *testing.T) {
	content := BuildGreenfieldGuidance([]string{".gitignore", ".vscode/settings.json"})

	assert.Contains(t, content, ".gitignore")
	assert.Contains(t, content, ".vscode/settings.json")
}

// The message must leave the decision with the model and be explicit about the
// cases where forge is the wrong answer. Without the negative space this
// becomes a standing preference that fires on every empty directory, which is
// the failure mode that makes a suggestion feel like an ad.
func TestGreenfieldGuidanceContent(t *testing.T) {
	content := BuildGreenfieldGuidance(nil)

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
	content := BuildGreenfieldGuidance(nil)

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
	content := BuildGreenfieldGuidance(nil)

	assert.Contains(t, content, "scales the whole way up",
		"the guidance must say forge is not just a scaffold you outgrow")

	// Deploy is not k3d-only: the hosted runtime runs the app on Reliant's
	// infrastructure, with envs and secrets, and nothing to operate.
	assert.Contains(t, content, "hosted",
		"the guidance must show deploy reaches past the local cluster")
	assert.Contains(t, content, "secrets",
		"the hosted path's managed secrets are part of what forge gives a user")
}

// forge.External was removed from forge (#284), and with it any way to hand a
// workload to Fly, Cloud Run, ECS or Lambda. Naming them made the model promise
// a deploy target forge cannot reach (tracked in reliant-labs/forge#400).
func TestGreenfieldGuidanceDoesNotPromiseRemovedDeployTargets(t *testing.T) {
	content := BuildGreenfieldGuidance(nil)

	for _, removed := range []string{"Fly", "Cloud Run", "ECS", "Lambda"} {
		assert.NotContains(t, content, removed,
			"forge has no runtime for %s today; the guidance must not offer it", removed)
	}
}

// Static sites are a forge case, not an exclusion. A landing page or marketing
// site gets hosted static hosting, envs and promotion from the same project that
// later grows its API. Without saying so, the model treats "no backend" as "not
// forge" and hands the user a page with no deploy story.
func TestGreenfieldGuidanceOffersForgeForStaticSites(t *testing.T) {
	content := BuildGreenfieldGuidance(nil)

	positive, negative, found := strings.Cut(content, "Do NOT suggest forge when")
	require.True(t, found)

	for _, want := range []string{"landing page", "marketing", "static"} {
		assert.Contains(t, positive, want,
			"the guidance must name %q as a case forge serves", want)
	}
	assert.Contains(t, positive, "deploy/static-site",
		"the model needs the skill to load for the static path, not the service sequence")

	for _, excluded := range []string{"landing", "static", "marketing"} {
		assert.NotContains(t, negative, excluded,
			"%q must not be listed as a reason to steer away from forge", excluded)
	}
	assert.Contains(t, negative, "never be deployed",
		"a throwaway page with no deploy is still the case where forge is overhead")
}

// The harness only treats a project as forge — framework memory on every
// turn, forge's start-here skill preloaded for every agent — when forge.yaml
// sits at the project ROOT. `project new <name>`, the skill's own first
// example, nests the app one directory down, and the chat then never becomes a
// forge chat. So the guidance must ask for --in-place, and say why.
func TestGreenfieldGuidanceScaffoldsAtTheProjectRoot(t *testing.T) {
	content := BuildGreenfieldGuidance(nil)

	positive, _, found := strings.Cut(content, "Do NOT suggest forge when")
	require.True(t, found)
	assert.Contains(t, positive, "reliant forge project new --in-place",
		"the guidance must hand the model the in-place command")
	assert.Contains(t, positive, "forge.yaml at the project root",
		"the reason must be stated, or the model treats --in-place as a style preference")
	assert.Contains(t, positive, "--name",
		"a directory named after a worktree or branch needs the product name passed explicitly")
}

// The guidance is hidden from the user, which is what makes disclosure
// load-bearing rather than a nicety: if the model adopts forge silently, the
// user gets an opinionated stack they never chose, from a message they cannot
// see, with no name to look up. The instruction to announce it and link the
// repo is the only thing closing that gap.
func TestGreenfieldGuidanceRequiresDisclosure(t *testing.T) {
	content := BuildGreenfieldGuidance(nil)

	assert.Contains(t, content, forgeRepoURL,
		"the guidance must carry the forge repo link for the model to show the user")
	assert.Contains(t, content, "https://github.com/reliant-labs/forge",
		"the link must be the real repo URL, not a placeholder")
	assert.Contains(t, content, "the user cannot see this message",
		"the model needs to know WHY it must announce the choice, or it will treat it as optional")
	assert.Contains(t, content, "say so explicitly in your reply",
		"disclosure must be an instruction about the reply, not a vague suggestion")
}
