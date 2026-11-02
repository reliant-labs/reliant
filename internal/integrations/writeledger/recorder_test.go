// Copyright (c) 2025 Reliant Labs
package writeledger_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/provenance"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
)

// fakeRunner stands in for the HTTP call. onCall runs while the "request" is in
// flight, which is when the intent row must already be visible.
type fakeRunner struct {
	mu     sync.Mutex
	calls  int
	onCall func()
	result *httpaction.Result
	err    error
}

func (f *fakeRunner) RunAuthenticated(_ context.Context, _ *reliantv1.IntegrationManifest, _ *reliantv1.ActionSpec, _ map[string]any, _ httpaction.CredentialSource, _ httpaction.CallSite) (*httpaction.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.onCall != nil {
		f.onCall()
	}
	return f.result, f.err
}

func githubManifest() *reliantv1.IntegrationManifest {
	return &reliantv1.IntegrationManifest{Id: "github"}
}

func action(id string, mutates bool) *reliantv1.ActionSpec {
	return &reliantv1.ActionSpec{Id: id, Mutates: mutates}
}

// seedRun creates a chat, its root workflow and the trigger event that launched
// it, so a call made "in" that run is attributed to a workflow and trigger.
func seedRun(t *testing.T, repo *db.Repo, workflowName string) (chatID, triggerID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	chatID = "chat-" + t.Name()
	triggerID = "trig-" + t.Name()
	wfID := "wf-" + t.Name()
	owner := "test-user"
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: chatID, Title: "t", ProjectID: "test-project", UserID: owner, WorkflowName: &workflowName,
		WorkflowID: &wfID, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return chatID, triggerID
}

func TestMutatingCallRecordsIntentBeforeTheRequestAndTheObjectIDAfter(t *testing.T) {
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	store := repo.IntegrationWrites()

	var sawDuringCall []*core.IntegrationWrite
	runner := &fakeRunner{result: &httpaction.Result{
		StatusCode: 201, ConnectionID: "conn-1",
		Data: map[string]any{"id": int64(4242), "html_url": "https://github.com/acme/app/pull/7#issuecomment-4242"},
	}}
	runner.onCall = func() {
		// The request is "in flight": the intent must already be committed, by a
		// different connection than the one the call runs on, because GitHub's
		// webhook can arrive before our own response does.
		var err error
		sawDuringCall, err = store.ListRecentIntegrationWrites(ctx, "github", "github:acme/app", time.Now().Add(-time.Minute), 10)
		require.NoError(t, err)
	}

	rec := writeledger.New(writeledger.Deps{Store: store, Runs: repo})
	body := "Looks good.\n\nOne nit."
	res, err := rec.Wrap(runner).RunAuthenticated(ctx, githubManifest(), action("issue.comment", true),
		map[string]any{"owner": "Acme", "repo": "App", "issue_number": float64(7), "body": body}, nil,
		httpaction.CallSite{RunID: "run-x", NodeID: "post", ToolCallID: "tc-1"})
	require.NoError(t, err)
	require.False(t, res.IsError)

	require.Len(t, sawDuringCall, 1, "the intent row must be committed before the request is made")
	w := sawDuringCall[0]
	assert.Equal(t, core.IntegrationWriteIntent, w.Status)
	assert.Equal(t, "github:acme/app", w.Scope, "scope is lowercased, as GitHub compares repository names")
	assert.Equal(t, "github:acme/app#7", w.Target)
	assert.Equal(t, "comment", w.Correlation["object_kind"])
	assert.Equal(t, provenance.BodyDigest(body), w.Correlation["body_sha256"])
	assert.Empty(t, w.ObjectID, "the id is only known after the call returns")
	assert.Equal(t, "post", w.NodeID)
	assert.Equal(t, "tc-1", w.ToolCallID)

	done, err := store.GetIntegrationWriteByObject(ctx, "github", "github:acme/app", "comment", "4242")
	require.NoError(t, err)
	assert.Equal(t, w.ID, done.ID)
	assert.Equal(t, core.IntegrationWriteDone, done.Status)
	assert.Equal(t, "4242", done.ObjectID)
	assert.Equal(t, "conn-1", done.ConnectionID)
	assert.Equal(t, "https://github.com/acme/app/pull/7#issuecomment-4242", done.ObjectURL)
	require.NotNil(t, done.CompletedAt)
}

