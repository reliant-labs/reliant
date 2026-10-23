// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitpublish"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/provenance"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

const activityPushToken = "ghs_ACTIVITY_one_shot_token"

type fakeGH struct{ def string }

func (f fakeGH) DefaultBranch(context.Context, gitpublish.Repo) (string, error) { return f.def, nil }
func (f fakeGH) Protected(_ context.Context, _ gitpublish.Repo, b string) (bool, error) {
	return b == "stable", nil
}
func (f fakeGH) PullRequest(_ context.Context, _ gitpublish.Repo, n int64) (gitpublish.PullRequestHead, error) {
	if n == 7 {
		return gitpublish.PullRequestHead{State: "open", HeadRepo: "acme/app", HeadRef: "fix/save", BaseRepo: "acme/app"}, nil
	}
	return gitpublish.PullRequestHead{State: "open", HeadRepo: "mallory/app", HeadRef: "fix/save", BaseRepo: "acme/app"}, nil
}

type fakeToken struct {
	calls int
	err   error
	actor string
}

func (f *fakeToken) PushToken(context.Context, httpaction.CallSite, gitpublish.Repo) (string, string, error) {
	f.calls++
	return activityPushToken, f.actor, f.err
}

// publishRouter plays the machine: prepare and push replies are canned, and
// every payload is kept so a test can look at what was sent.
type publishRouter struct {
	placementRecordingRouter
	prepare gitpublish.PrepareResponse
	push    gitpublish.PushResponse
	pushErr error
	mu      sync.Mutex
	sent    []sentCommand
}

type sentCommand struct {
	daemon, command string
	payload         []byte
}

func (r *publishRouter) SendDaemonCommandToDaemon(_ context.Context, _, daemonID, command string, payload []byte, _ int32) ([]byte, error) {
	r.mu.Lock()
	r.sent = append(r.sent, sentCommand{daemonID, command, append([]byte(nil), payload...)})
	r.mu.Unlock()
	switch command {
	case gitpublish.CommandPrepare:
		return json.Marshal(r.prepare)
	case gitpublish.CommandPush:
		if r.pushErr != nil {
			return nil, r.pushErr
		}
		return json.Marshal(r.push)
	}
	return json.Marshal(map[string]any{})
}

func (r *publishRouter) commands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.sent {
		out = append(out, c.command)
	}
	return out
}

// ledgerRecorder stands in for the write ledger.
type ledgerRecorder struct {
	writeledger.Service
	pushes   []writeledger.Push
	beginErr error
}

func (l *ledgerRecorder) BeginPush(_ context.Context, p writeledger.Push) (*writeledger.PushIntent, error) {
	if l.beginErr != nil {
		return nil, l.beginErr
	}
	l.pushes = append(l.pushes, p)
	return nil, nil
}

func lit(s string) *reliantv1.CelString {
	return &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: s}}
}

func publishNode(branch string, pr int64, allow ...string) *reliantv1.Node {
	args := &reliantv1.GitPublishArgs{Worktree: lit("/work/issue-12"), Repo: lit("acme/app"), Message: lit("Fix the bug")}
	if branch != "" {
		args.Branch = lit(branch)
	}
	if pr != 0 {
		args.PrHeadOf = &reliantv1.CelInt{Value: &reliantv1.CelInt_Literal{Literal: pr}}
	}
	if len(allow) > 0 {
		args.Allow = &reliantv1.CelStringList{Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: allow}}}
	}
	return &reliantv1.Node{Id: "publish", Type: "git_publish", Args: &reliantv1.Node_GitPublish{GitPublish: args}}
}

type publishFixture struct {
	*activityPlacementFixture
	router *publishRouter
	tok    *fakeToken
	ledger *ledgerRecorder
	act    *GitPublishActivity
	chat   string
}

func newPublishActivityFixture(t *testing.T) *publishFixture {
	t.Helper()
	base := newActivityPlacementFixture(t)
	r := &publishRouter{prepare: gitpublish.PrepareResponse{Committed: true, Commits: 1, HeadSHA: "abc1234def", BaseSHA: "0000111", FilesChanged: 3, Insertions: 10, Deletions: 2},
		push: gitpublish.PushResponse{Pushed: true}}
	f := &publishFixture{activityPlacementFixture: base, router: r, tok: &fakeToken{actor: core.IntegrationActorOwner}, ledger: &ledgerRecorder{}}
	f.act = NewGitPublishActivity(base.repo, r, nil)
	f.act.token = f.tok
	f.act.github = func(httpaction.CallSite) gitpublish.GitHubReader { return fakeGH{def: "trunk"} }
	f.act = f.act.WithLedger(f.ledger)
	f.chat = base.chat(t, "")
	return f
}

