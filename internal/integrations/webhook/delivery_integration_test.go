package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/reliant-labs/reliant/internal/store/triggertest"
	"github.com/reliant-labs/reliant/internal/triggers"
)

type receiptStarts struct{ ids map[string]int }

func (s *receiptStarts) ExecuteWorkflow(_ context.Context, opts client.StartWorkflowOptions, _ any, _ ...any) (client.WorkflowRun, error) {
	s.ids[opts.ID]++
	return nil, nil
}

type receiptCatalog struct {
	*db.Repo
	reads  int
	before func(context.Context) error
}

func (s *receiptCatalog) PinProjectCatalog(ctx context.Context, scope db.CatalogPinScope) (string, error) {
	s.reads++
	if s.before != nil {
		if err := s.before(ctx); err != nil {
			return "", err
		}
	}
	return s.Repo.PinProjectCatalog(ctx, scope)
}

type receiptFixture struct {
	*storetest.Env
	now     time.Time
	starts  *receiptStarts
	catalog *receiptCatalog
	intake  *triggers.Intake
	relay   *Relay
	mux     *http.ServeMux
}

func newReceiptFixture(t *testing.T) *receiptFixture {
	t.Helper()
	f := &receiptFixture{Env: storetest.New(t), now: time.Now().UTC().Truncate(time.Millisecond),
		starts: &receiptStarts{ids: map[string]int{}}, mux: http.NewServeMux()}
	f.catalog = &receiptCatalog{Repo: f.Repo}
	f.intake = triggers.NewIntake(triggers.NewStores(f.Repo), f.starts, "test-queue").WithWorkflows(triggers.LaunchWorkflows{Repo: f.catalog})
	f.relay = NewRelay(NewStores(f.Repo), f.intake, db.TestConnections(t, f.Repo), nil)
	f.relay.now = func() time.Time { return f.now }
	registry := NewRegistry()
	require.NoError(t, registry.Register(NewGitHubProvider(gitHubTestSecret)))
	events := NewEventsReceiver(EventsOptions{Store: NewStores(f.Repo), Inbox: NewStores(f.Repo), Intake: f.intake, Registry: registry, PublicURL: "https://reliant.example.com"})
	events.now = f.relay.now
	events.Register(f.mux.Handle)
	hooks := NewHooksReceiver(HooksOptions{Store: NewStores(f.Repo), Inbox: NewStores(f.Repo), Intake: f.intake})
	hooks.now = f.relay.now
	hooks.Register(f.mux.Handle)
	require.NoError(t, f.Repo.CreateProject(t.Context(), &db.Project{ID: "receipt-project", UserID: "alice", Name: "Receipt project", Path: t.TempDir(), CreatedAt: f.now, UpdatedAt: f.now, LastActive: f.now}))
	return f
}

func (f *receiptFixture) trigger(t *testing.T, id string, kind core.TriggerKind, at time.Time, enabled bool) {
	t.Helper()
	cfg := json.RawMessage(`{}`)
	if kind == core.TriggerKindIntegration {
		cfg = json.RawMessage(`{"integration":"github","events":["issues.*"]}`)
	}
	require.NoError(t, triggertest.Create(t.Context(), f.Repo, &core.Trigger{ID: id, Name: id, UserID: "alice", ProjectID: "receipt-project", Kind: kind, Enabled: enabled,
		Workflow: "builtin://agent", Config: cfg, NoMachine: true, CreatedAt: at, UpdatedAt: at}))
	if kind == core.TriggerKindWebhook {
		require.NoError(t, store.SetTriggerWebhookTokenHash(t.Context(), f.Repo.ORM(), id, triggers.HashWebhookToken(hookToken)))
	}
}

func (f *receiptFixture) grant(t *testing.T) {
	t.Helper()
	require.NoError(t, store.ReplaceIntegrationAccess(t.Context(), f.Repo.ORM(), "alice", "github", "11", f.now,
		[]core.IntegrationAccessGrant{{AccountKey: "98765", ResourceKey: "123456"}}))
}

func (f *receiptFixture) github(event, id string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/integrations/github/events", strings.NewReader(string(body)))
	r.Header = gitHubRequest(event, id, body, signGitHub(gitHubTestSecret, body)).Header
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f *receiptFixture) hook(prefer, key string) *httptest.ResponseRecorder {
	return f.postHook(prefer, key, "", `{"body":"same"}`)
}

