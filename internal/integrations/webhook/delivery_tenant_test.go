package webhook

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/triggertest"
	"github.com/reliant-labs/reliant/internal/triggers"
)

func TestDeliveryInboxPinsAccountWorkflowsPerOwner(t *testing.T) {
	f := newReceiptFixture(t)
	ctx := t.Context()
	// Both owners reach the same project/daemon catalog through their own
	// GitHub grants. Only the account workflow behind the same slug differs.
	const slug = "receipt-isolation"
	declaration := "receipt"
	definitions := map[string]string{}
	for _, tc := range []struct{ owner, event string }{{"alice", "issues.opened"}, {"bob", "issues.closed"}} {
		definition := fmt.Sprintf(`name: %s
triggers:
  - name: receipt
    integration:
      integration: github
      events: [issues.*]
    filter: trigger.payload.event == '%s'
entry: [hold]
nodes:
  - id: hold
    type: approval
    args: {title: "%s's workflow"}
`, slug, tc.event, tc.owner)
		definitions[tc.owner] = definition
		require.NoError(t, f.Repo.CreateWorkflowDraft(ctx, &db.WorkflowDraft{
			ID: uuid.NewString(), UserID: tc.owner, Name: slug, Slug: slug,
			Definition: definition, Status: db.WorkflowDraftStatusComplete, Version: 1,
			CreatedAt: f.now, UpdatedAt: f.now,
		}))
		require.NoError(t, store.ReplaceIntegrationAccess(ctx, f.Repo.ORM(), tc.owner, "github", tc.owner, f.now,
			[]core.IntegrationAccessGrant{{AccountKey: "98765", ResourceKey: "123456"}}))
	}
	// Alice comes first in route order: accidentally sharing her pin would
	// let Bob's trigger start. Two Alice rows also prove reuse within an owner.
	for _, tc := range []struct{ id, owner string }{{"a-first", "alice"}, {"a-second", "alice"}, {"b-foreign", "bob"}} {
		require.NoError(t, triggertest.Create(ctx, f.Repo, &core.Trigger{
			ID: tc.id, Name: tc.id, UserID: tc.owner, ProjectID: "receipt-project",
			Kind: core.TriggerKindIntegration, Enabled: true, NoMachine: true,
			Workflow: slug, WorkflowTrigger: &declaration,
			Config:    json.RawMessage(`{"integration":"github","events":["issues.*"]}`),
			CreatedAt: f.now, UpdatedAt: f.now,
		}))
	}
	const delivery = "owner-catalog"
	require.Equal(t, http.StatusOK, f.github("issues", delivery, gitHubFixture(t, "issues.opened")).Code)
	f.drain(t, 1)
	f.count(t, "SELECT count(*) FROM trigger_events", 3)
	wantStarts := map[string]int{}
	for _, tc := range []struct {
		id, owner string
		outcome   core.TriggerEventOutcome
	}{
		{"a-first", "alice", core.TriggerEventPending},
		{"a-second", "alice", core.TriggerEventPending},
		{"b-foreign", "bob", core.TriggerEventSkipped},
	} {
		ev, err := f.Repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindIntegration, tc.id+":"+delivery)
		require.NoError(t, err)
		require.Equal(t, tc.outcome, ev.Outcome, ev.OutcomeDetail)
		assert.Equal(t, tc.owner, ev.UserID)
		require.NotNil(t, ev.Definition)
		assert.NotEmpty(t, ev.Definition.CatalogPin)
		assert.Equal(t, config.DefinitionHash([]byte(definitions[tc.owner])), ev.Definition.ContentHash)
		if tc.outcome == core.TriggerEventPending {
			wantStarts[triggers.EventFireWorkflowID(ev.ID)] = 1
		} else {
			assert.Contains(t, ev.OutcomeDetail, "issues.closed")
		}
	}
	assert.Equal(t, wantStarts, f.starts.ids, "the foreign owner's filter cannot inherit Alice's admit decision")
	assert.Equal(t, 2, f.catalog.reads, "two owner pins, not one per trigger or one shared across owners")
}

