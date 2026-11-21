// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// webhookTriggerService plays the TriggerService rpcs behind activate's
// webhook output and `trigger rotate`.
type webhookTriggerService struct {
	reliantv1connect.UnimplementedTriggerServiceHandler
	mu      sync.Mutex
	trigger *reliantv1.Trigger
	managed bool
	enabled []*reliantv1.EnableWorkflowRequest
	rotated []string
}

func (f *webhookTriggerService) GetTrigger(_ context.Context, r *connect.Request[reliantv1.GetTriggerRequest]) (*connect.Response[reliantv1.GetTriggerResponse], error) {
	if r.Msg.GetId() != f.trigger.GetId() {
		return nil, connect.NewError(connect.CodeNotFound, assertNotFound{})
	}
	return connect.NewResponse(&reliantv1.GetTriggerResponse{Trigger: f.trigger}), nil
}

func (f *webhookTriggerService) ListTriggers(context.Context, *connect.Request[reliantv1.ListTriggersRequest]) (*connect.Response[reliantv1.ListTriggersResponse], error) {
	return connect.NewResponse(&reliantv1.ListTriggersResponse{Triggers: []*reliantv1.Trigger{f.trigger}}), nil
}

func (f *webhookTriggerService) EnableWorkflow(_ context.Context, r *connect.Request[reliantv1.EnableWorkflowRequest]) (*connect.Response[reliantv1.EnableWorkflowResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled = append(f.enabled, r.Msg)
	return connect.NewResponse(&reliantv1.EnableWorkflowResponse{Enabled: true, Results: []*reliantv1.WorkflowTriggerResult{{
		Trigger:    "deploy-finished",
		Outcome:    reliantv1.WorkflowTriggerOutcome_WORKFLOW_TRIGGER_OUTCOME_ENABLED,
		Activation: f.trigger,
		Webhook:    &reliantv1.WebhookCredential{Token: "tok-once", Url: "https://api.example/hooks/hook-1/tok-once"},
		Warnings:   []string{"no account of this org can run its model yet"},
	}}}), nil
}

func (f *webhookTriggerService) RotateWebhookToken(_ context.Context, r *connect.Request[reliantv1.RotateWebhookTokenRequest]) (*connect.Response[reliantv1.RotateWebhookTokenResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rotated = append(f.rotated, r.Msg.GetId())
	resp := &reliantv1.RotateWebhookTokenResponse{Trigger: f.trigger}
	if !f.managed {
		resp.Webhook = &reliantv1.WebhookCredential{Token: "tok-new", Url: "https://api.example/hooks/hook-1/tok-new"}
	}
	return connect.NewResponse(resp), nil
}

func newWebhookTriggerServer(t *testing.T) *webhookTriggerService {
	t.Helper()
	isolateCLI(t)
	url := "https://api.example/hooks/hook-1"
	fake := &webhookTriggerService{trigger: &reliantv1.Trigger{
		Id: "hook-1", Name: "deploys · deploy-finished", ProjectId: "project-a", Enabled: true, WebhookUrl: &url,
		Source: &reliantv1.Trigger_Webhook{Webhook: &reliantv1.WebhookSource{}},
	}}
	mux := http.NewServeMux()
	p, h := reliantv1connect.NewTriggerServiceHandler(fake)
	mux.Handle(p, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	loginFor(t, srv.URL, fakeLoginToken)
	t.Setenv(envServerURL, srv.URL)
	t.Setenv(daemonpolicy.AgentShellEnv, "")
	return fake
}

// activate prints the webhook's URL and token, the one time the server
// returns the token, and the enable's warnings; the signing secret goes to
// the server from a file, as the trigger's override.
func TestTriggerActivatePrintsTheWebhookTokenAndSendsTheSigningSecret(t *testing.T) {
	fake := newWebhookTriggerServer(t)
	secretFile := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(secretFile, []byte("s3cret\n"), 0o600))

	stdout, stderr, err := executeRootSplit(t, "trigger", "activate", "--workflow", "deploys", "--project-id", "project-a",
		"--no-machine", "--hmac-secret-file", "deploy-finished="+secretFile)
	require.NoError(t, err, stderr)

	assert.Contains(t, stdout, "Token:    tok-once")
	assert.Contains(t, stdout, `POST to:  https://api.example/hooks/hook-1 with the header "Authorization: Bearer <token>"`)
	assert.Contains(t, stdout, "or to:    https://api.example/hooks/hook-1/tok-once")
	assert.Contains(t, stdout, "reliant trigger rotate hook-1")
	assert.Contains(t, stdout, "Warning (deploy-finished): no account of this org can run its model yet")

	require.Len(t, fake.enabled, 1)
	overrides := fake.enabled[0].GetOverrides()
	require.Len(t, overrides, 1)
	assert.Equal(t, "deploy-finished", overrides[0].GetTrigger())
	assert.Equal(t, "s3cret", overrides[0].GetWebhookHmacSecret(), "one trailing newline is dropped")
	assert.NotContains(t, stdout, "s3cret", "the secret is never echoed")
}

func TestTriggerActivateReadsTheSigningSecretFromStdin(t *testing.T) {
	fake := newWebhookTriggerServer(t)
	root := NewRootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader("from-stdin"))
	root.SetArgs([]string{"trigger", "activate", "--workflow", "deploys", "--project-id", "project-a", "--no-machine", "--hmac-secret-file", "deploy-finished=-"})
	require.NoError(t, root.Execute(), out.String())
	require.Len(t, fake.enabled, 1)
	assert.Equal(t, "from-stdin", fake.enabled[0].GetOverrides()[0].GetWebhookHmacSecret())
}

func TestReadHMACSecretFilesRefusesWhatCannotBeASecret(t *testing.T) {
	_, err := readHMACSecretFiles([]string{"nofile"}, nil)
	assert.ErrorContains(t, err, "must be trigger=path")
	_, err = readHMACSecretFiles([]string{"a=-", "b=-"}, strings.NewReader("x"))
	assert.ErrorContains(t, err, "only one secret can be read from standard input")
	empty := filepath.Join(t.TempDir(), "empty")
	require.NoError(t, os.WriteFile(empty, []byte("\n"), 0o600))
	_, err = readHMACSecretFiles([]string{"a=" + empty}, nil)
	assert.ErrorContains(t, err, "the secret is empty")
	full := filepath.Join(t.TempDir(), "full")
	require.NoError(t, os.WriteFile(full, []byte("k"), 0o600))
	_, err = readHMACSecretFiles([]string{"a=" + full, "a=" + full}, nil)
	assert.ErrorContains(t, err, "twice")
	_, err = readHMACSecretFiles([]string{"a=/does/not/exist"}, nil)
	assert.ErrorContains(t, err, "reading the secret")
}

// rotate takes a name, calls RotateWebhookToken by id, and prints the new
// token once; a managed webhook has none to print. An agent's shell is
// refused, as for every command that opens a way in.
func TestTriggerRotateDialsTheServer(t *testing.T) {
	fake := newWebhookTriggerServer(t)

	stdout, stderr, err := executeRootSplit(t, "trigger", "rotate", "deploys · deploy-finished")
	require.NoError(t, err, stderr)
	assert.Equal(t, []string{"hook-1"}, fake.rotated, "a name resolves to its id")
	assert.Contains(t, stdout, "the old token no longer works")
	assert.Contains(t, stdout, "Token:    tok-new")
	assert.Contains(t, stdout, "or to:    https://api.example/hooks/hook-1/tok-new")

	fake.managed = true
	stdout, stderr, err = executeRootSplit(t, "trigger", "rotate", "hook-1")
	require.NoError(t, err, stderr)
	assert.Contains(t, stdout, "the new token went to its sender")
	assert.NotContains(t, stdout, "Token:")

	t.Setenv(daemonpolicy.AgentShellEnv, "1")
	_, _, err = executeRootSplit(t, "trigger", "rotate", "hook-1")
	assert.ErrorIs(t, err, errAgentCannotEnable)
	assert.Len(t, fake.rotated, 2, "a refused rotate reached the server")
}

func TestTriggerGetShowsTheWebhookURL(t *testing.T) {
	newWebhookTriggerServer(t)
	stdout, stderr, err := executeRootSplit(t, "trigger", "get", "hook-1")
	require.NoError(t, err, stderr)
	assert.Contains(t, stdout, "https://api.example/hooks/hook-1 (token: shown once at enable; `reliant trigger rotate` issues a new one)")
}