func (f *publishFixture) run(t *testing.T, node *reliantv1.Node) (*reliantv1.GitPublishOutput, error) {
	t.Helper()
	return f.act.Execute(context.Background(), ActivityInput{
		Runtime: types.RuntimeContext{ChatID: f.chat, WorkflowID: "run-42", StepID: "publish"}, Node: node,
	})
}

func TestGitPublishActivity_PushesAndRecordsTheIntentBeforeThePush(t *testing.T) {
	t.Parallel()

	f := newPublishActivityFixture(t)
	out, err := f.run(t, publishNode("autopilot/issue-12", 0, "autopilot/*"))
	require.NoError(t, err)

	assert.True(t, out.Pushed)
	assert.False(t, out.Refused, out.Refusal)
	assert.Equal(t, "autopilot/issue-12", out.Branch)
	assert.Equal(t, "abc1234def", out.Sha)
	assert.EqualValues(t, 3, out.FilesChanged)
	assert.EqualValues(t, 10, out.Insertions)
	assert.Equal(t, []string{gitpublish.CommandPrepare, gitpublish.CommandPush}, f.router.commands())

	require.Len(t, f.ledger.pushes, 1, "the push intent is in the write ledger")
	assert.Equal(t, writeledger.Push{Repo: "acme/app", Branch: "autopilot/issue-12", SHA: "abc1234def", RunID: "run-42", NodeID: "publish", Actor: core.IntegrationActorOwner}, f.ledger.pushes[0])

	var prep gitpublish.PrepareRequest
	require.NoError(t, json.Unmarshal(f.router.sent[0].payload, &prep))
	assert.Equal(t, "/work/issue-12", prep.Worktree)
	assert.Equal(t, provenance.RunTrailerKey, prep.Trailers[0].Key, "the Reliant-Run trailer is added")
	assert.Equal(t, "run-42", prep.Trailers[0].Value)

	var push gitpublish.PushRequest
	require.NoError(t, json.Unmarshal(f.router.sent[1].payload, &push))
	assert.Equal(t, "https://github.com/acme/app.git", push.RemoteURL, "built from the node's repo")
	assert.Equal(t, "abc1234def", push.ExpectSHA, "the pushed sha is the recorded sha")
	assert.Equal(t, activityPushToken, push.Token)
	assert.NotContains(t, string(f.router.sent[0].payload), activityPushToken, "prepare carries no credential")
}

// Policy refusals send nothing to the machine and fetch no credential.
func TestGitPublishActivity_PolicyRefusalsTouchNothing(t *testing.T) {
	t.Parallel()

	for name, node := range map[string]*reliantv1.Node{
		"the default branch":  publishNode("trunk", 0),
		"main":                publishNode("main", 0),
		"a release branch":    publishNode("release/v1", 0),
		"a protected branch":  publishNode("stable", 0),
		"outside the allow":   publishNode("feature/x", 0, "autopilot/*"),
		"a fork pull request": publishNode("", 8),
		"a malformed ref":     publishNode("refs/heads/x", 0),
	} {
		t.Run(name, func(t *testing.T) {
			f := newPublishActivityFixture(t)
			out, err := f.run(t, node)
			require.NoError(t, err, "a refusal is an output, not a failed node")
			assert.True(t, out.Refused)
			assert.NotEmpty(t, out.Refusal)
			assert.False(t, out.Pushed)
			assert.Empty(t, f.router.commands(), "nothing reached the machine")
			assert.Zero(t, f.tok.calls, "no credential was fetched")
			assert.Empty(t, f.ledger.pushes)
		})
	}
}

func TestGitPublishActivity_PullRequestHeadInThisRepoIsAllowed(t *testing.T) {
	t.Parallel()

	f := newPublishActivityFixture(t)
	out, err := f.run(t, publishNode("", 7, "autopilot/*"))
	require.NoError(t, err)
	assert.True(t, out.Pushed, out.Refusal)
	assert.Equal(t, "fix/save", out.Branch)
	require.Len(t, f.ledger.pushes, 1)
	assert.EqualValues(t, 7, f.ledger.pushes[0].PullNumber, "the synchronize event is attributed too")
}

// Nothing to commit: no push, no credential, no ledger row, no error.
func TestGitPublishActivity_NothingToPublishIsNotAnError(t *testing.T) {
	t.Parallel()

	f := newPublishActivityFixture(t)
	f.router.prepare = gitpublish.PrepareResponse{Commits: 0, HeadSHA: "abc1234def"}
	out, err := f.run(t, publishNode("autopilot/issue-12", 0))
	require.NoError(t, err)
	assert.False(t, out.Pushed)
	assert.False(t, out.Refused)
	assert.Equal(t, "autopilot/issue-12", out.Branch)
	assert.Equal(t, "abc1234def", out.Sha)
	assert.Equal(t, []string{gitpublish.CommandPrepare}, f.router.commands())
	assert.Zero(t, f.tok.calls)
	assert.Empty(t, f.ledger.pushes)
}