func TestDeliveryInboxReplayCannotWidenSignedTwilioRoutes(t *testing.T) {
	f := newReceiptFixture(t)
	ctx := t.Context()
	secrets := &fakeSecrets{tokens: map[string]string{}, owners: map[string]string{}}
	registry := NewRegistry()
	require.NoError(t, registry.Register(NewTwilioProvider()))
	receiver := NewEventsReceiver(EventsOptions{
		Store: NewStores(f.Repo), Inbox: NewStores(f.Repo), Intake: f.intake, Registry: registry, PublicURL: twPublic, ConnectionSecrets: secrets,
	})
	receiver.now = f.relay.now
	mux := http.NewServeMux()
	receiver.Register(mux.Handle)

	for _, tc := range []struct{ trigger, owner, connection, token string }{
		{"alice-live", "alice", "alice-connection", twTokenA},
		{"alice-disabled-after-receipt", "alice", "alice-connection", twTokenA},
		{"bob-unverified", "bob", "bob-connection", twTokenB},
	} {
		if _, exists := secrets.tokens[tc.connection]; !exists {
			account := twAccountA // same account and live route, different credential
			require.NoError(t, db.TestConnections(t, f.Repo).CreateConnection(ctx, &core.Connection{
				ID: tc.connection, OwnerKind: core.ConnectionOwnerUser, UserID: tc.owner,
				IntegrationID: TwilioProviderID, AuthKind: core.ConnectionAuthBasic, Name: tc.connection,
				Status: core.ConnectionStatusActive, ExternalAccountID: &account,
			}, nil, core.ConnectionEvent{UserID: tc.owner, Kind: core.ConnectionEventCreated, Actor: "user:" + tc.owner}))
			secrets.tokens[tc.connection], secrets.owners[tc.connection] = tc.token, tc.owner
		}
		connID := tc.connection
		require.NoError(t, triggertest.Create(ctx, f.Repo, &core.Trigger{
			ID: tc.trigger, Name: tc.trigger, UserID: tc.owner, ProjectID: "receipt-project",
			Kind: core.TriggerKindIntegration, Enabled: true, NoMachine: true, ConnectionID: &connID,
			Workflow: "builtin://agent", Config: json.RawMessage(`{"integration":"twilio","events":["message.received"]}`),
			CreatedAt: f.now, UpdatedAt: f.now,
		}))
	}
	const sid = "SMtenant-isolation"
	form := inboundSMS(twAccountA, sid, "+15551230000", "+15559870000", "test")
	post := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/integrations/twilio/events", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Twilio-Signature", twilioSign(token, twEvents, form))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, http.StatusUnauthorized, post("neither-connection-token").Code)
	f.count(t, "SELECT count(*) FROM trigger_delivery_inbox", 0)
	secrets.asked = nil
	ack := post(twTokenA)
	require.Equal(t, http.StatusOK, ack.Code, ack.Body.String())
	assert.ElementsMatch(t, []string{"alice/alice-connection", "bob/bob-connection"}, secrets.asked,
		"both live connections were candidates, but only Alice's token verified")
	assert.Equal(t, "text/xml", ack.Header().Get("Content-Type"))
	assert.Equal(t, string(twilioEmptyTwiML), ack.Body.String())
	f.count(t, "SELECT count(*) FROM trigger_events", 0)

	// Verify the authenticated routing evidence survived serialization, not
	// just an in-memory receiver call. The subsequent relay loads this row.
	var payload []byte
	require.NoError(t, f.SQL.QueryRowContext(ctx, "SELECT payload FROM trigger_delivery_inbox").Scan(&payload))
	var envelope integrationDelivery
	require.NoError(t, json.Unmarshal(payload, &envelope))
	assert.ElementsMatch(t, []verifiedRoute{
		{TriggerID: "alice-live", UserID: "alice", ConnectionID: "alice-connection"},
		{TriggerID: "alice-disabled-after-receipt", UserID: "alice", ConnectionID: "alice-connection"},
	}, envelope.Verified)
	assert.NotContains(t, string(payload), twilioSign(twTokenA, twEvents, form))
	calls := len(secrets.asked)
	disabled, err := store.GetTrigger(ctx, f.Repo.ORM(), "alice-disabled-after-receipt")
	require.NoError(t, err)
	disabled.Enabled = false
	require.NoError(t, triggertest.Update(ctx, f.Repo, disabled))
	f.drain(t, 1)
	assert.Len(t, secrets.asked, calls, "relay uses receipt evidence without reopening connection secrets")
	f.count(t, "SELECT count(*) FROM trigger_events", 1)
	ev, err := f.Repo.GetTriggerEventByDedupe(ctx, core.TriggerEventKindIntegration, "alice-live:"+sid)
	require.NoError(t, err)
	assert.Equal(t, "alice", ev.UserID)
	assert.Equal(t, map[string]int{triggers.EventFireWorkflowID(ev.ID): 1}, f.starts.ids)
}
