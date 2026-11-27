// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/serveraction"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

const testEcho = "reliant/test.echo@1"

func serverActionNode(uses string, with map[string]*structpb.Value) *reliantv1.Node {
	return &reliantv1.Node{Id: "say", Type: "action", Args: &reliantv1.Node_Action{Action: &reliantv1.ActionArgs{
		Uses: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: uses}},
		With: with,
	}}}
}

type stubResolver struct {
	principal  serveraction.Principal
	provenance serveraction.Provenance
	err        error
	calls      int
}

func (r *stubResolver) Resolve(context.Context, serveraction.Invocation, serveraction.RunFacts) (serveraction.Principal, serveraction.Provenance, error) {
	r.calls++
	return r.principal, r.provenance, r.err
}

func echoDispatcher(t *testing.T, enabled bool, resolver serveraction.Resolver, h serveraction.HandlerFunc) *serveraction.Dispatcher {
	t.Helper()
	c, err := serveraction.NewCatalog(serveraction.Spec{Name: testEcho, Description: "echo", Params: map[string]any{
		"type": "object", "required": []any{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}},
	}})
	require.NoError(t, err)
	d, err := serveraction.NewDispatcher(serveraction.Config{Enabled: enabled, Catalog: c, Resolver: resolver,
		Handlers: map[string]serveraction.Handler{testEcho: h}})
	require.NoError(t, err)
	return d
}

func runServerAction(t *testing.T, a *ActionActivity, rtx types.RuntimeContext, node *reliantv1.Node) *reliantv1.ActionOutput {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivity(a.Execute)
	val, err := env.ExecuteActivity(a.Execute, types.ActivityInput{Runtime: rtx, Node: node})
	require.NoError(t, err)
	var out reliantv1.ActionOutput
	require.NoError(t, val.Get(&out))
	return &out
}

// While pipeline.enabled is off (and on a worker with no dispatcher at all)
// a reliant/ action is refused, naming the switch, and nothing runs: not the
// handler, not a read of the run's records, not the integration catalog.
func TestServerActionRefusedWhileTheSwitchIsOff(t *testing.T) {
	ran := 0
	resolver := &stubResolver{principal: serveraction.Principal{UserID: "u"}}
	off := echoDispatcher(t, false, resolver, func(context.Context, *serveraction.Request) (*serveraction.Result, error) {
		ran++
		return &serveraction.Result{}, nil
	})
	node := serverActionNode(testEcho, map[string]*structpb.Value{"text": structpb.NewStringValue("hi")})
	for name, a := range map[string]*ActionActivity{
		"switch off":       NewActionActivity(nil).WithServerActions(off),
		"no dispatcher":    NewActionActivity(nil),
		"unknown, too":     NewActionActivity(nil).WithServerActions(off),
		"malformed, too":   NewActionActivity(nil).WithServerActions(off),
		"nil after wiring": NewActionActivity(nil).WithServerActions(nil),
	} {
		t.Run(name, func(t *testing.T) {
			n := node
			switch name {
			case "unknown, too":
				n = serverActionNode("reliant/nope.nope@1", nil)
			case "malformed, too":
				n = serverActionNode("reliant/Nope", nil)
			}
			out := runServerAction(t, a, types.RuntimeContext{WorkflowID: "run-1", StepID: "say"}, n)
			assert.True(t, out.GetIsError())
			assert.Equal(t, "failed_precondition", out.GetErrorCode())
			assert.Contains(t, out.GetContent(), "pipeline.enabled")
		})
	}
	assert.Zero(t, ran)
	assert.Zero(t, resolver.calls)
}

