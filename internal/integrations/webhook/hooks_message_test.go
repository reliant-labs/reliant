// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/stretchr/testify/require"
)

// Exercise the real HTTP receiver -> intake -> event fire -> launch seed, not
// merely the seed formatter. Only persistence and the workflow launcher are fakes.
func TestInlineWebhookBodyReachesMessageWithoutCredentialsOrTenantOverride(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	env := newHooksEnv(t, core.WebhookConfig{}, "")
	env.trigger.NoMachine = true
	env.trigger.ProjectID = "project-owner"
	env.trigger.Message = "Summarise the delivery."
	repo := &messageEventRepo{fakeStore: env.store}
	launched := &messageLauncher{}
	intake := &messageIntake{repo: repo, launcher: launched}
	mux := http.NewServeMux()
	NewHooksReceiver(HooksOptions{Store: env.store, Intake: intake}).Register(func(p string, h http.Handler) { mux.Handle(p, h) })
	post := func(id, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/hooks/"+id+"?token=query-secret", strings.NewReader(`{"ref":"refs/heads/release","user_id":"attacker","project_id":"foreign"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Cookie", "session=cookie-secret")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	env.store.triggers["other"] = &core.Trigger{ID: "other", UserID: "other-user", Enabled: true, Kind: core.TriggerKindWebhook, Config: []byte(`{}`)}
	env.store.creds["other"] = &core.TriggerWebhookCredentials{TokenHash: triggers.HashWebhookToken("other-token")}
	require.Equal(t, http.StatusNotFound, post("other", hookToken).Code)
	require.Nil(t, launched.spec)
	require.Equal(t, http.StatusAccepted, post(env.trigger.ID, hookToken).Code)
	require.NotNil(t, launched.spec)
	require.Equal(t, env.trigger.UserID, launched.spec.OwnerUserID)
	require.Equal(t, env.trigger.ProjectID, launched.spec.ProjectID)
	require.Equal(t, env.trigger.UserID, repo.event.UserID)
	message := launched.spec.Messages[0].Content
	require.Contains(t, message, env.trigger.Message)
	require.Contains(t, message, `"ref":"refs/heads/release"`)
	require.Contains(t, message, "<trigger_event")
	require.Contains(t, logs.String(), "test: start deferred", "the error logging path was exercised")
	for _, secret := range []string{hookToken, "cookie-secret", "query-secret"} {
		require.NotContains(t, logs.String(), secret)
		require.NotContains(t, message, secret)
		require.NotContains(t, launched.spec.Messages[1].Content, secret)
	}
	require.NotContains(t, launched.spec.Messages[1].Content, "refs/heads/release")
}

type messageEventRepo struct {
	triggers.EventRepo
	*fakeStore
	event *core.TriggerEvent
}

func (r *messageEventRepo) GetTrigger(ctx context.Context, id string) (*core.Trigger, error) {
	return r.fakeStore.GetTrigger(ctx, id)
}
func (r *messageEventRepo) CreateTriggerEvent(_ context.Context, event *core.TriggerEvent) (bool, error) {
	r.event = event
	return true, nil
}
func (r *messageEventRepo) GetTriggerEventByDedupe(_ context.Context, kind core.TriggerEventKind, key string) (*core.TriggerEvent, error) {
	if r.event == nil || r.event.Kind != kind || r.event.DedupeKey != key {
		return nil, core.ErrTriggerEventNotFound
	}
	return r.event, nil
}

type messageIntake struct {
	repo     *messageEventRepo
	launcher *messageLauncher
}

func (i *messageIntake) Accept(ctx context.Context, trigger *core.Trigger, event triggers.InboundEvent, opts triggers.AcceptOptions) (*triggers.AcceptResult, error) {
	result, err := triggers.NewIntake(i.repo, messageStarter{}, "").Accept(ctx, trigger, event, opts)
	if err != nil {
		return nil, err
	}
	_, err = triggers.NewEventFirer(i.repo, i.launcher).Fire(ctx, triggers.EventFireInput{TriggerID: trigger.ID, Kind: event.Kind, DedupeKey: event.DedupeKey})
	return result, err
}

// A start outage leaves the real intake's pending row for the redriver;
// this test invokes that fire synchronously below.
type messageStarter struct{}

func (messageStarter) ExecuteWorkflow(context.Context, client.StartWorkflowOptions, any, ...any) (client.WorkflowRun, error) {
	return nil, errors.New("test: start deferred")
}

type messageLauncher struct{ spec *launch.Spec }

func (l *messageLauncher) Launch(_ context.Context, _ launch.Event, spec launch.Spec) (*launch.Result, error) {
	l.spec = &spec
	return &launch.Result{Chat: &core.Chat{ID: spec.NewChatID}}, nil
}
