// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

// TestResolveMaxAttempts_InlineSaveMessage is the regression test for the
// "Attempt 1" red card.
//
// A SaveMessage failure rendered in the transcript as a TERMINAL error reading
// "Attempt 1" while Temporal still had four retries left. The cause: inline
// SaveMessage is dispatched with MaximumAttempts 5 but sets a 30s
// StartToCloseTimeout and NO heartbeat, so it missed resolveMaxAttempts'
// signature match on the graph-step timeout shape and resolved to 0. Zero makes
// activityIsRetrying return false, so is_retrying went out false and the UI drew
// a dead error for a failure that was about to be retried.
func TestResolveMaxAttempts_InlineSaveMessage(t *testing.T) {
	// Exactly what executeSaveMessageInline dispatches: no heartbeat, 30s
	// start-to-close, and (as the SDK reports in practice) no retry policy.
	info := activity.Info{
		ActivityType:        activity.Type{Name: "SaveMessage"},
		StartToCloseTimeout: 30 * time.Second,
	}

	got := resolveMaxAttempts(info)

	if got == 0 {
		t.Fatal("resolveMaxAttempts returned 0 for inline SaveMessage; " +
			"0 makes activityIsRetrying false, which renders a retryable failure " +
			"as a terminal red error in the transcript")
	}
	if got != inlineSaveMessageMaxAttempts {
		t.Errorf("expected %d attempts (what save_message.go dispatches), got %d",
			inlineSaveMessageMaxAttempts, got)
	}

	// The behaviour that actually reached the user.
	if !activityIsRetrying(1, got, errors.New("could not serialize access (SQLSTATE 40001)")) {
		t.Error("attempt 1 of a 5-attempt ladder must report is_retrying=true")
	}
	// And the other end of the ladder must still read as terminal.
	if activityIsRetrying(5, got, errors.New("could not serialize access (SQLSTATE 40001)")) {
		t.Error("the final attempt must not report is_retrying=true")
	}
}

// TestResolveMaxAttempts_ServerPolicyWins keeps the authoritative source
// authoritative: whatever the server reports beats every local inference.
func TestResolveMaxAttempts_ServerPolicyWins(t *testing.T) {
	info := activity.Info{
		ActivityType:        activity.Type{Name: "SaveMessage"},
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 9},
	}

	if got := resolveMaxAttempts(info); got != 9 {
		t.Errorf("server-reported policy must win; expected 9, got %d", got)
	}
}

// TestResolveMaxAttempts_GraphStepAndRouter pins the one genuinely ambiguous
// case: CallLLM runs on two different ladders depending on who dispatched it,
// and the router's fixed start-to-close is what tells them apart.
func TestResolveMaxAttempts_GraphStepAndRouter(t *testing.T) {
	graphStep := activity.Info{
		ActivityType:        activity.Type{Name: "CallLLM"},
		HeartbeatTimeout:    activityHeartbeatTimeout,
		StartToCloseTimeout: 10 * time.Minute,
	}
	if got := resolveMaxAttempts(graphStep); got != stepActivityMaxAttempts {
		t.Errorf("graph step: expected %d, got %d", stepActivityMaxAttempts, got)
	}

	router := activity.Info{
		ActivityType:        activity.Type{Name: "CallLLM"},
		HeartbeatTimeout:    activityHeartbeatTimeout,
		StartToCloseTimeout: routerActivityStartToClose,
	}
	if got := resolveMaxAttempts(router); got != infrastructureActivityMaxAttempts {
		t.Errorf("router dispatch: expected %d, got %d", infrastructureActivityMaxAttempts, got)
	}
}

// TestExtractInfrastructureSummary_HidesRawSQLSTATE is the regression test for
// the error card the user screenshotted: four layers of Go wrapping around a
// Postgres error code, shown verbatim for a failure the system retries itself.
func TestExtractInfrastructureSummary_HidesRawSQLSTATE(t *testing.T) {
	// The exact string from the transcript.
	raw := "failed to save message: failed to create chat_update: failed to get " +
		"next sequence number: allocate chat update sequence: ERROR: could not " +
		"serialize access due to concurrent update (SQLSTATE 40001)"

	summary := extractLLMErrorSummary(raw)

	if summary == "" {
		t.Fatal("a serialization failure must produce a summary; without one the UI " +
			"falls back to the raw wrap chain, which is what users saw")
	}
	if strings.Contains(summary, "SQLSTATE") || strings.Contains(summary, "40001") {
		t.Errorf("summary still leaks the SQLSTATE: %q", summary)
	}
	if strings.Contains(strings.ToLower(summary), "chat_update") {
		t.Errorf("summary leaks internal table names: %q", summary)
	}
	// It must say it is self-correcting — that is the part that tells the user
	// whether to wait or to act.
	if !strings.Contains(strings.ToLower(summary), "retr") {
		t.Errorf("summary should say the system is retrying; got %q", summary)
	}
}

func TestExtractInfrastructureSummary_Classifications(t *testing.T) {
	cases := []struct {
		name     string
		err      string
		wantPart string
	}{
		{"deadlock", "ERROR: deadlock detected (SQLSTATE 40P01)", "retrying"},
		{"cancellation", "failed to load: context canceled", "Stopped"},
		{"bad connection", "driver: bad connection", "database connection"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractLLMErrorSummary(tc.err)
			if got == "" {
				t.Fatalf("expected a summary for %q", tc.err)
			}
			if !strings.Contains(got, tc.wantPart) {
				t.Errorf("summary %q does not mention %q", got, tc.wantPart)
			}
			if strings.Contains(got, "SQLSTATE") {
				t.Errorf("summary leaks SQLSTATE: %q", got)
			}
		})
	}
}

// TestExtractLLMErrorSummary_ProviderErrorsStillWin guards the ordering: adding
// infrastructure classification must not swallow the provider summaries that
// were already working.
func TestExtractLLMErrorSummary_ProviderErrorsStillWin(t *testing.T) {
	cases := []struct {
		name     string
		err      string
		wantPart string
	}{
		{"overloaded", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, "overloaded"},
		{"rate limit", "429 rate limit exceeded", "Rate limited"},
		{"network", "dial tcp 1.2.3.4:443: no such host", "Cannot reach"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractLLMErrorSummary(tc.err)
			if !strings.Contains(strings.ToLower(got), strings.ToLower(tc.wantPart)) {
				t.Errorf("expected summary containing %q, got %q", tc.wantPart, got)
			}
		})
	}
}
