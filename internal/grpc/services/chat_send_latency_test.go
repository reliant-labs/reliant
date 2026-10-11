// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/converter"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// fullConfigRowSpy is the repository a send runs against, with one read
// instrumented: GetProjectConfigRecord, which fetches the whole project_configs
// row.
//
// That row carries every skill body and repo memory the daemon indexed,
// recursively across nested repos. In prod a workspace of ~30 checkouts synced
// an 18 MB row (23.6 MB of it project_skills_json when re-measured from the
// same workspace), and SendMessage read it 3+N times per send (N = selected
// presets) to learn what two empty columns said: there are no project
// workflows and no project presets. Goroutine dumps sampled while sends were
// in flight caught the handler inside that read 70 times out of 74; SendMessage
// p50 was 1.7s, StartChat 2.3s.
type fullConfigRowSpy struct {
	db.Repository

	mu      sync.Mutex
	callers []string
}

func (s *fullConfigRowSpy) GetProjectConfigRecord(ctx context.Context, projectID string) (*db.ProjectConfigRecord, error) {
	s.mu.Lock()
	s.callers = append(s.callers, callerChain())
	s.mu.Unlock()
	return s.Repository.GetProjectConfigRecord(ctx, projectID)
}

func (s *fullConfigRowSpy) reads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.callers...)
}

// callerChain names the reliant frames that made a read, innermost first, so a
// regression says which path brought the whole-row read back.
func callerChain() string {
	pcs := make([]uintptr, 32)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs)])
	var chain []string
	for {
		f, more := frames.Next()
		if strings.Contains(f.Function, "reliant-labs/reliant/internal/") && !strings.Contains(f.Function, "_test") {
			chain = append(chain, f.Function[strings.LastIndex(f.Function, "/")+1:])
		}
		if !more || len(chain) == 6 {
			break
		}
	}
	return strings.Join(chain, " <- ")
}

// slowQueryTemporalClient is a run whose worker is too busy to answer a query:
// QueryWorkflow returns only when its context ends.
type slowQueryTemporalClient struct {
	wakeTestTemporalClient
}

func (c *slowQueryTemporalClient) QueryWorkflow(
	ctx context.Context, _, _, _ string, _ ...interface{},
) (converter.EncodedValue, error) {
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
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	temporal := &slowQueryTemporalClient{wakeTestTemporalClient{
		absorbTestTemporalClient: absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING},
	}}
	service := &ChatService{
		database:   repo,
		tempClient: temporal,
		runs:       runs.NewService(repo, temporal, nil),
	}

	// A backstop, so a regression fails instead of hanging the package.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "next step"))
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Less(t, elapsed, 3*time.Second, "the send waited on the workflow's answer to an advisory query")
	assert.True(t, resp.Msg.Queued, "the message is queued for the running thread's next turn")

	var names []string
	for _, s := range temporal.signals {
		names = append(names, s.name)
	}
	assert.Contains(t, names, "update_workflow_state", "the params still reach the run")
	assert.Contains(t, names, v2.ThreadWakeSignalName, "the thread is still woken")
}

// TestSendMessage_DoesNotReadTheWholeProjectConfigRow pins the latency fix on
// every branch a send takes: queued to a running run, resuming a paused one,
// and starting a new run after a finished one. Each sends what the composer
// does — workflow params and a preset selection — on the default workflow, so
// it exercises preset loading, workflow resolution and input validation.
func TestSendMessage_DoesNotReadTheWholeProjectConfigRow(t *testing.T) {
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
			spy := &fullConfigRowSpy{Repository: repo}
			service := &ChatService{
				database:   spy,
				tempClient: temporal,
				runs:       runs.NewService(repo, temporal, pause),
			}

			req := sendMessageRequest(t, fx.chatID, "next step")
			req.Msg.SelectedPresets = map[string]string{"default": "general"}
			_, err = service.SendMessage(ctx, req)
			require.NoError(t, err)

			assert.Empty(t, spy.reads(),
				"a send must read the project config columns it needs, never the whole row: "+
					"the row holds every indexed skill body and was 18 MB in prod")
		})
	}
}