func (f *receiptFixture) postHook(prefer, key, proxyID, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/hooks/plain?password=query-secret", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+hookToken)
	r.Header.Set("Cookie", "session=cookie-secret")
	r.Header.Set("Prefer", prefer)
	r.Header.Set("Idempotency-Key", key)
	if proxyID != "" {
		r.Header.Set("X-Request-Id", proxyID)
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f *receiptFixture) count(t *testing.T, q string, want int) {
	t.Helper()
	var n int
	require.NoError(t, f.SQL.QueryRowContext(t.Context(), q).Scan(&n))
	require.Equal(t, want, n, q)
}

func (f *receiptFixture) drain(t *testing.T, n int) {
	t.Helper()
	for range n {
		worked, err := f.relay.ProcessOne(t.Context())
		require.NoError(t, err)
		require.True(t, worked)
	}
	worked, err := f.relay.ProcessOne(t.Context())
	require.NoError(t, err)
	require.False(t, worked)
}

func TestDeliveryInboxRetriesReadsAndFencesExpiredLease(t *testing.T) {
	f := newReceiptFixture(t)
	f.trigger(t, "alpha", core.TriggerKindIntegration, f.now, true)
	f.trigger(t, "zeta", core.TriggerKindIntegration, f.now, true)
	f.trigger(t, "future", core.TriggerKindIntegration, f.now.Add(time.Minute), true)
	f.trigger(t, "disabled", core.TriggerKindIntegration, f.now, false)
	f.grant(t)
	require.Equal(t, http.StatusOK, f.github("issues", "recover", gitHubFixture(t, "issues.opened")).Code)
	rows, err := NewStores(f.Repo).ClaimTriggerDeliveries(t.Context(), f.now, f.now.Add(time.Second), "crashed", 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	f.now = f.now.Add(2 * time.Second)
	f.catalog.before = func(context.Context) error {
		if f.catalog.reads == 1 {
			return context.DeadlineExceeded
		}
		return nil
	}
	worked, err := f.relay.ProcessOne(t.Context())
	require.NoError(t, err)
	require.True(t, worked)
	f.count(t, "SELECT count(*) FROM trigger_events", 1)
	f.count(t, "SELECT count(*) FROM trigger_delivery_inbox WHERE failures=1 AND completed_at IS NULL", 1)
	settled, err := NewStores(f.Repo).SettleTriggerDelivery(t.Context(), rows[0].ID, "crashed", core.TriggerDeliverySettlement{Now: f.now})
	require.NoError(t, err)
	require.False(t, settled)
	f.now = f.now.Add(2 * time.Second)
	f.drain(t, 1)
	f.count(t, "SELECT count(*) FROM trigger_events", 2)
	require.Len(t, f.starts.ids, 2, "partial processing can retry only the same fire ID")
	f.count(t, "SELECT count(*) FROM trigger_delivery_inbox WHERE completed_at IS NOT NULL", 1)
}

func TestDeliveryInboxRevokesBeforeOlderQueuedEventCanRoute(t *testing.T) {
	f := newReceiptFixture(t)
	f.trigger(t, "github", core.TriggerKindIntegration, f.now, true)
	f.grant(t)
	require.Equal(t, http.StatusOK, f.github("issues", "older", gitHubFixture(t, "issues.opened")).Code)
	require.Equal(t, http.StatusOK, f.github("installation", "uninstall", []byte(`{"action":"deleted","installation":{"id":98765}}`)).Code)
	f.count(t, "SELECT count(*) FROM integration_event_access", 0)
	f.drain(t, 2)
	f.count(t, "SELECT count(*) FROM trigger_events", 0)
}

func TestDeliveryInboxReplaysAfterWritingTheEvent(t *testing.T) {
	f := newReceiptFixture(t)
	f.trigger(t, "plain", core.TriggerKindWebhook, f.now, true)
	require.Equal(t, http.StatusAccepted, f.hook("respond-async", "crash-after-event").Code)
	rows, err := NewStores(f.Repo).ClaimTriggerDeliveries(t.Context(), f.now, f.now.Add(time.Minute), "dead-worker", 1)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	res, err := f.relay.process(t.Context(), rows[0])
	require.NoError(t, err)
	require.NotEmpty(t, res.EventID)
	// Simulate the worker crashing between the event transaction and its
	// receipt checkpoint. Even an unreadable current catalog cannot cause
	// the replay to replace the accepted event or evaluate a new key.
	f.now = f.now.Add(2 * time.Minute)
	f.catalog.before = func(context.Context) error {
		t.Fatal("replay re-read the catalog after accepting an event")
		return nil
	}
	f.drain(t, 1)
	f.count(t, "SELECT count(*) FROM trigger_events", 1)
	f.count(t, "SELECT count(*) FROM trigger_delivery_inbox WHERE completed_at IS NOT NULL", 1)
	assert.Len(t, f.starts.ids, 1)
}

func TestDeliveryInboxIgnoresProxyRequestIDsForDedupe(t *testing.T) {
	f := newReceiptFixture(t)
	f.trigger(t, "plain", core.TriggerKindWebhook, f.now, true)

	const sameBody = `{"body":"same"}`
	legacy := func(proxyID string) (reply struct {
		EventID   string `json:"event_id"`
		Duplicate bool   `json:"duplicate"`
	}) {
		t.Helper()
		response := f.postHook("", "", proxyID, sameBody)
		require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &reply))
		require.NotEmpty(t, reply.EventID)
		return reply
	}
	first, again := legacy("proxy-first"), legacy("proxy-retry")
	assert.False(t, first.Duplicate)
	assert.True(t, again.Duplicate)
	assert.Equal(t, first.EventID, again.EventID)
	f.count(t, "SELECT count(*) FROM trigger_delivery_inbox", 2)
	f.count(t, "SELECT count(*) FROM trigger_events", 1)

	async := func(key, proxyID, body string) string {
		t.Helper()
		response := f.postHook("respond-async", key, proxyID, body)
		require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
		assert.Equal(t, "respond-async", response.Header().Get("Preference-Applied"))
		var reply map[string]string
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &reply))
		assert.Equal(t, "pending", reply["status"])
		assert.NotContains(t, reply, "event_id")
		require.NotEmpty(t, reply["delivery_id"])
		return reply["delivery_id"]
	}
	// Reusing the same proxy ID catches accidental use as the inbox key.
	a, b := async("", "proxy-same", sameBody), async("", "proxy-same", sameBody)
	require.NotEqual(t, a, b)
	f.count(t, "SELECT count(*) FROM trigger_delivery_inbox", 4)
	f.drain(t, 2)
	for _, id := range []string{a, b} {
		event, err := f.Repo.GetTriggerEventByDedupe(t.Context(), core.TriggerEventKindWebhook, "plain:receipt:"+id)
		require.NoError(t, err)
		assert.Equal(t, 1, f.starts.ids[triggers.EventFireWorkflowID(event.ID)])
	}
	f.count(t, "SELECT count(*) FROM trigger_events", 3)

	keyed := async("event-key", "proxy-keyed", `{"body":"first"}`)
	assert.Equal(t, keyed, async("event-key", "proxy-keyed-retry", `{"body":"changed"}`))
	f.count(t, "SELECT count(*) FROM trigger_delivery_inbox", 5)
	f.drain(t, 1)
	f.count(t, "SELECT count(*) FROM trigger_events", 4)
	event, err := f.Repo.GetTriggerEventByDedupe(t.Context(), core.TriggerEventKindWebhook, "plain:key:event-key")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"body": "first"}, event.Payload["body"], "the retry cannot replace the original payload")
	assert.Equal(t, 1, f.starts.ids[triggers.EventFireWorkflowID(event.ID)])
}

