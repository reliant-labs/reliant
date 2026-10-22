// Copyright (c) 2025 Reliant Labs
package connection

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// connectionError maps a domain error onto a Connect code. NotFound stays
// NotFound (never PermissionDenied) so ids are not an oracle. Untyped errors
// are internal and carry no detail to the client.
func connectionError(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	var e *connections.Error
	if !errors.As(err, &e) {
		if errors.Is(err, context.Canceled) {
			return connect.NewError(connect.CodeCanceled, err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return connect.NewError(connect.CodeDeadlineExceeded, err)
		}
		return svcerr.ToConnect(svcerr.Internal("connection service error"))
	}
	msg := errors.New(e.Message)
	switch e.Code {
	case connections.CodeNotFound:
		return connect.NewError(connect.CodeNotFound, msg)
	case connections.CodeInvalidArgument:
		return connect.NewError(connect.CodeInvalidArgument, msg)
	case connections.CodeAlreadyExists:
		return connect.NewError(connect.CodeAlreadyExists, msg)
	case connections.CodeFailedPrecondition, connections.CodeNeedsReauth:
		return connect.NewError(connect.CodeFailedPrecondition, msg)
	case connections.CodeUnavailable:
		return connect.NewError(connect.CodeUnavailable, msg)
	default:
		return svcerr.ToConnect(svcerr.Internal("connection service error"))
	}
}

func connectionStatusToProto(s string) reliantv1.ConnectionStatus {
	switch s {
	case core.ConnectionStatusActive:
		return reliantv1.ConnectionStatus_CONNECTION_STATUS_ACTIVE
	case core.ConnectionStatusNeedsReauth:
		return reliantv1.ConnectionStatus_CONNECTION_STATUS_NEEDS_REAUTH
	case core.ConnectionStatusRevoked:
		return reliantv1.ConnectionStatus_CONNECTION_STATUS_REVOKED
	}
	return reliantv1.ConnectionStatus_CONNECTION_STATUS_UNSPECIFIED
}

func rfc3339Ptr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func connectionToProto(c *core.Connection) *reliantv1.Connection {
	scopes := c.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return &reliantv1.Connection{
		Id: c.ID, IntegrationId: c.IntegrationID, AuthKind: connectionrpc.AuthKind(c.AuthKind),
		Name: c.Name, AccountLabel: derefStr(c.AccountLabel), ExternalAccountId: derefStr(c.ExternalAccountID),
		Scopes: scopes, Status: connectionStatusToProto(c.Status), StatusReason: derefStr(c.StatusReason),
		IsDefault: c.IsDefault, AccessExpiresAt: rfc3339Ptr(c.AccessExpiresAt), LastUsedAt: rfc3339Ptr(c.LastUsedAt),
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339),
		Params: c.Params, SenderId: derefStr(c.SenderID),
	}
}

func (s *Service) ListIntegrations(ctx context.Context, _ *connect.Request[reliantv1.ListIntegrationsRequest]) (*connect.Response[reliantv1.ListIntegrationsResponse], error) {
	if _, err := connectionrpc.Caller(ctx); err != nil {
		return nil, err
	}
	out := []*reliantv1.Integration{}
	for _, i := range s.deps.Connections.ListIntegrations() {
		in := &reliantv1.Integration{Id: i.ID, DisplayName: i.DisplayName}
		for _, m := range i.Methods {
			in.Methods = append(in.Methods, &reliantv1.IntegrationAuthMethod{
				Kind: connectionrpc.AuthKind(m.Kind), Available: m.Available,
				UnavailableReason: m.Reason, FieldLabels: m.FieldLabels,
			})
		}
		for _, p := range i.Params {
			in.ConnectionParams = append(in.ConnectionParams, &reliantv1.IntegrationConnectionParam{
				Name: p.GetName(), DisplayName: p.GetDisplayName(), Description: p.GetDescription(),
				Pattern: p.GetPattern(), DefaultValue: p.GetDefaultValue(), Required: p.GetDefaultValue() == "",
			})
		}
		out = append(out, in)
	}
	return connect.NewResponse(&reliantv1.ListIntegrationsResponse{Integrations: out}), nil
}

