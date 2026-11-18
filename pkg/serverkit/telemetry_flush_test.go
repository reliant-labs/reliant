package serverkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// A shutdown flush that ran out of budget is a WARN (nobody can act on one
// exiting process's lost batch); any other flush failure stays an ERROR.
func TestLogTelemetryFlushError_LevelFollowsTheCause(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"budget exhausted", fmt.Errorf("failed to upload metrics: %w", context.DeadlineExceeded), "level=WARN"},
		{"joined deadlines", errors.Join(context.DeadlineExceeded, context.DeadlineExceeded), "level=WARN"},
		{"exporter refused", errors.New("failed to upload metrics: 400 Bad Request"), "level=ERROR"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			LogTelemetryFlushError(context.Background(), slog.New(slog.NewTextHandler(&buf, nil)), "telemetry flush error", c.err)
			if !strings.Contains(buf.String(), c.want) {
				t.Errorf("logged %q, want %s", buf.String(), c.want)
			}
		})
	}
}