func TestCreationLearnsItsTargetFromTheResponse(t *testing.T) {
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	store := repo.IntegrationWrites()
	runner := &fakeRunner{result: &httpaction.Result{StatusCode: 201, Data: map[string]any{"number": int64(91), "html_url": "https://github.com/acme/app/pull/91"}}}

	rec := writeledger.New(writeledger.Deps{Store: store, Runs: repo})
	_, err := rec.Wrap(runner).RunAuthenticated(ctx, githubManifest(), action("pr.create", true),
		map[string]any{"owner": "acme", "repo": "app", "head": "fork:feature/x", "base": "main", "title": "t"}, nil, httpaction.CallSite{})
	require.NoError(t, err)

	got, err := store.GetIntegrationWriteByObject(ctx, "github", "github:acme/app", "pull_request", "91")
	require.NoError(t, err)
	assert.Equal(t, "github:acme/app#91", got.Target, "the PR's own number replaces the repository-level target")
	assert.Equal(t, "feature/x", got.Correlation["branch"], "a fork head names the branch after the colon")
}

func TestFailedWriteLeavesAnIntentMarkedFailed(t *testing.T) {
	cases := []struct {
		name      string
		result    *httpaction.Result
		err       error
		ambiguous bool
		listed    bool
	}{
		{"definite rejection", &httpaction.Result{StatusCode: 422, IsError: true, Content: "validation failed"}, nil, false, false},
		{"server error may have applied", &httpaction.Result{StatusCode: 502, IsError: true, Content: "bad gateway", Retryable: true}, nil, true, true},
		{"transport error may have applied", nil, errors.New("context deadline exceeded"), true, true},
		{"refused credential never reached the provider", nil, &httpaction.CredentialError{Code: httpaction.CodeNeedsReauth, Message: "reconnect"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := db.NewTestRepo(t)
			ctx := context.Background()
			store := repo.IntegrationWrites()
			runner := &fakeRunner{result: tc.result, err: tc.err}
			rec := writeledger.New(writeledger.Deps{Store: store, Runs: repo})

			_, _ = rec.Wrap(runner).RunAuthenticated(ctx, githubManifest(), action("issue.comment", true),
				map[string]any{"owner": "acme", "repo": "app", "issue_number": float64(3), "body": "hi"}, nil, httpaction.CallSite{})

			var status string
			var ambiguous bool
			var errText string
			require.NoError(t, repo.DB.SQLDB().QueryRow(`SELECT status, ambiguous, error FROM integration_writes`).Scan(&status, &ambiguous, &errText))
			assert.Equal(t, "failed", status)
			assert.Equal(t, tc.ambiguous, ambiguous)
			assert.NotEmpty(t, errText)

			recent, err := store.ListRecentIntegrationWrites(ctx, "github", "github:acme/app", time.Now().Add(-time.Hour), 10)
			require.NoError(t, err)
			if tc.listed {
				assert.Len(t, recent, 1, "an ambiguous failure may still have caused an event")
			} else {
				assert.Empty(t, recent, "a failure that provably changed nothing caused nothing")
			}
		})
	}
}

func TestReadOnlyCallsAreNotRecorded(t *testing.T) {
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	runner := &fakeRunner{result: &httpaction.Result{StatusCode: 200, Data: map[string]any{}}}
	rec := writeledger.New(writeledger.Deps{Store: repo.IntegrationWrites(), Runs: repo})

	_, err := rec.Wrap(runner).RunAuthenticated(ctx, githubManifest(), action("issue.get", false),
		map[string]any{"owner": "acme", "repo": "app", "issue_number": float64(1)}, nil, httpaction.CallSite{})
	require.NoError(t, err)
	assert.Equal(t, 1, runner.calls)

	var n int
	require.NoError(t, repo.DB.SQLDB().QueryRow(`SELECT count(*) FROM integration_writes`).Scan(&n))
	assert.Zero(t, n)
}