func (s *Service) ListConnections(ctx context.Context, req *connect.Request[reliantv1.ListConnectionsRequest]) (*connect.Response[reliantv1.ListConnectionsResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.deps.Connections.List(ctx, userID, req.Msg.GetIntegrationId())
	if err != nil {
		return nil, connectionError(err)
	}
	out := make([]*reliantv1.Connection, 0, len(cs))
	for _, c := range cs {
		out = append(out, connectionToProto(c))
	}
	return connect.NewResponse(&reliantv1.ListConnectionsResponse{Connections: out}), nil
}

func (s *Service) GetConnection(ctx context.Context, req *connect.Request[reliantv1.GetConnectionRequest]) (*connect.Response[reliantv1.GetConnectionResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.deps.Connections.Get(ctx, userID, req.Msg.GetId())
	if err != nil {
		return nil, connectionError(err)
	}
	return connect.NewResponse(&reliantv1.GetConnectionResponse{Connection: connectionToProto(c)}), nil
}

func (s *Service) CreateApiKeyConnection(ctx context.Context, req *connect.Request[reliantv1.CreateApiKeyConnectionRequest]) (*connect.Response[reliantv1.CreateApiKeyConnectionResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	var kind connections.APIKeyKind
	switch req.Msg.GetKind() {
	case reliantv1.ApiKeyConnectionKind_API_KEY_CONNECTION_KIND_API_KEY:
		kind = connections.APIKeyKindAPIKey
	case reliantv1.ApiKeyConnectionKind_API_KEY_CONNECTION_KIND_BASIC:
		kind = connections.APIKeyKindBasic
	}
	c, err := s.deps.Connections.CreateAPIKey(ctx, connections.CreateAPIKeyParams{
		UserID: userID, IntegrationID: req.Msg.GetIntegrationId(), Name: req.Msg.GetName(),
		Kind: kind, Fields: req.Msg.GetFields(), Params: req.Msg.GetParams(),
	})
	if err != nil {
		return nil, connectionError(err)
	}
	return connect.NewResponse(&reliantv1.CreateApiKeyConnectionResponse{Connection: connectionToProto(c)}), nil
}

func (s *Service) StartOAuth(ctx context.Context, req *connect.Request[reliantv1.StartOAuthRequest]) (*connect.Response[reliantv1.StartOAuthResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	url, err := s.deps.Connections.StartOAuth(ctx, connections.StartParams{
		UserID: userID, IntegrationID: req.Msg.GetIntegrationId(), Name: req.Msg.GetName(),
		ReconnectID: req.Msg.GetReconnectId(), RedirectAfter: req.Msg.GetRedirectAfter(),
		Params: req.Msg.GetParams(),
		// The browser sets Origin; page script cannot. It names the web app
		// the provider's redirect is relayed back to.
		ClientOrigin:     req.Header().Get("Origin"),
		LoopbackRedirect: req.Msg.GetLoopbackRedirect(),
	})
	if err != nil {
		return nil, connectionError(err)
	}
	return connect.NewResponse(&reliantv1.StartOAuthResponse{AuthorizeUrl: url}), nil
}

func (s *Service) CompleteOAuth(ctx context.Context, req *connect.Request[reliantv1.CompleteOAuthRequest]) (*connect.Response[reliantv1.CompleteOAuthResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	done, err := s.deps.Connections.CompleteOAuth(ctx, userID, req.Msg.GetState(), req.Msg.GetCode())
	if err != nil {
		return nil, connectionError(err)
	}
	return connect.NewResponse(&reliantv1.CompleteOAuthResponse{
		Connection: connectionToProto(done.Connection), RedirectAfter: done.RedirectAfter,
	}), nil
}

func (s *Service) TestConnection(ctx context.Context, req *connect.Request[reliantv1.TestConnectionRequest]) (*connect.Response[reliantv1.TestConnectionResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.deps.Connections.Test(ctx, userID, req.Msg.GetId())
	if err != nil {
		return nil, connectionError(err)
	}
	return connect.NewResponse(&reliantv1.TestConnectionResponse{
		Ok: r.OK, Probed: r.Probed, AccountLabel: r.AccountLabel, ErrorClass: r.ErrorClass,
	}), nil
}

func (s *Service) RenameConnection(ctx context.Context, req *connect.Request[reliantv1.RenameConnectionRequest]) (*connect.Response[reliantv1.RenameConnectionResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.deps.Connections.Rename(ctx, userID, req.Msg.GetId(), req.Msg.GetName())
	if err != nil {
		return nil, connectionError(err)
	}
	return connect.NewResponse(&reliantv1.RenameConnectionResponse{Connection: connectionToProto(c)}), nil
}

func (s *Service) SetDefaultConnection(ctx context.Context, req *connect.Request[reliantv1.SetDefaultConnectionRequest]) (*connect.Response[reliantv1.SetDefaultConnectionResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.deps.Connections.SetDefault(ctx, userID, req.Msg.GetId())
	if err != nil {
		return nil, connectionError(err)
	}
	return connect.NewResponse(&reliantv1.SetDefaultConnectionResponse{Connection: connectionToProto(c)}), nil
}

func (s *Service) DeleteConnection(ctx context.Context, req *connect.Request[reliantv1.DeleteConnectionRequest]) (*connect.Response[reliantv1.DeleteConnectionResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Connections.Delete(ctx, userID, req.Msg.GetId()); err != nil {
		return nil, connectionError(err)
	}
	return connect.NewResponse(&reliantv1.DeleteConnectionResponse{}), nil
}

func (s *Service) ListConnectionEvents(ctx context.Context, req *connect.Request[reliantv1.ListConnectionEventsRequest]) (*connect.Response[reliantv1.ListConnectionEventsResponse], error) {
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	evs, err := s.deps.Connections.Events(ctx, userID, req.Msg.GetId(), int(req.Msg.GetLimit()), req.Msg.GetBeforeId())
	if err != nil {
		return nil, connectionError(err)
	}
	out := make([]*reliantv1.ConnectionEvent, 0, len(evs))
	for _, e := range evs {
		out = append(out, &reliantv1.ConnectionEvent{
			Id: e.ID, ConnectionId: e.ConnectionID, Kind: e.Kind, RunId: e.RunID, NodeId: e.NodeID,
			ToolCallId: e.ToolCallID, Actor: e.Actor, At: e.At.UTC().Format(time.RFC3339),
		})
	}
	return connect.NewResponse(&reliantv1.ListConnectionEventsResponse{Events: out}), nil
}
