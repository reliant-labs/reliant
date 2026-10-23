// Copyright (c) 2025 Reliant Labs
package workersetup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/llm/accountrouter"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/workflow/capacitywake"
)

type usageDoer func(*http.Request) (*http.Response, error)

func (d usageDoer) Do(r *http.Request) (*http.Response, error) { return d(r) }

type recordedSignals struct{ workflows []string }

func (s *recordedSignals) SignalWorkflow(_ context.Context, workflowID, _, name string, _ interface{}) error {
	if name == capacitywake.SignalName {
		s.workflows = append(s.workflows, workflowID)
	}
	return nil
}

// The owner's report, end to end over the real store, the real router and the
// real usage refresher. An account hit its 5-hour limit and the provider
// benched it until the reset. The user raised the quota, and Settings'
// refresh read the account with room. Before the fix, that refresh restored
// the bench, so the router kept refusing the account Settings showed as fine
// until the reset, or until a disconnect and reconnect.
//
// Now the refresh lifts the bench in the store the router reads (the one
// table every worker and API replica shares, with no per-process cache). The
// router's next assignment for the thread pinned to that account is the
// account itself. The user's chat whose thread waits on the limit is sent a
// capacity wake.
func TestUsageRefreshThatShowsCapacityUnbenchesTheAccountForTheRouterAndWakesTheChat(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	drivers.InitializeAPIKeyProvider(repo)
	prevClient := drivers.AccountAvailabilityClient
	drivers.AccountAvailabilityClient = func(_ context.Context, id models.DriverID, _ models.DriverConfig) (registry.Client, error) {
		return noCatalog{name: string(id)}, nil
	}
	t.Cleanup(func() { drivers.AccountAvailabilityClient = prevClient })

	user := "user-" + uuid.NewString()
	alice, _, err := repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: user, Provider: "claude", AuthKind: core.AuthKindOAuth,
		IdentityKey: core.ClaudeIdentityKey("alice", "org-alice"), Email: "alice@acme.com",
		ExternalAccountID: "alice", OrganizationID: "org-alice",
		Secrets:   core.AccountSecrets{Secret: "access-alice", Refresh: "refresh-alice"},
		ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)

	// A chat whose run is live and whose sub-agent waits on the limit.
	now := time.Now().UTC()
	projectID, chatID := "project-"+uuid.NewString(), uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{ID: projectID, UserID: user, Name: "p", Path: t.TempDir()}))
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{ID: chatID, ProjectID: projectID, UserID: user, WorkflowID: &chatID}))
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: chatID, ChatID: chatID, WorkflowName: "builtin://agent", Thread: chatID, Status: db.Active(), CreatedAt: now,
	}))
	agentThread := chatID + "-agent"
	_, err = repo.CreateThread(ctx, &db.Thread{ID: agentThread, ChatID: chatID})
	require.NoError(t, err)
	require.NoError(t, repo.RecordUsageLimitWait(ctx, chatID, agentThread, "Claude", false, now.Add(3*time.Hour), now))

	// The provider refused alice on its 5-hour window, and benched it until
	// the reset. The agent's thread is pinned to alice.
	reset := now.Add(3 * time.Hour).Truncate(time.Second)
	require.NoError(t, repo.BenchAccount(ctx, user, alice.ID, reset, "five_hour", false))
	_, err = repo.SetThreadAssignment(ctx, core.Assignment{ThreadID: agentThread, Provider: "claude", AccountID: alice.ID, AssignedAt: now, Reason: accountrouter.ReasonInitial}, "")
	require.NoError(t, err)

	routing := accountRouting(repo, nil)
	assign := func() (accountrouter.Result, error) {
		avail := drivers.GetAvailableDrivers(ctx, user)
		return routing.Router.Assign(ctx, accountrouter.Request{
			UserID: user, ThreadID: agentThread, Driver: "claude", ModelID: "claude-5.5-opus",
			Accounts: avail.Accounts["claude"],
		})
	}
	_, err = assign()
	var all *accountrouter.ErrAllLimited
	require.True(t, errors.As(err, &all), "while benched the router refuses the account: %v", err)

	// Settings' refresh: the usage endpoint reports the 5-hour window at 10%.
	signals := &recordedSignals{}
	fetcher := &accountlimits.Fetcher{
		Store:  repo,
		Tokens: drivers.RefreshingTokens{Store: repo},
		Doer: usageDoer(func(*http.Request) (*http.Response, error) {
			body := fmt.Sprintf(`{"five_hour":{"utilization":10,"resets_at":%q},"seven_day":{"utilization":20,"resets_at":%q}}`,
				now.Add(2*time.Hour).Format(time.RFC3339), now.Add(48*time.Hour).Format(time.RFC3339))
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
		OnCapacity: func(ctx context.Context, userID, _ string) {
			capacitywake.WakeUser(ctx, repo, signals, userID, "test")
		},
	}
	res, err := fetcher.RefreshUsage(ctx, user, alice.ID)
	require.NoError(t, err)
	require.NotNil(t, res.Limits)
	assert.Nil(t, res.Limits.LimitedUntil, "Settings is answered with the unbenched state")
	assert.Equal(t, core.AccountAvailable, res.Account.Status(res.Limits, time.Now()))

	got, err := assign()
	require.NoError(t, err, "the router serves the account the refresh read with room")
	assert.Equal(t, alice.ID, got.Account.AccountID)
	assert.Equal(t, []string{chatID}, signals.workflows, "the chat waiting on the limit is woken to re-route")
}
