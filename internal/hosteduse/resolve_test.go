package hosteduse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/stepsecrets"
)

var traderRef = stepsecrets.Ref{Environment: "trader/staging", Name: "TRADER_KEY"}

type resolveRequest struct {
	TicketID string            `json:"ticket_id"`
	Refs     []stepsecrets.Ref `json:"refs"`
}

// controller is a stand-in for the control plane's resolve route: it redeems
// the ticket the way the real one does (empty grant, digest of the refs) and
// answers with whatever respond returns.
type controller struct {
	t        *testing.T
	store    Store
	mu       sync.Mutex
	requests []resolveRequest
	auth     []string
	respond  func(n int, w http.ResponseWriter)
}

func (c *controller) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	require.NoError(c.t, json.NewDecoder(r.Body).Decode(&req))
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.auth = append(c.auth, r.Header.Get("Authorization"))
	n := len(c.requests)
	c.mu.Unlock()
	require.Equal(c.t, "/internal/secrets/resolve", r.URL.Path)
	meta, err := Redeem(r.Context(), c.store, RedeemRequest{req.TicketID, "", stepsecrets.RefsDigest(req.Refs)})
	if err != nil {
		http.Error(w, genericDenial, http.StatusForbidden)
		return
	}
	require.Equal(c.t, "trusted-owner", meta.OwnerUserID)
	c.respond(n, w)
}

func okValues(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{"values": []map[string]any{
		{"environment": "trader/staging", "name": "TRADER_KEY", "value": "stg_live/secret", "version": 3},
	}})
}

func resolveInActivity(t *testing.T, c *Client, name string, refs []stepsecrets.Ref) (map[stepsecrets.Ref]string, error) {
	t.Helper()
	var (
		got map[stepsecrets.Ref]string
		err error
	)
	actionTest(t, name, func(ctx context.Context) error {
		got, err = c.ResolveSecrets(ctx, activity.GetInfo(ctx).WorkflowExecution.ID, "deploy", refs)
		return nil
	})
	return got, err
}

func newController(t *testing.T, respond func(n int, w http.ResponseWriter)) (*Client, *controller, *memoryStore) {
	t.Helper()
	store := &memoryStore{}
	ctl := &controller{t: t, store: store, respond: respond}
	server := httptest.NewServer(ctl)
	t.Cleanup(server.Close)
	return newTestClient(t, store, server.URL), ctl, store
}

func TestResolveSecretsReturnsValuesForARedeemedTicketBoundToTheRefs(t *testing.T) {
	for _, activityType := range []string{"Action", "ExecuteRunStep"} {
		t.Run(activityType, func(t *testing.T) {
			c, ctl, store := newController(t, func(_ int, w http.ResponseWriter) { okValues(w) })
			got, err := resolveInActivity(t, c, activityType, []stepsecrets.Ref{traderRef, traderRef})
			require.NoError(t, err)
			assert.Equal(t, map[stepsecrets.Ref]string{traderRef: "stg_live/secret"}, got)

			require.Len(t, store.tickets, 1)
			tk := store.tickets[0]
			assert.Empty(t, tk.GrantID, "a resolve ticket carries no grant")
			assert.Equal(t, stepsecrets.RefsDigest([]stepsecrets.Ref{traderRef}), tk.RequestSHA256)
			assert.Equal(t, "deploy", tk.NodeID)
			assert.Contains(t, tk.ActivityID, "#resolve-", "the key is distinct from the HTTP send path's own")
			assert.NotNil(t, tk.RedeemedAt, "the controller redeemed it")
			assert.Equal(t, []string{"Bearer internal-token"}, ctl.auth)
			assert.Equal(t, []stepsecrets.Ref{traderRef}, ctl.requests[0].Refs, "refs are canonical and deduplicated")
		})
	}
}

func TestResolveSecretsRefusesActivitiesThatMayNotResolve(t *testing.T) {
	c, ctl, store := newController(t, func(_ int, w http.ResponseWriter) { okValues(w) })

	_, err := c.ResolveSecrets(context.Background(), "forged", "deploy", []stepsecrets.Ref{traderRef})
	require.Error(t, err, "not an activity")

	_, err = resolveInActivity(t, c, "ExecuteTools", []stepsecrets.Ref{traderRef})
	require.Error(t, err, "a tool activity may not resolve")

	var foreign error
	actionTest(t, "Action", func(ctx context.Context) error {
		_, foreign = c.ResolveSecrets(ctx, "foreign-run", "deploy", []stepsecrets.Ref{traderRef})
		return nil
	})
	require.Error(t, foreign, "another run's id")

	_, err = resolveInActivity(t, c, "Action", nil)
	require.Error(t, err, "no refs")
	_, err = resolveInActivity(t, c, "Action", []stepsecrets.Ref{{Environment: "bad env", Name: "X"}})
	require.Error(t, err, "malformed ref")

	assert.Empty(t, store.tickets)
	assert.Empty(t, ctl.requests)
}

