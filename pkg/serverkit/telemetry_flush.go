package serverkit

import (
	"context"
	"errors"
	"log/slog"
)

// LogTelemetryFlushError logs a failure of the final telemetry flush at
// shutdown — the OTel provider shutdown (Run) or a supervisor's Flush
// (lifecyclekit.Supervise).
//
// A flush that ran out of the shutdown budget is a WARN, not an ERROR: the
// process is exiting, its last batch is lost, and nobody can act on this one
// line — a collector that stays unreachable is caught by the telemetry-arrival
// checks, which see every process go quiet, not one. As an ERROR it opened an
// issue on every rollout (control-plane prod, 2026-10-23..25: admin-server,
// reliant-api-server, daemon-gateway and the workers, each on pod
// termination). Any other flush failure — an exporter that refused the data —
// stays an ERROR.
func LogTelemetryFlushError(ctx context.Context, logger *slog.Logger, msg string, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		logger.WarnContext(ctx, msg, "error", err,
			"impact", "the shutdown budget ran out before the exporter drained; this process's last telemetry batch is lost")
		return
	}
	logger.ErrorContext(ctx, msg, "error", err)
}
