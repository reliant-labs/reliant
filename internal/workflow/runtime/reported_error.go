// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"errors"

	"go.temporal.io/sdk/log"
)

// reportedError marks an error that was already logged at ERROR, with its full
// context, where it arose. A workflow failure travels up through the step, the
// loop iteration, the loop, the inline workflow and the run, and each layer
// that returns it used to log it again: one loop-guard stop was five ERROR
// lines and five Sentry events (2026-10-22). The layers that only pass a
// reported error along log it at DEBUG (logFailure).
type reportedError struct{ error }

func (e reportedError) Unwrap() error { return e.error }

// markReported returns err marked as logged. nil stays nil.
func markReported(err error) error {
	if err == nil || alreadyReported(err) {
		return err
	}
	return reportedError{err}
}

// alreadyReported reports whether err, or an error it wraps, is marked.
func alreadyReported(err error) bool {
	var r reportedError
	return errors.As(err, &r)
}

// logFailure logs a failure a layer is returning: at ERROR; at DEBUG when an
// inner layer already reported err; at INFO when it is the user's cancel. A
// cancel ends the thread the way the user asked, and ERROR is for faults:
// each layer it passed through used to log it there, and every ERROR opens an
// obs issue (prod 4d68df91, reliant #2395). A real failure keeps ERROR.
func logFailure(logger log.Logger, err error, msg string, keyvals ...interface{}) {
	switch {
	case alreadyReported(err):
		logger.Debug(msg, keyvals...)
	case errors.Is(err, ErrThreadCancelled):
		logger.Info(msg, keyvals...)
	default:
		logger.Error(msg, keyvals...)
	}
}

// reportExpectedStop logs, at WARN, a failure the runtime raises on purpose
// to end an agent — a guard working as designed on what the agent was told —
// and returns err marked reported, so the layers that pass it up log it at
// DEBUG. The run still fails with it, and its user reads why in the run; it
// is not a fault of the platform, so it is not an ERROR (prod bc65dd1b, an
// agent that kept asking GitHub for a repository the app is not installed on).
func reportExpectedStop(logger log.Logger, err error, msg string, keyvals ...interface{}) error {
	logger.Warn(msg, keyvals...)
	return markReported(err)
}