// With the switch on, the handler gets the node's own definition, its
// rendered params, its place in the run and the run's facts, and its answer
// is the node's output.
func TestServerActionPassesTheRunContextToItsHandler(t *testing.T) {
	resolver := &stubResolver{
		principal:  serveraction.Principal{UserID: "u-1", OrgID: "o-1", ProjectID: "p-1", ActingIdentity: "app"},
		provenance: serveraction.Provenance{WorkflowRef: "builtin://pr", Source: serveraction.SourceBuiltin},
	}
	var got *serveraction.Request
	d := echoDispatcher(t, true, resolver, func(_ context.Context, req *serveraction.Request) (*serveraction.Result, error) {
		got = req
		return &serveraction.Result{Content: "said hi", Data: map[string]any{"next": "review", "round": 2}}, nil
	})
	a := NewActionActivity(nil).WithServerActions(d)
	node := serverActionNode(testEcho, map[string]*structpb.Value{"text": structpb.NewStringValue("hi")})
	rtx := types.RuntimeContext{
		ChatID: "chat-1", Thread: "thread-1", WorkflowID: "run-1", StepID: "say", NodePath: "loop.say",
		LoopNodeID: "loop", LoopIteration: 3, CatalogPin: "pin-1", ActingIdentity: "app", Automation: "pr",
		Telemetry: &types.RunTelemetry{WorkflowRef: "builtin://pr", Trigger: "github"},
		ServerActionFrom: &types.ServerActionFrom{NodeID: "review", Type: "workflow", Role: "reviewer", Review: true, Ran: true,
			Output: map[string]interface{}{"verdict": "pass", "session_id": "s-1"}},
	}
	out := runServerAction(t, a, rtx, node)

	require.False(t, out.GetIsError(), out.GetContent())
	assert.Equal(t, testEcho, out.GetUses())
	assert.Equal(t, "said hi", out.GetContent())
	assert.Equal(t, map[string]any{"next": "review", "round": float64(2)}, out.GetData().AsMap())

	require.NotNil(t, got)
	assert.Equal(t, "say", got.Node.GetId(), "the handler reads the node's definition")
	assert.Equal(t, map[string]any{"text": "hi"}, got.Params)
	assert.Equal(t, resolver.principal, got.Principal)
	assert.Equal(t, resolver.provenance, got.Provenance)
	inv := got.Invocation
	assert.Equal(t, "run-1", inv.RunID)
	assert.Equal(t, "chat-1", inv.ChatID)
	assert.Equal(t, "thread-1", inv.ThreadID)
	assert.Equal(t, "say", inv.NodeID)
	assert.Equal(t, "loop.say", inv.NodePath)
	assert.Equal(t, "run-1:loop.say:loop#3", inv.PositionKey)
	assert.NotEmpty(t, inv.ActivityID, "inside an activity the scheduled execution is named")
	assert.Equal(t, inv.PositionKey+"/"+inv.TemporalRunID+"/"+inv.ActivityID, inv.ExecutionKey)
	assert.EqualValues(t, 1, inv.Attempt)
	assert.Equal(t, &serveraction.NodeRecord{NodeID: "review", Type: "workflow", Role: "reviewer", Review: true, Ran: true,
		Output: map[string]any{"verdict": "pass", "session_id": "s-1"}}, got.From, "the engine's record of the node `from` names")
}

func TestServerActionRunFactsComeFromTheRuntime(t *testing.T) {
	var facts serveraction.RunFacts
	resolver := resolverFunc(func(_ context.Context, _ serveraction.Invocation, run serveraction.RunFacts) (serveraction.Principal, serveraction.Provenance, error) {
		facts = run
		return serveraction.Principal{UserID: "u"}, serveraction.Provenance{}, nil
	})
	d := echoDispatcher(t, true, resolver, func(context.Context, *serveraction.Request) (*serveraction.Result, error) { return nil, nil })
	out := runServerAction(t, NewActionActivity(nil).WithServerActions(d), types.RuntimeContext{
		WorkflowID: "run-1", StepID: "say", CatalogPin: "pin-1", ActingIdentity: "app", Automation: "pr",
		Telemetry: &types.RunTelemetry{WorkflowRef: ".reliant/workflows/pr.yaml", Trigger: "github"},
	}, serverActionNode(testEcho, map[string]*structpb.Value{"text": structpb.NewStringValue("hi")}))
	require.False(t, out.GetIsError(), out.GetContent())
	assert.Equal(t, serveraction.RunFacts{WorkflowRef: ".reliant/workflows/pr.yaml", Trigger: "github", CatalogPin: "pin-1", ActingIdentity: "app", Automation: "pr"}, facts)
}

type resolverFunc func(context.Context, serveraction.Invocation, serveraction.RunFacts) (serveraction.Principal, serveraction.Provenance, error)

func (f resolverFunc) Resolve(ctx context.Context, inv serveraction.Invocation, run serveraction.RunFacts) (serveraction.Principal, serveraction.Provenance, error) {
	return f(ctx, inv, run)
}