// A ledger that cannot record the intent stops the push: an unattributable
// write is how two workflows loop.
func TestGitPublishActivity_NoIntentNoPush(t *testing.T) {
	t.Parallel()

	f := newPublishActivityFixture(t)
	f.ledger.beginErr = errors.New("ledger down")
	out, err := f.run(t, publishNode("autopilot/issue-12", 0))
	require.NoError(t, err)
	assert.True(t, out.Refused)
	assert.False(t, out.Pushed)
	assert.NotContains(t, f.router.commands(), gitpublish.CommandPush)
}

func TestGitPublishActivity_RemoteRefusalIsReported(t *testing.T) {
	t.Parallel()

	f := newPublishActivityFixture(t)
	f.router.push = gitpublish.PushResponse{Refusal: "the remote branch has commits this checkout does not (non-fast-forward): the push was refused, not forced", NonFastForward: true}
	out, err := f.run(t, publishNode("autopilot/issue-12", 0))
	require.NoError(t, err)
	assert.True(t, out.Refused)
	assert.False(t, out.Pushed)
	assert.Contains(t, out.Refusal, "not forced")
}

// The machine going away mid-push is an error the step may retry; the token is
// not in what comes back.
func TestGitPublishActivity_TransportFailureIsAnErrorWithoutTheToken(t *testing.T) {
	t.Parallel()

	f := newPublishActivityFixture(t)
	f.router.pushErr = errors.New("daemon disconnected")
	_, err := f.run(t, publishNode("autopilot/issue-12", 0))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), activityPushToken)
}

// A credential the run cannot get is a refusal, with nothing pushed.
func TestGitPublishActivity_NoCredentialIsARefusal(t *testing.T) {
	t.Parallel()

	f := newPublishActivityFixture(t)
	f.tok.err = &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "GitHub is not connected"}
	out, err := f.run(t, publishNode("autopilot/issue-12", 0))
	require.NoError(t, err)
	assert.True(t, out.Refused)
	assert.Contains(t, out.Refusal, "not connected")
	assert.NotContains(t, f.router.commands(), gitpublish.CommandPush)
}

// The acting identity the token source reports is what the ledger records: the
// seam an installation-token identity plugs into.
func TestGitPublishActivity_LedgerRecordsTheTokenSourcesIdentity(t *testing.T) {
	t.Parallel()

	f := newPublishActivityFixture(t)
	f.tok.actor = core.IntegrationActorApp
	_, err := f.run(t, publishNode("autopilot/issue-12", 0))
	require.NoError(t, err)
	require.Len(t, f.ledger.pushes, 1)
	assert.Equal(t, core.IntegrationActorApp, f.ledger.pushes[0].Actor)
}

func TestGitPublishActivity_NoMachineRunIsRefusedAtTheNode(t *testing.T) {
	t.Parallel()

	f := newPublishActivityFixture(t)
	now := time.Now().UTC()
	nm := &db.Chat{ID: uuid.NewString(), ProjectID: f.projectID, UserID: f.userID, NoMachine: true, CreatedAt: now, UpdatedAt: now, LastActive: now}
	require.NoError(t, f.repo.CreateChat(context.Background(), nm))
	f.chat = nm.ID

	_, err := f.run(t, publishNode("autopilot/issue-12", 0))
	require.Error(t, err, "a publish needs a checkout on a machine")
	assert.Empty(t, f.router.commands())
	assert.Zero(t, f.tok.calls)
}

// pushCredSource is a CredentialSource that records every request and answers
// the owner's and the bot's by what the request says, as connauth does.
type pushCredSource struct {
	reqs     []httpaction.CredentialRequest
	botErr   error
	ownerErr error
}

type bearer string

func (b bearer) ConnectionID() string { return "test" }
func (b bearer) Apply(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer "+string(b))
	return nil
}
func (b bearer) Scrub(s string) string     { return s }
func (b bearer) Params() map[string]string { return nil }

func (r *pushCredSource) Credential(_ context.Context, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	r.reqs = append(r.reqs, req)
	if req.ActingIdentity == httpaction.IdentityApp {
		if r.botErr != nil {
			return nil, r.botErr
		}
		return bearer("ghs_bot_push_token"), nil
	}
	if r.ownerErr != nil {
		return nil, r.ownerErr
	}
	return bearer("ghu_owner_push_token"), nil
}

