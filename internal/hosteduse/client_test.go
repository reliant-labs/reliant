package hosteduse

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/reliant/internal/controlplane/cptransport"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/hostedhttp"
)

type memoryStore struct {
	mu      sync.Mutex
	tickets []core.HostedHTTPActionTicket
}

func (s *memoryStore) CreateHostedHTTPActionTicket(_ context.Context, t core.HostedHTTPActionTicket) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.tickets {
		if old.WorkflowID == t.WorkflowID && old.TemporalRunID == t.TemporalRunID && old.ActivityID == t.ActivityID {
			return false, nil
		}
	}
	t.OwnerUserID = "trusted-owner"
	t.ProjectID = "trusted-project"
	t.WorkflowName = "trader-bot"
	s.tickets = append(s.tickets, t)
	return true, nil
}
func (s *memoryStore) RedeemHostedHTTPActionTicket(_ context.Context, id, grant, digest string) (*core.HostedHTTPActionTicket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tickets {
		t := &s.tickets[i]
		if t.ID == id && t.GrantID == grant && t.RequestSHA256 == digest && t.RedeemedAt == nil && t.ExpiresAt.After(time.Now()) {
			now := time.Now()
			t.RedeemedAt = &now
			out := *t
			return &out, nil
		}
	}
	return nil, nil
}

func actionTest(t *testing.T, name string, fn func(context.Context) error) {
	t.Helper()
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
	_, err := env.ExecuteActivity(name)
	require.NoError(t, err)
}
func newTestClient(t *testing.T, store Store, base string) *Client {
	t.Helper()
	c, err := New(Deps{Store: store, BaseURL: base, Sign: func() (string, error) { return "internal-token", nil }})
	require.NoError(t, err)
	return c.(*Client)
}

var sampleRequest = hostedhttp.Request{URL: "https://trader.example/rpc", Method: "POST", Body: map[string]any{"amount": 1}}

func TestIssueRequiresRealFixedActivity(t *testing.T) {
	store := &memoryStore{}
	c := newTestClient(t, store, "https://controller.example")
	_, err := c.Issue(context.Background(), "forged", "node", "grant", sampleRequest)
	require.Error(t, err)
	actionTest(t, "ExecuteTools", func(ctx context.Context) error {
		_, err := c.Issue(ctx, activity.GetInfo(ctx).WorkflowExecution.ID, "node", "grant", sampleRequest)
		require.Error(t, err)
		return nil
	})
	actionTest(t, "Action", func(ctx context.Context) error {
		_, err := c.Issue(ctx, "foreign-run", "node", "grant", sampleRequest)
		require.Error(t, err)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, err = c.Issue(cancelled, activity.GetInfo(ctx).WorkflowExecution.ID, "node", "grant", sampleRequest)
		require.Error(t, err)
		return nil
	})
	require.Empty(t, store.tickets)
}

func TestIssueBoundToActivityAndDeadline(t *testing.T) {
	store := &memoryStore{}
	c := newTestClient(t, store, "https://controller.example")
	actionTest(t, "Action", func(ctx context.Context) error {
		info := activity.GetInfo(ctx)
		deadline := time.Now().Add(10 * time.Second)
		ctx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		id, err := c.Issue(ctx, info.WorkflowExecution.ID, "fixed-node", "grant", sampleRequest)
		require.NoError(t, err)
		require.NotEmpty(t, id)
		saved := store.tickets[0]
		digest, err := hostedhttp.RequestDigest(sampleRequest)
		require.NoError(t, err)
		require.Equal(t, info.WorkflowExecution.RunID, saved.TemporalRunID)
		require.Equal(t, info.ActivityID, saved.ActivityID)
		require.Equal(t, digest, saved.RequestSHA256)
		require.Equal(t, deadline, saved.ExpiresAt)
		_, err = c.Issue(ctx, info.WorkflowExecution.ID, "fixed-node", "grant", sampleRequest)
		require.Error(t, err)
		return nil
	})
}