func TestACallWhoseIntentCannotBeRecordedIsNotMade(t *testing.T) {
	runner := &fakeRunner{result: &httpaction.Result{StatusCode: 201}}
	rec := writeledger.New(writeledger.Deps{Store: brokenStore{}})

	res, err := rec.Wrap(runner).RunAuthenticated(context.Background(), githubManifest(), action("issue.comment", true),
		map[string]any{"owner": "acme", "repo": "app", "issue_number": float64(1), "body": "x"}, nil, httpaction.CallSite{})
	require.NoError(t, err)
	assert.Zero(t, runner.calls, "an unattributable write is how two workflows loop: it must not be sent")
	require.NotNil(t, res)
	assert.True(t, res.IsError)
	assert.True(t, res.Retryable, "nothing was sent, so retrying is safe")
}

func TestNilRecorderWrapsNothing(t *testing.T) {
	var rec *writeledger.Recorder
	runner := &fakeRunner{}
	assert.Same(t, writeledger.Runner(runner), rec.Wrap(runner))
}

func TestWritesAreAttributedToTheRunsWorkflowAndTrigger(t *testing.T) {
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	chatID, _ := seedRun(t, repo, "pr-review")
	runner := &fakeRunner{result: &httpaction.Result{StatusCode: 200, Data: map[string]any{"id": int64(1)}}}
	rec := writeledger.New(writeledger.Deps{Store: repo.IntegrationWrites(), Runs: repo})

	_, err := rec.Wrap(runner).RunAuthenticated(ctx, githubManifest(), action("pr.review.create", true),
		map[string]any{"owner": "acme", "repo": "app", "pull_number": float64(5), "event": "REQUEST_CHANGES", "body": "no"}, nil,
		httpaction.CallSite{RunID: chatID})
	require.NoError(t, err)

	var user, chat, workflow string
	require.NoError(t, repo.DB.SQLDB().QueryRow(`SELECT user_id, chat_id, workflow FROM integration_writes`).Scan(&user, &chat, &workflow))
	assert.Equal(t, "test-user", user)
	assert.Equal(t, chatID, chat)
	assert.Equal(t, "pr-review", workflow)
}

type brokenStore struct{}

func (brokenStore) CreateIntegrationWrite(context.Context, *core.IntegrationWrite) error {
	return errors.New("database is down")
}

func (brokenStore) CompleteIntegrationWrite(context.Context, string, core.IntegrationWriteResult) (bool, error) {
	return false, errors.New("database is down")
}

func TestTheLedgerRecordsWhoTheWriteWasMadeAs(t *testing.T) {
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	store := repo.IntegrationWrites()
	rec := writeledger.New(writeledger.Deps{Store: store, Runs: repo})
	runner := &fakeRunner{result: &httpaction.Result{StatusCode: 201, Data: map[string]any{"id": int64(11)}}}

	for id, tc := range map[string]struct{ identity, want string }{
		"owner": {"", core.IntegrationActorOwner},
		"app":   {httpaction.IdentityApp, core.IntegrationActorApp},
	} {
		_, err := rec.Wrap(runner).RunAuthenticated(ctx, githubManifest(), action("issue.comment", true),
			map[string]any{"owner": "acme", "repo": "app", "issue_number": float64(7), "body": id}, nil,
			httpaction.CallSite{RunID: "run-" + id, ActingIdentity: tc.identity})
		require.NoError(t, err)
	}
	rows, err := store.ListRecentIntegrationWrites(ctx, "github", "github:acme/app", time.Now().Add(-time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	byBody := map[string]string{}
	for _, w := range rows {
		byBody[w.Correlation["body_sha256"].(string)] = w.Actor
	}
	assert.Equal(t, core.IntegrationActorOwner, byBody[provenance.BodyDigest("owner")])
	assert.Equal(t, core.IntegrationActorApp, byBody[provenance.BodyDigest("app")])
}

type testScrubber struct{ secret string }

func (s testScrubber) Mask(in string) string { return strings.ReplaceAll(in, s.secret, "***") }
func (s testScrubber) MaskAny(v any) any {
	switch x := v.(type) {
	case string:
		return s.Mask(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[s.Mask(k)] = s.MaskAny(item)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = s.MaskAny(item)
		}
		return out
	}
	return v
}

type capturingStore struct {
	mu      sync.Mutex
	created []core.IntegrationWrite
	done    []core.IntegrationWriteResult
}

func (c *capturingStore) CreateIntegrationWrite(_ context.Context, w *core.IntegrationWrite) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.created = append(c.created, *w)
	return nil
}

func (c *capturingStore) CompleteIntegrationWrite(_ context.Context, _ string, res core.IntegrationWriteResult) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.done = append(c.done, res)
	return true, nil
}