func (r *pushCredSource) ownerAsked() bool {
	for _, q := range r.reqs {
		if q.ActingIdentity != httpaction.IdentityApp {
			return true
		}
	}
	return false
}

func (f *publishFixture) runAs(t *testing.T, identity string, src httpaction.CredentialSource) (*reliantv1.GitPublishOutput, error) {
	t.Helper()
	f.act.token = runPushToken{credentials: src}
	return f.act.Execute(context.Background(), ActivityInput{
		Runtime: types.RuntimeContext{ChatID: f.chat, WorkflowID: "run-42", StepID: "publish", ActingIdentity: identity, Automation: "autopilot"},
		Node:    publishNode("autopilot/issue-12", 0),
	})
}

// An app run pushes with the bot's installation token for exactly this
// repository, and the ledger says the bot made the push.
func TestGitPublishActivity_AppIdentityPushesWithTheBotToken(t *testing.T) {
	t.Parallel()

	f := newPublishActivityFixture(t)
	src := &pushCredSource{}
	out, err := f.runAs(t, httpaction.IdentityApp, src)
	require.NoError(t, err)
	assert.True(t, out.Pushed, out.Refusal)

	require.Len(t, src.reqs, 1)
	assert.Equal(t, httpaction.IdentityApp, src.reqs[0].ActingIdentity)
	assert.Equal(t, "autopilot", src.reqs[0].Automation)
	assert.True(t, src.reqs[0].Mutates, "a push is a write")
	assert.Equal(t, map[string]any{"owner": "acme", "repo": "app"}, src.reqs[0].Params, "scoped to the one repository")
	assert.False(t, src.ownerAsked())

	var push gitpublish.PushRequest
	require.NoError(t, json.Unmarshal(f.router.sent[1].payload, &push))
	assert.Equal(t, "ghs_bot_push_token", push.Token)
	require.Len(t, f.ledger.pushes, 1)
	assert.Equal(t, core.IntegrationActorApp, f.ledger.pushes[0].Actor)
}

// Whatever stops the bot from minting, the run is refused with the reason: no
// owner credential is requested, no push command is sent, no ledger row is
// written, and the node does not crash.
func TestGitPublishActivity_AppIdentityFailsClosed(t *testing.T) {
	t.Parallel()

	for name, err := range map[string]error{
		"no app key":        &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "the control plane has no GitHub App key"},
		"app not installed": &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "the Reliant GitHub App is not installed on acme/app"},
		"permissions":       &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "the Reliant GitHub App has not been granted the permissions"},
		"transient":         &httpaction.CredentialError{Code: httpaction.CodeUnavailable, Message: "could not get a GitHub App token right now; try again"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPublishActivityFixture(t)
			src := &pushCredSource{botErr: err}
			out, runErr := f.runAs(t, httpaction.IdentityApp, src)
			require.NoError(t, runErr, "a refusal is an output, not a crashed node")
			assert.True(t, out.Refused)
			assert.False(t, out.Pushed)
			assert.Contains(t, out.Refusal, err.(*httpaction.CredentialError).Message)
			assert.False(t, src.ownerAsked(), "the owner's credential was never requested")
			assert.NotContains(t, f.router.commands(), gitpublish.CommandPush, "nothing was pushed")
			assert.Empty(t, f.ledger.pushes)
		})
	}
}

// An unrecognised identity is refused rather than read as the owner.
func TestGitPublishActivity_UnknownIdentityFailsClosed(t *testing.T) {
	t.Parallel()

	f := newPublishActivityFixture(t)
	src := &pushCredSource{}
	out, err := f.runAs(t, "admin", src)
	require.NoError(t, err)
	assert.True(t, out.Refused)
	assert.Empty(t, src.reqs, "no credential of any identity was requested")
	assert.NotContains(t, f.router.commands(), gitpublish.CommandPush)
}

// An owner run is untouched: the owner's token, recorded as the owner's.
func TestGitPublishActivity_OwnerIdentityStillPushesAsTheOwner(t *testing.T) {
	t.Parallel()

	f := newPublishActivityFixture(t)
	src := &pushCredSource{}
	out, err := f.runAs(t, "", src)
	require.NoError(t, err)
	assert.True(t, out.Pushed, out.Refusal)
	require.Len(t, src.reqs, 1)
	assert.Empty(t, src.reqs[0].ActingIdentity)
	var push gitpublish.PushRequest
	require.NoError(t, json.Unmarshal(f.router.sent[1].payload, &push))
	assert.Equal(t, "ghu_owner_push_token", push.Token)
	assert.Equal(t, core.IntegrationActorOwner, f.ledger.pushes[0].Actor)
}