// A handler's internal failure is an error output that names the action and
// none of the cause; its own public error passes through as written.
func TestServerActionHandlerErrorsBecomeOutputs(t *testing.T) {
	resolver := &stubResolver{principal: serveraction.Principal{UserID: "u"}}
	node := serverActionNode(testEcho, map[string]*structpb.Value{"text": structpb.NewStringValue("hi")})
	rtx := types.RuntimeContext{WorkflowID: "run-1", StepID: "say"}

	internal := echoDispatcher(t, true, resolver, func(context.Context, *serveraction.Request) (*serveraction.Result, error) {
		return nil, errors.New("pq: connection to 10.1.2.3:5432 refused for role worker")
	})
	out := runServerAction(t, NewActionActivity(nil).WithServerActions(internal), rtx, node)
	assert.True(t, out.GetIsError())
	assert.Equal(t, "internal", out.GetErrorCode())
	assert.Contains(t, out.GetContent(), testEcho)
	assert.NotContains(t, out.GetContent(), "10.1.2.3")

	public := echoDispatcher(t, true, resolver, func(context.Context, *serveraction.Request) (*serveraction.Result, error) {
		return nil, &serveraction.Error{Code: serveraction.CodePermissionDenied, Message: "only a reviewer node may record a verdict"}
	})
	out = runServerAction(t, NewActionActivity(nil).WithServerActions(public), rtx, node)
	assert.True(t, out.GetIsError())
	assert.Equal(t, "permission_denied", out.GetErrorCode())
	assert.Equal(t, "only a reviewer node may record a verdict", out.GetContent())

	badParams := runServerAction(t, NewActionActivity(nil).WithServerActions(public), rtx, serverActionNode(testEcho, nil))
	assert.Equal(t, "invalid_argument", badParams.GetErrorCode())
}

type fakeRunRows struct {
	rows map[string]*core.Workflow
	err  error
}

func (f fakeRunRows) LoadWorkflow(_ context.Context, id string) (*core.Workflow, error) {
	if f.err != nil {
		return nil, f.err
	}
	if w, ok := f.rows[id]; ok {
		return w, nil
	}
	return nil, svcerr.NotFound("workflow")
}

// The principal is the run's recorded owner and org, its chat's when the run
// recorded none; a definition that cannot be placed is unknown, never
// guessed; a builtin:// ref needs no lookup at all.
func TestServerActionResolverPrincipalAndBuiltinProvenance(t *testing.T) {
	repo := db.NewTestRepo(t)
	defer repo.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	chatOwner, runOwner := "user-"+uuid.NewString(), "user-"+uuid.NewString()
	projectID := "project-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: projectID, UserID: chatOwner, Name: "p", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now}))
	chatID := "chat-" + uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, UserID: chatOwner, ProjectID: projectID, Title: "c",
		State: db.ChatStateIdle, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	org := "org-1"
	rows := fakeRunRows{rows: map[string]*core.Workflow{"run-owned": {ID: "run-owned", OwnerUserID: &runOwner, OrgID: &org}}}
	r := NewServerActionResolver(repo, rows)

	principal, provenance, err := r.Resolve(ctx, serveraction.Invocation{RunID: "run-owned", ChatID: chatID},
		serveraction.RunFacts{WorkflowRef: "builtin://pr", Trigger: "github", ActingIdentity: "app", Automation: "pr"})
	require.NoError(t, err)
	assert.Equal(t, serveraction.Principal{UserID: runOwner, OrgID: org, ProjectID: projectID, ActingIdentity: "app", Automation: "pr"}, principal)
	assert.Equal(t, serveraction.Provenance{WorkflowRef: "builtin://pr", Source: serveraction.SourceBuiltin, Trigger: "github"}, provenance)

	principal, provenance, err = r.Resolve(ctx, serveraction.Invocation{RunID: "run-unrecorded", ChatID: chatID},
		serveraction.RunFacts{WorkflowRef: "pr"})
	require.NoError(t, err)
	assert.Equal(t, chatOwner, principal.UserID, "no run row: the chat's owner")
	assert.Equal(t, projectID, principal.ProjectID)
	assert.Equal(t, serveraction.SourceUnknown, provenance.Source, "a project ref with no pin cannot be placed")

	_, _, err = NewServerActionResolver(repo, fakeRunRows{err: errors.New("db down")}).Resolve(ctx, serveraction.Invocation{RunID: "x", ChatID: chatID}, serveraction.RunFacts{})
	assert.Error(t, err, "a failed read is an error, not an empty principal")
}