func TestExecuteNeverResendsUncertainMutation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "Bearer internal-token", r.Header.Get("Authorization"))
		http.Error(w, "synthetic-sensitive-error", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	store := &memoryStore{}
	c := newTestClient(t, store, server.URL)
	actionTest(t, "Action", func(ctx context.Context) error {
		run := activity.GetInfo(ctx).WorkflowExecution.ID
		_, err := c.ExecuteAction(ctx, run, "node", "grant", sampleRequest)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "synthetic-sensitive-error")
		_, err = c.ExecuteAction(ctx, run, "node", "grant", sampleRequest)
		require.Error(t, err)
		return nil
	})
	require.EqualValues(t, 1, calls.Load())
}

func TestExecuteRedirectRefusedAndNativeResult(t *testing.T) {
	for _, redirect := range []bool{true, false} {
		t.Run(map[bool]string{true: "redirect", false: "result"}[redirect], func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if redirect {
					http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
					return
				}
				_, _ = w.Write([]byte(`{"content":"safe","data":{"json":{"n":9007199254740993}},"status_code":200,"is_error":false,"retryable":true}`))
			}))
			defer server.Close()
			c := newTestClient(t, &memoryStore{}, server.URL)
			actionTest(t, "Action", func(ctx context.Context) error {
				out, err := c.ExecuteAction(ctx, activity.GetInfo(ctx).WorkflowExecution.ID, "node", "grant", sampleRequest)
				if redirect {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
					require.Equal(t, "safe", out.Content)
					require.False(t, out.Retryable)
					_, conversionErr := structpb.NewStruct(out.Data)
					require.NoError(t, conversionErr, "native result numbers must be compatible with ActionOutput")
				}
				return nil
			})
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestRedeemMismatchDoesNotConsumeAndConcurrentSingleUse(t *testing.T) {
	store := &memoryStore{}
	c := newTestClient(t, store, "https://controller.example")
	actionTest(t, "Action", func(ctx context.Context) error {
		id, err := c.Issue(ctx, activity.GetInfo(ctx).WorkflowExecution.ID, "fixed-node", "grant", sampleRequest)
		require.NoError(t, err)
		digest, _ := hostedhttp.RequestDigest(sampleRequest)
		_, err = Redeem(ctx, store, RedeemRequest{id, "foreign", digest})
		require.Error(t, err)
		_, err = Redeem(ctx, store, RedeemRequest{id, "grant", strings.Repeat("0", 64)})
		require.Error(t, err)
		var wg sync.WaitGroup
		var successes atomic.Int32
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out, err := Redeem(ctx, store, RedeemRequest{id, "grant", digest})
				if err == nil {
					successes.Add(1)
					require.Equal(t, "trusted-owner", out.OwnerUserID)
					require.Equal(t, "trader-bot", out.WorkflowName)
				}
			}()
		}
		wg.Wait()
		require.EqualValues(t, 1, successes.Load())
		return nil
	})
}

func TestNewFailsClosed(t *testing.T) {
	for _, base := range []string{"", "http://external.example", "http://10.0.0.8:8090", "http://admin-server:8090", "https://user:pass@controller.example", "https://controller.example/path", "https://controller.example?query=1"} {
		_, err := New(Deps{Store: &memoryStore{}, BaseURL: base, Sign: func() (string, error) { return "", errors.New("no token") }})
		require.Error(t, err, base)
	}
	_, err := New(Deps{Store: &memoryStore{}, BaseURL: "http://external.example", Sign: func() (string, error) { return "t", nil }})
	require.ErrorIs(t, err, cptransport.ErrInsecure, "an operator-misconfigured external http URL is refused")
}

// Prod's worker reaches control-plane's admin-server over its in-cluster
// Service in plaintext, as every other control-plane call does; dev reaches it
// on loopback. Both build a client.
func TestNewAcceptsInClusterAndLoopbackHTTP(t *testing.T) {
	for _, base := range []string{
		"http://admin-server.control-plane-prod.svc.cluster.local:8090",
		"http://admin-server.control-plane-e2e.svc:8090",
		"http://127.0.0.1:8090",
		"http://localhost:8090/",
		"https://controller.example",
	} {
		_, err := New(Deps{Store: &memoryStore{}, BaseURL: base, Sign: func() (string, error) { return "t", nil }})
		require.NoError(t, err, base)
	}
}