func TestResolveSecretsRetriesAnUnavailableControllerWithAFreshTicketEachTry(t *testing.T) {
	c, ctl, store := newController(t, func(n int, w http.ResponseWriter) {
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		okValues(w)
	})
	got, err := resolveInActivity(t, c, "Action", []stepsecrets.Ref{traderRef})
	require.NoError(t, err)
	assert.Equal(t, "stg_live/secret", got[traderRef])

	require.Len(t, store.tickets, 3)
	seen := map[string]bool{}
	for _, tk := range store.tickets {
		assert.False(t, seen[tk.ID], "every try has its own ticket")
		seen[tk.ID] = true
	}
	assert.Len(t, ctl.requests, 3)
}

func TestResolveSecretsGivesUpAfterThreeTries(t *testing.T) {
	var calls atomic.Int32
	c, _, _ := newController(t, func(_ int, w http.ResponseWriter) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	})
	_, err := resolveInActivity(t, c, "Action", []stepsecrets.Ref{traderRef})
	require.Error(t, err)
	assert.EqualValues(t, 3, calls.Load())
}

func TestResolveSecretsShowsAControlPlaneRefusalAndDoesNotRetryIt(t *testing.T) {
	reason := "TRADER_KEY in trader/staging is not available to this workflow: add it to the secret's access list"
	var calls atomic.Int32
	c, _, _ := newController(t, func(_ int, w http.ResponseWriter) {
		calls.Add(1)
		http.Error(w, reason, http.StatusForbidden)
	})
	_, err := resolveInActivity(t, c, "ExecuteRunStep", []stepsecrets.Ref{traderRef})
	var denied *stepsecrets.DeniedError
	require.ErrorAs(t, err, &denied)
	assert.Equal(t, reason, denied.Reason)
	assert.EqualValues(t, 1, calls.Load())
}

func TestResolveSecretsTreatsAnUnshowableRefusalAsGeneric(t *testing.T) {
	for name, body := range map[string]string{
		"generic":   genericDenial,
		"multiline": "line one\nline two",
		"oversized": strings.Repeat("x", 4096),
		"empty":     "",
	} {
		t.Run(name, func(t *testing.T) {
			c, _, _ := newController(t, func(_ int, w http.ResponseWriter) { http.Error(w, body, http.StatusForbidden) })
			_, err := resolveInActivity(t, c, "Action", []stepsecrets.Ref{traderRef})
			require.Error(t, err)
			var denied *stepsecrets.DeniedError
			assert.NotErrorAs(t, err, &denied)
			assert.NotContains(t, err.Error(), "x"+"xxx")
			assert.Contains(t, err.Error(), "refused")
		})
	}
}

func TestResolveSecretsRejectsIncompleteOrUnexpectedResponses(t *testing.T) {
	for name, body := range map[string]string{
		"missing ref":   `{"values":[]}`,
		"unknown field": `{"values":[{"environment":"trader/staging","name":"TRADER_KEY","value":"v","version":1}],"extra":1}`,
		"trailing":      `{"values":[]} {}`,
		"not json":      `nope`,
	} {
		t.Run(name, func(t *testing.T) {
			c, _, _ := newController(t, func(_ int, w http.ResponseWriter) { _, _ = w.Write([]byte(body)) })
			_, err := resolveInActivity(t, c, "Action", []stepsecrets.Ref{traderRef})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "TRADER_KEY\":")
		})
	}
}

// Redeem accepts a resolve ticket's empty grant, and still refuses a grant
// that does not match the ticket.
func TestRedeemAcceptsAnEmptyGrantAndStillRejectsAMismatch(t *testing.T) {
	store := &memoryStore{}
	c := newTestClient(t, store, "https://controller.example")
	_ = c
	digest := stepsecrets.RefsDigest([]stepsecrets.Ref{traderRef})
	actionTest(t, "Action", func(ctx context.Context) error {
		for _, grant := range []string{"", "forged"} {
			ok, err := store.CreateHostedHTTPActionTicket(ctx, ticketFor(ctx, grant+"-t", grant, digest))
			require.NoError(t, err)
			require.True(t, ok)
		}
		_, err := Redeem(ctx, store, RedeemRequest{"-t", "x", digest})
		require.Error(t, err, "a grant that does not match")
		meta, err := Redeem(ctx, store, RedeemRequest{"-t", "", digest})
		require.NoError(t, err)
		assert.Equal(t, "trusted-owner", meta.OwnerUserID)
		_, err = Redeem(ctx, store, RedeemRequest{"-t", "", digest})
		require.Error(t, err, "single use")
		return nil
	})
}

func ticketFor(ctx context.Context, id, grant, digest string) core.HostedHTTPActionTicket {
	info := activity.GetInfo(ctx)
	return core.HostedHTTPActionTicket{
		ID: id, WorkflowID: info.WorkflowExecution.ID, TemporalRunID: info.WorkflowExecution.RunID,
		ActivityID: info.ActivityID + id, NodeID: "deploy", GrantID: grant, RequestSHA256: digest,
		ExpiresAt: time.Now().Add(time.Minute),
	}
}
