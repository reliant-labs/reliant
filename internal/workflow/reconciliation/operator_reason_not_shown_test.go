// Copyright (c) 2025 Reliant Labs. All rights reserved.
package reconciliation

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"

	"github.com/reliant-labs/reliant/internal/db"
)

// A Temporal terminate reason is operator-facing text. An operator ended a
// wedged prod run with "orchestrator: wedged on TMPRL1100 (history from #641
// build has no version marker); continue from checkpoint" and the reconciler
// posted it to the user's chat verbatim; the user took the product for broken
// and deleted the chat. The reason belongs in the log.

const operatorJargonReason = "orchestrator: wedged on TMPRL1100 (history from #641 build has no version marker); continue from checkpoint"

func chatNoticeTexts(t *testing.T, repo *terminationRepo) []string {
	t.Helper()
	var texts []string
	for _, update := range repo.errorUpdates() {
		for _, key := range []string{"error_message", "error_summary"} {
			text, ok := update.data[key].(string)
			require.True(t, ok, "%s is a string", key)
			texts = append(texts, text)
		}
	}
	for _, saved := range repo.savedMessages {
		texts = append(texts, saved.content)
	}
	return texts
}

func TestReconciler_OperatorTerminate_ChatNoticeCarriesNoTemporalReason(t *testing.T) {
	cases := []struct {
		name        string
		start       bool
		wantMessage string
	}{
		{
			name:        "auto-continued",
			start:       true,
			wantMessage: "This run was interrupted and restarted from your last message — nothing you sent was lost.",
		},
		{
			name:        "not continued",
			start:       false,
			wantMessage: "This run was stopped before it finished. Send a message to continue from where it left off.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			repo := newTerminationRepo(terminatedChatWorkflow())
			reconciler := NewReconciler(repo, terminatedRunClient(operatorJargonReason), DefaultConfig())
			reconciler.SetQueuedDelivery(&fakeQueuedDelivery{start: tc.start})

			_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
			require.Empty(t, errs)

			updates := repo.errorUpdates()
			require.Len(t, updates, 1)
			assert.Equal(t, tc.wantMessage, updates[0].data["error_message"])
			assert.Equal(t, tc.wantMessage, updates[0].data["error_summary"])
			for _, text := range chatNoticeTexts(t, repo) {
				assert.NotContains(t, text, "TMPRL1100")
				assert.NotContains(t, text, "orchestrator")
				assert.NotContains(t, text, "Reason:")
				assert.NotContains(t, text, "stopped by the system")
			}

			lines := logsWithMessage(logs(), silentEndMessage)
			require.Len(t, lines, 1)
			assert.Equal(t, slog.LevelInfo, lines[0].level)
			assert.Equal(t, operatorJargonReason, lines[0].attrs["reason"], "operators still get the reason")
			assert.Equal(t, "Terminated", lines[0].attrs["closeStatus"])
			assert.Equal(t, "wf-1", lines[0].attrs["workflowID"])
			assert.Equal(t, "chat-1", lines[0].attrs["chatID"])
		})
	}
}

func TestReconciler_TimedOutRun_ChatNoticeSaysItRanPastItsTimeLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start bool
		want  string
	}{
		{"auto-continued", true, "This run ran past its time limit and was restarted from your last message — nothing you sent was lost."},
		{"not continued", false, "This run ran past its time limit and was stopped. Send a message to continue from where it left off."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTerminationRepo(terminatedChatWorkflow())
			client := &mockReconcilerTemporalClient{
				describeResponses: map[string]mockDescribeResponse{
					"wf-1": {resp: makeTerminalDescribeResp(enums.WORKFLOW_EXECUTION_STATUS_TIMED_OUT)},
				},
			}
			reconciler := NewReconciler(repo, client, DefaultConfig())
			reconciler.SetQueuedDelivery(&fakeQueuedDelivery{start: tc.start})

			_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
			require.Empty(t, errs)

			assert.Equal(t, db.Failed(), repo.rows["wf-1"].Status)
			updates := repo.errorUpdates()
			require.Len(t, updates, 1)
			assert.Equal(t, tc.want, updates[0].data["error_message"])
			assert.Contains(t, updates[0].data["error_message"], "ran past its time limit")
		})
	}
}
