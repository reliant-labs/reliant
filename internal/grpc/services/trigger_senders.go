// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// SenderDirectory looks people up on one integration for a trigger's "Only
// from", with the caller's own credential for it: handles (GitHub logins) to
// the ids trigger.sender.id carries, and ids to the person's current name.
// Satisfied by *ghusers.Directory via an adapter in serverapi.
type SenderDirectory interface {
	Resolve(ctx context.Context, userID string, handles, ids []string) ([]ResolvedSender, error)
	// IsPermanent reports whether a Resolve error needs the user to act
	// (connect or reconnect the integration) rather than a retry.
	IsPermanent(err error) bool
	// IsInvalid reports whether a Resolve error is the request's fault (too
	// many people at once).
	IsInvalid(err error) bool
}

// ResolvedSender is one person a SenderDirectory found.
type ResolvedSender struct {
	// Query is the handle or id as it was asked.
	Query string
	// ID is trigger.sender.id for the person.
	ID string
	// DisplayName is the person's current name, for display only.
	DisplayName string
}

// WithSenderDirectories enables ResolveTriggerSenders for the integrations
// named, keyed by integration id.
func (s *TriggerService) WithSenderDirectories(dirs map[string]SenderDirectory) *TriggerService {
	s.senders = dirs
	return s
}

// ResolveTriggerSenders looks people up for "Only from". It reads nothing of
// the caller's triggers: it asks the provider, as the caller, about people.
func (s *TriggerService) ResolveTriggerSenders(
	ctx context.Context,
	req *connect.Request[reliantv1.ResolveTriggerSendersRequest],
) (*connect.Response[reliantv1.ResolveTriggerSendersResponse], error) {
	userID := auth.MustGetUserID(ctx)
	integration := strings.TrimSpace(req.Msg.Integration)
	if integration != "github" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("looking people up is only available for GitHub, not %q", integration))
	}
	dir := s.senders[integration]
	if dir == nil {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("GitHub triggers are not set up on this server, so GitHub people cannot be looked up"))
	}
	found, err := dir.Resolve(ctx, userID, req.Msg.Handles, req.Msg.SenderIds)
	switch {
	case err == nil:
	case dir.IsInvalid(err):
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	case dir.IsPermanent(err):
		// The caller's own problem to fix, and the only message they need:
		// the cause can name internals.
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("connect GitHub (or reconnect it) in Settings to look people up by their GitHub login"))
	default:
		logging.Warn("looking GitHub people up failed", "user_id", userID, "error", err)
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("GitHub could not be reached to look people up; try again"))
	}
	out := make([]*reliantv1.ResolvedTriggerSender, 0, len(found))
	for _, f := range found {
		out = append(out, &reliantv1.ResolvedTriggerSender{Query: f.Query, SenderId: f.ID, DisplayName: f.DisplayName})
	}
	return connect.NewResponse(&reliantv1.ResolveTriggerSendersResponse{Senders: out}), nil
}