func TestDeliveryInboxLegacyResponseAndCallerRetry(t *testing.T) {
	f := newReceiptFixture(t)
	f.trigger(t, "plain", core.TriggerKindWebhook, f.now, true)
	f.catalog.before = func(context.Context) error { return errors.New("catalog unavailable") }
	bad := f.hook("", "retry")
	require.Equal(t, http.StatusServiceUnavailable, bad.Code, bad.Body.String())
	f.count(t, "SELECT count(*) FROM trigger_delivery_inbox WHERE failure_code='caller_notified_failure' AND dead_at IS NOT NULL", 1)
	f.now = f.now.Add(deliveryLease)
	f.drain(t, 0)
	f.catalog.before = nil
	ok := f.hook("", "retry")
	require.Equal(t, http.StatusAccepted, ok.Code, ok.Body.String())
	var answer, duplicate map[string]any
	require.NoError(t, json.Unmarshal(ok.Body.Bytes(), &answer))
	again := f.hook("", "retry")
	require.Equal(t, http.StatusAccepted, again.Code, again.Body.String())
	require.NoError(t, json.Unmarshal(again.Body.Bytes(), &duplicate))
	assert.NotEmpty(t, answer["event_id"])
	assert.Equal(t, answer["event_id"], duplicate["event_id"])
	assert.Equal(t, false, answer["duplicate"])
	assert.Equal(t, true, duplicate["duplicate"])
	assert.NotContains(t, answer, "delivery_id")
	for range 2 {
		require.Equal(t, http.StatusAccepted, f.hook("", "").Code)
	}
	f.count(t, "SELECT count(*) FROM trigger_delivery_inbox", 3)
	f.count(t, "SELECT count(*) FROM trigger_events", 2)
}
