// Copyright (c) 2025 Reliant Labs
package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
)

// error_class_test.go — a user error is written, counted, and pages nobody.
//
// Measured in prod over five days: the ERROR stream — what issues and the
// alerts are built from — was dominated by "no daemon connected for user" (the user's
// machine was closed) and "AI provider usage limit reached" (the user's
// subscription was spent). Nobody on our side could act on either.

// policyLogger is the process logger install builds, writing JSON to buf.
func policyLogger(t *testing.T, buf *bytes.Buffer) *slog.Logger {
	t.Helper()
	return slog.New(withReporting(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

func lineFor(t *testing.T, buf *bytes.Buffer, msg string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec), line)
		if rec["msg"] == msg {
			return rec
		}
	}
	t.Fatalf("no %q line in:\n%s", msg, buf.String())
	return nil
}

// Only an ERROR record becomes an issue, so a user error must not stay one.
func TestWithReporting_UserErrorsAreInfo(t *testing.T) {
	var buf bytes.Buffer
	logger := policyLogger(t, &buf)

	machineOffline := svcerr.WithClass(errors.New("no daemon connected for user"), svcerr.ClassUser)
	// What the workflow side logs after Temporal serialized an activity's
	// user error: only the Benign category survived.
	usageLimit := temporal.NewApplicationErrorWithOptions("AI provider usage limit reached", "ProviderUsageLimit",
		temporal.ApplicationErrorOptions{NonRetryable: true, Category: temporal.ApplicationErrorCategoryBenign})

	logger.Error("[TerminalWS] create terminal session failed", "error", fmt.Errorf("create terminal session: %w", machineOffline))
	logger.Error("[StepExecutor] Activity failed after retry exhaustion", "error", fmt.Errorf("activity error: %w", usageLimit))
	logger.Error("[Cache] Failed to create cache directory", "error", errors.New("mkdir /.config: read-only file system"))

	for _, msg := range []string{"[TerminalWS] create terminal session failed", "[StepExecutor] Activity failed after retry exhaustion"} {
		line := lineFor(t, &buf, msg)
		assert.Equal(t, "INFO", line["level"], msg)
		assert.Equal(t, "user", line["error_class"], msg)
		assert.NotEmpty(t, line["error"], "a user error must still carry its error: %s", msg)
	}
	server := lineFor(t, &buf, "[Cache] Failed to create cache directory")
	assert.Equal(t, "ERROR", server["level"])
	assert.Equal(t, "server", server["error_class"])
}

func TestWithReporting_UserErrorsAreCounted(t *testing.T) {
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_user_error_total"}, []string{"level", "package", "message"})
	previous := deadEndCounter
	SetDeadEndErrorCounter(counter)
	t.Cleanup(func() { SetDeadEndErrorCounter(previous) })

	var buf bytes.Buffer
	logger := policyLogger(t, &buf)
	logger.Error("plan limit hit", "error", svcerr.PlanLimit("seat cap"))
	logger.Info("ordinary info")
	logger.Error("caller went away", "error", fmt.Errorf("stream: %w", svcerr.ErrCanceled))

	levels := map[string]string{}
	metrics := make(chan prometheus.Metric, 8)
	counter.Collect(metrics)
	close(metrics)
	for metric := range metrics {
		var written dto.Metric
		require.NoError(t, metric.Write(&written))
		var level, message string
		for _, pair := range written.GetLabel() {
			switch pair.GetName() {
			case "level":
				level = pair.GetValue()
			case "message":
				message = pair.GetValue()
			}
		}
		levels[message] = level
	}
	assert.Equal(t, map[string]string{"plan limit hit": "user_error"}, levels,
		"a user error is counted under user_error; info and cancellations are not counted")
}