// A project workflow is placed through the catalog the run pinned: its file,
// and the branch and commit its repo was synced from.
func TestServerActionResolverPlacesAPinnedProjectWorkflow(t *testing.T) {
	repo := db.NewTestRepo(t)
	defer repo.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	userID, projectID, daemonID := "user-"+uuid.NewString(), "project-"+uuid.NewString(), uuid.NewString()
	require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: daemonID, UserID: userID}))
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: projectID, UserID: userID, Name: "labs", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now, LastActive: now}))
	const sha = "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
	pr := "name: pr\ndescription: d\napiVersion: v2\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n"
	wj, err := config.EncodeCatalogJSON([]config.StoredWorkflow{{Slug: "pr", Name: "pr", RelativePath: "api/.reliant/workflows/pr.yaml", YAMLContent: pr}})
	require.NoError(t, err)
	sj, err := config.EncodeCatalogJSON([]config.SyncedRepoState{
		{Repo: "", Branch: "main", Commit: "0000000000000000000000000000000000000000", DefaultBranch: "main"},
		{Repo: "api", Branch: "main", Commit: sha, DefaultBranch: "main"},
	})
	require.NoError(t, err)
	db.HoldProjectOnMachineForTest(t, repo, projectID, daemonID)
	_, err = repo.SyncProjectConfigSnapshot(ctx, &db.ProjectConfigRecord{ProjectID: projectID, DaemonID: daemonID,
		ProjectWorkflowsJSON: wj, RepoStatesJSON: sj, PushedAt: now}, true, nil)
	require.NoError(t, err)
	chatID := "chat-" + uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, UserID: userID, ProjectID: projectID, Title: "c",
		State: db.ChatStateIdle, ActiveDaemonID: &daemonID, CreatedAt: now, UpdatedAt: now, LastActive: now}))
	loaded, err := NewLoadWorkflowActivity(repo, workspaceRowsOf(repo)).Execute(ctx, LoadWorkflowInput{ChatID: chatID, WorkflowName: "pr"})
	require.NoError(t, err)
	require.NotEmpty(t, loaded.CatalogPin)

	r := NewServerActionResolver(repo, store.NewWorkflowExtStore(repo.ORM()))
	principal, provenance, err := r.Resolve(ctx, serveraction.Invocation{RunID: "run-" + uuid.NewString(), ChatID: chatID},
		serveraction.RunFacts{WorkflowRef: "pr", CatalogPin: loaded.CatalogPin, Trigger: "manual"})
	require.NoError(t, err)
	assert.Equal(t, userID, principal.UserID)
	assert.Equal(t, daemonID, principal.DaemonID, "the machine is the chat's own record")
	assert.Empty(t, principal.TriggerID, "a chat no activation launched has none")
	assert.Equal(t, serveraction.SourceProject, provenance.Source)
	assert.Equal(t, "api/.reliant/workflows/pr.yaml", provenance.Path)
	require.NotNil(t, provenance.Repo)
	assert.Equal(t, serveraction.RepoState{Repo: "api", Branch: "main", Commit: sha, DefaultBranch: "main"}, *provenance.Repo)

	_, unpinned, err := r.Resolve(ctx, serveraction.Invocation{ChatID: chatID}, serveraction.RunFacts{WorkflowRef: "pr"})
	require.NoError(t, err)
	assert.Equal(t, serveraction.SourceUnknown, unpinned.Source, "without the pin the live catalog may not be what the run executes")
}

func TestRepoOfPathPrefersTheNestedRepo(t *testing.T) {
	states := []config.SyncedRepoState{{Repo: ""}, {Repo: "api"}, {Repo: "api/sub"}, {Repo: "web"}}
	assert.Equal(t, "api/sub", repoOfPath(states, "api/sub/.reliant/workflows/x.yaml").Repo)
	assert.Equal(t, "api", repoOfPath(states, "api/.reliant/workflows/x.yaml").Repo)
	assert.Equal(t, "", repoOfPath(states, "x.yaml").Repo)
	assert.Equal(t, "", repoOfPath(states, "apix/.reliant/workflows/x.yaml").Repo, "a prefix must end at a path separator")
	assert.Nil(t, repoOfPath(nil, "x.yaml"))
}