// A call carrying a step's resolved secrets is made with the params intact, but
// nothing the ledger persists, before or after the call, can hold a value: the
// scope, target and correlation are planned from masked params, and the error a
// provider echoed back is masked.
func TestSecretCarryingCallPersistsNoValue(t *testing.T) {
	const secret = "stg_live/secret+value&k=1"
	ctx := httpaction.WithSecretCall(context.Background(), testScrubber{secret})
	store := &capturingStore{}

	var sentOwner any
	inner := recordingParams{
		fakeRunner: &fakeRunner{result: &httpaction.Result{StatusCode: 401, IsError: true, Content: "bad credentials for " + secret}},
		got:        &sentOwner,
	}
	rec := writeledger.New(writeledger.Deps{Store: store})
	res, err := rec.Wrap(inner).RunAuthenticated(ctx, githubManifest(), action("issue.comment", true),
		map[string]any{"owner": secret, "repo": "app", "issue_number": float64(7), "body": "hi " + secret}, nil,
		httpaction.CallSite{RunID: "run-x", NodeID: "post"})
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Equal(t, secret, sentOwner, "the outbound request keeps the real value")

	require.Len(t, store.created, 1)
	require.Len(t, store.done, 1)
	raw, err := json.Marshal(map[string]any{"intent": store.created[0], "result": store.done[0]})
	require.NoError(t, err)
	assert.NotContains(t, string(raw), secret)
	assert.NotContains(t, string(raw), url.QueryEscape(secret))
	assert.NotContains(t, string(raw), strings.ToLower(secret), "the lowercased scope must not carry it either")
	assert.Equal(t, core.IntegrationWriteFailed, store.done[0].Status)
	assert.Equal(t, "bad credentials for ***", store.done[0].Error)
	assert.Contains(t, store.created[0].Scope, "***", "the scope was planned from masked params")
}

// Control: with no secrets in play the ledger records the real params, as before.
func TestCallWithoutSecretsPersistsItsRealScope(t *testing.T) {
	store := &capturingStore{}
	rec := writeledger.New(writeledger.Deps{Store: store})
	_, err := rec.Wrap(&fakeRunner{result: &httpaction.Result{StatusCode: 201}}).RunAuthenticated(context.Background(), githubManifest(), action("issue.comment", true),
		map[string]any{"owner": "acme", "repo": "app", "issue_number": float64(7), "body": "hi"}, nil, httpaction.CallSite{})
	require.NoError(t, err)
	require.Len(t, store.created, 1)
	assert.Equal(t, "github:acme/app", store.created[0].Scope)
}

type recordingParams struct {
	*fakeRunner
	got *any
}

func (r recordingParams) RunAuthenticated(ctx context.Context, m *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec, params map[string]any, src httpaction.CredentialSource, site httpaction.CallSite) (*httpaction.Result, error) {
	*r.got = params["owner"]
	return r.fakeRunner.RunAuthenticated(ctx, m, a, params, src, site)
}
