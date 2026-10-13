// Copyright (c) 2025 Reliant Labs
package daemoncmd

import (
	"context"
	"encoding/json"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
)

// forwarder is the Service.
type forwarder struct {
	sender Sender
}

// New returns the forwarder.
func New(deps Deps) Service {
	return forwarder{sender: deps.Sender}
}

// UserID pulls the authenticated user id from the request context, returning a
// Connect Unauthenticated error when absent.
func UserID(ctx context.Context) (string, error) {
	uid, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return "", connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}
	return uid, nil
}

func (f forwarder) Dispatch(
	ctx context.Context,
	userID, commandType string,
	req, resp any,
	timeoutMs int32,
) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("marshal %s request: %w", commandType, err))
	}

	respBytes, err := f.sender.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
	if err != nil {
		return daemonerr.DispatchConnectError(commandType, err)
	}

	if err := json.Unmarshal(respBytes, resp); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal %s response: %w", commandType, err))
	}
	return nil
}
