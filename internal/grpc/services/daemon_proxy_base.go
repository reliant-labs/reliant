// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// daemonProxyBase is the shared forwarding plumbing for gRPC services that, on a
// cloud daemon, must run their work on the daemon's filesystem rather than the
// api-server's. A per-service proxy EMBEDS it and calls dispatch to forward a
// typed request to a registered daemon command (e.g. "pkg.list_commands").
//
// Its reason to exist is UNIFORM, LOUD error mapping: every failure mode —
// daemon unreachable, dispatch timeout, command not registered, malformed
// response — becomes a Connect error, never a silently-empty success. A silent
// empty result is precisely the class of bug this base was written to prevent
// (PackageCommandsService.ListCommands returning [] on a cloud daemon because
// discovery ran on the wrong filesystem).
//
// The existing FileSystem/Background/Terminal proxies predate this base and
// still carry their own copies of getUserID/sendCommand; they are structured so
// they can adopt this base later (tracked as a follow-up).
type daemonProxyBase struct {
	router toolexec.DaemonRouter
}

// userID pulls the authenticated user id from the request context, returning a
// Connect Unauthenticated error when absent.
func (b *daemonProxyBase) userID(ctx context.Context) (string, error) {
	uid, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return "", connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}
	return uid, nil
}

// dispatch marshals req, forwards it to the daemon as commandType, and
// unmarshals the reply into resp. Any failure is mapped onto a loud Connect
// error via daemonerr.DispatchConnectError; dispatch never returns nil while leaving
// resp unpopulated, so callers can rely on "no error => resp is valid".
func (b *daemonProxyBase) dispatch(
	ctx context.Context,
	userID, commandType string,
	req, resp any,
	timeoutMs int32,
) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("marshal %s request: %w", commandType, err))
	}

	respBytes, err := b.router.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
	if err != nil {
		return daemonerr.DispatchConnectError(commandType, err)
	}

	if err := json.Unmarshal(respBytes, resp); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal %s response: %w", commandType, err))
	}
	return nil
}
