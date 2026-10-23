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

// logFailure logs a failure a layer is returning: at ERROR, or at DEBUG when
// an inner layer already reported err.
func logFailure(logger log.Logger, err error, msg string, keyvals ...interface{}) {
	if alreadyReported(err) {
		logger.Debug(msg, keyvals...)
		return
	}
	logger.Error(msg, keyvals...)
}
