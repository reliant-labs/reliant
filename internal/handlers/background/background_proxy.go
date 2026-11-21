// Copyright (c) 2025 Reliant Labs
package background

import (
	"context"
	"encoding/json"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// Every rpc forwards its request to the caller's daemon through Deps.Router:
// a DaemonCommand (request/response), or for StreamProcessOutput the
// daemon's output subscription.

func (s *Service) getUserID(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return "", svcerr.Unauthenticated("user ID not found in context")
	}
	return userID, nil
}

func (s *Service) sendCommand(ctx context.Context, userID, commandType string, req any, resp any, timeoutMs int32) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("marshal request"))
	}

	respBytes, err := s.deps.Router.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
	if err != nil {
		return daemonFailure("daemon command "+commandType, err)
	}

	if err := json.Unmarshal(respBytes, resp); err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("unmarshal response"))
	}
	return nil
}

// daemonFailure is a failed daemon round trip as the svcerr kind the rpc
// returns. A machine that is starting, asleep or absent is not a server
// fault: Unavailable while it is on its way, FailedPrecondition when the user
// has none (daemonerr.MachineState; ListProcesses used to return the second as
// Internal, ELECTRON-A6). A daemon older than this server is FailedPrecondition
// in the daemon's own words (daemonerr.Outdated). Anything else is Internal,
// saying what was being done: the daemon's own words reach the user, the
// router's and the transport's stay on the error as its logged cause.
func daemonFailure(doing string, err error) error {
	if daemonerr.IsOutdated(err) {
		return daemonerr.Outdated(err)
	}
	if serr, ok := daemonerr.MachineState("", err); ok {
		return serr
	}
	msg := doing + " failed"
	if text, ok := toolexec.DaemonReportedText(err); ok {
		msg += ": " + text
	}
	return svcerr.WithCause(svcerr.Internal(msg), err)
}

// daemonProcessInfo mirrors daemon.ProcessInfo for JSON deserialization.
type daemonProcessInfo struct {
	ID        string     `json:"id"`
	Command   string     `json:"command"`
	Status    string     `json:"status"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	StartTime time.Time  `json:"start_time"`
	EndTime   *time.Time `json:"end_time,omitempty"`
}
