// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// slowQueryTemporalClient is a run whose worker is too busy to answer a query:
// QueryWorkflowWithOptions returns only when its context ends. It records how
// long each query was allowed to wait, so the test pins the bound itself
// rather than the send's wall-clock time.
type slowQueryTemporalClient struct {
	wakeTestTemporalClient
	mu      sync.Mutex
	queries []time.Duration // the time each query's context had left; -1 for no deadline
}

func (c *slowQueryTemporalClient) QueryWorkflowWithOptions(
	ctx context.Context, _ *client.QueryWorkflowWithOptionsRequest,
) (*client.QueryWorkflowWithOptionsResponse, error) {
	left := time.Duration(-1)
	if deadline, ok := ctx.Deadline(); ok {
		left = time.Until(deadline)
	}
	c.mu.Lock()
	c.queries = append(c.queries, left)
	c.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

// A send with params asks the run whether they changed, to decide whether to
// save a hidden "params changed" note. That query is answered by the worker,
// and a busy worker held sends for 5+ seconds in prod (and every later send to
// the same chat behind them, on the run-control lock). The answer is advisory,
// so the send must not wait on it: the message is queued, the params signalled
// and the thread woken regardless.
func TestSendMessage_DoesNotWaitOnASlowParamsQuery(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	temporal := &slowQueryTemporalClient{wakeTestTemporalClient: wakeTestTemporalClient{
		absorbTestTemporalClient: absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING},
	}}
	service := &Service{
		deps: Deps{
			Repo:     repo,
			Temporal: temporal,
		},
		runs: runs.NewService(repo, temporal, nil),
	}

	// A backstop, so a regression fails instead of hanging the package.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "next step"))
	require.NoError(t, err)
	// The query is bounded by its own short timeout, not by the request's
	// context. (A wall-clock bound on the whole send was flaky: on a loaded
	// gate the send's DB round trips alone could pass it.)
	temporal.mu.Lock()
	queries := append([]time.Duration(nil), temporal.queries...)
	temporal.mu.Unlock()
	require.NotEmpty(t, queries, "the send asks the run whether its params changed")
	for _, left := range queries {
		require.GreaterOrEqual(t, left, time.Duration(0), "the advisory query has no deadline of its own")
		assert.LessOrEqual(t, left, paramsChangeQueryTimeout, "the send waited on the workflow's answer to an advisory query")
	}
	assert.True(t, resp.Msg.Queued, "the message is queued for the running thread's next turn")

	var names []string
	for _, s := range temporal.signals {
		names = append(names, s.Name)
	}
	assert.Contains(t, names, "update_workflow_state", "the params still reach the run")
	assert.Contains(t, names, v2.ThreadWakeSignalName, "the thread is still woken")
}

// TestSendMessage_DoesNotReadTheWholeProjectConfigRow pins the latency fix on
// every branch a send takes: queued to a running run, resuming a paused one,
// and starting a new run after a finished one. Each sends what the composer
// does — workflow params and a preset selection — on the default workflow, so
// it exercises preset loading, workflow resolution and input validation.
//
// The whole-row read is GetProjectConfigRecord, which fetches the entire
// project_configs row. That row carries every skill body and repo memory the
// daemon indexed, recursively across nested repos. In prod a workspace of ~30
// checkouts synced an 18 MB row (23.6 MB of it project_skills_json when
// re-measured from the same workspace), and SendMessage read it 3+N times per
// send (N = selected presets) to learn what two empty columns said: there are
// no project workflows and no project presets. Goroutine dumps sampled while
// sends were in flight caught the handler inside that read 70 times out of 74;
// SendMessage p50 was 1.7s, StartChat 2.3s. A spy used to count that read;
// now Repository simply does not declare it, so no send can make it.
func TestSendMessage_DoesNotReadTheWholeProjectConfigRow(t *testing.T) {
	testkit.Slow(t, "a DB-backed chat handler test with many round trips to pgtest's Postgres")
	t.Parallel()

	cases := []struct {
		name     string
		status   db.WorkflowStatus
		temporal enums.WorkflowExecutionStatus
		pause    bool
	}{
		{"queued to a running run", db.Active(), enums.WORKFLOW_EXECUTION_STATUS_RUNNING, false},
		{"resuming a paused run", db.Paused(), enums.WORKFLOW_EXECUTION_STATUS_RUNNING, true},
		{"starting a new run", db.Completed(), enums.WORKFLOW_EXECUTION_STATUS_COMPLETED, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, cleanup := db.SetupTestDB(t)
			t.Cleanup(cleanup)

			ctx, fx := setupAbsorbFixture(t, repo, "test-user", tc.status)
			chat, err := repo.GetChat(ctx, fx.chatID)
			require.NoError(t, err)
			// A synced record, as every project with a connected daemon has.
			skills := `[{"name":"a-skill","body":"` + strings.Repeat("x", 64<<10) + `"}]`
			require.NoError(t, repo.UpsertProjectConfigRecord(ctx, &db.ProjectConfigRecord{
				ProjectID: chat.ProjectID, DaemonID: "daemon-1", ProjectSkillsJSON: &skills,
			}))

			temporal := &wakeTestTemporalClient{
				absorbTestTemporalClient: absorbTestTemporalClient{exists: true, status: tc.temporal},
			}
			var pause runs.PauseController
			if tc.pause {
				pause = succeedingPauseController{}
			}
			service := &Service{
				deps: Deps{
					Repo:     repo,
					Temporal: temporal,
				},
				runs: runs.NewService(repo, temporal, pause),
			}

			req := sendMessageRequest(t, fx.chatID, "next step")
			req.Msg.SelectedPresets = map[string]string{"default": "general"}
			_, err = service.SendMessage(ctx, req)
			require.NoError(t, err)

			requireRepositoryLacks(t, "GetProjectConfigRecord",
				"a send must read the project config columns it needs, never the whole row: "+
					"the row holds every indexed skill body and was 18 MB in prod")
		})
	}
}
