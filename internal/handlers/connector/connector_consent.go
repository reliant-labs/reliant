// Copyright (c) 2025 Reliant Labs
package connector

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
)

// Consent: recording which connector an OAuth client may act through.
//
// An OAuth access token identifies a user, not a grant. Without an explicit
// choice the MCP endpoint would have to guess which of a user's connectors an
// application meant, and a wrong guess hands that application authority its
// user never intended — invisibly. These RPCs are how the user answers.

// AuthorizeClient records the consent decision.
func (s *Service) AuthorizeClient(
	ctx context.Context,
	req *connect.Request[reliantv1.AuthorizeClientRequest],
) (*connect.Response[reliantv1.AuthorizeClientResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	clientID := strings.TrimSpace(req.Msg.GetClientId())
	if clientID == "" {
		return nil, svcerr.InvalidArgument("client id is required")
	}

	grant, credential, err := s.grantForConsent(ctx, userID, req.Msg)
	if err != nil {
		return nil, err
	}

	binding := &connectorgrant.ClientBinding{
		ID:         uuid.New().String(),
		UserID:     userID,
		ClientID:   clientID,
		GrantID:    grant.ID,
		ClientName: strings.TrimSpace(req.Msg.GetClientName()),
	}
	if err := s.deps.Grants.PutBinding(ctx, binding); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to record consent"))
	}

	return connect.NewResponse(&reliantv1.AuthorizeClientResponse{
		Connector:  toProtoConnector(grant),
		Credential: credential,
	}), nil
}

// grantForConsent resolves the connector a consent request names, creating one
// when the user chose to.
//
// Creating inline reuses the same validation as CreateConnector rather than a
// parallel path: the consent screen and the settings editor are the same
// decision in two places, and two validators would eventually disagree about
// what a valid grant is.
func (s *Service) grantForConsent(
	ctx context.Context,
	userID string,
	msg *reliantv1.AuthorizeClientRequest,
) (*connectorgrant.Grant, string, error) {
	existingID := strings.TrimSpace(msg.GetConnectorId())
	newConnector := msg.GetNewConnector()

	switch {
	case existingID != "" && newConnector != nil:
		return nil, "", svcerr.InvalidArgument("specify either an existing connector or a new one, not both")

	case existingID != "":
		// Scoped by user, so a guessed id cannot bind a client to someone
		// else's connector.
		grant, err := s.deps.Grants.GetGrant(ctx, userID, existingID)
		if errors.Is(err, connectorgrant.ErrNotFound) {
			return nil, "", svcerr.NotFound("connector")
		}
		if err != nil {
			return nil, "", svcerr.WrapOr(err, svcerr.Internal("failed to get connector"))
		}
		if err := grant.IsLive(time.Now()); err != nil {
			return nil, "", svcerr.FailedPrecondition("that connector has been revoked or has expired")
		}
		return grant, "", nil

	case newConnector != nil:
		grant, err := s.buildGrant(userID, newConnector)
		if err != nil {
			return nil, "", err
		}
		raw, err := s.createGrantWithCredential(ctx, grant)
		if err != nil {
			return nil, "", svcerr.WrapOr(err, svcerr.Internal("failed to create connector"))
		}
		return grant, raw, nil

	default:
		return nil, "", svcerr.InvalidArgument("choose an existing connector or describe a new one")
	}
}

// ListAuthorizedClients returns the applications acting through the caller's
// connectors.
func (s *Service) ListAuthorizedClients(
	ctx context.Context,
	_ *connect.Request[reliantv1.ListAuthorizedClientsRequest],
) (*connect.Response[reliantv1.ListAuthorizedClientsResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	bindings, err := s.deps.Grants.ListBindingsByUser(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list authorized clients"))
	}

	// Connector names are resolved so the UI can say "ChatGPT → my-project"
	// rather than showing an opaque id pair.
	names := map[string]string{}
	if grants, err := s.deps.Grants.ListGrantsByUser(ctx, userID); err == nil {
		for _, g := range grants {
			names[g.ID] = g.Name
		}
	}

	out := make([]*reliantv1.AuthorizedClient, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, &reliantv1.AuthorizedClient{
			ClientId:      b.ClientID,
			ClientName:    b.ClientName,
			ConnectorId:   b.GrantID,
			ConnectorName: names[b.GrantID],
			AuthorizedAt:  b.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return connect.NewResponse(&reliantv1.ListAuthorizedClientsResponse{Clients: out}), nil
}

// RevokeClientAuthorization disconnects one application.
//
// The connector survives: it may serve several applications, and disconnecting
// one should not cut off the others. Revoking the connector itself is the
// separate, broader action.
func (s *Service) RevokeClientAuthorization(
	ctx context.Context,
	req *connect.Request[reliantv1.RevokeClientAuthorizationRequest],
) (*connect.Response[reliantv1.RevokeClientAuthorizationResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, svcerr.ErrUnauthenticated
	}

	clientID := strings.TrimSpace(req.Msg.GetClientId())
	if clientID == "" {
		return nil, svcerr.InvalidArgument("client id is required")
	}

	revoked, err := s.deps.Grants.DeleteBinding(ctx, userID, clientID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to revoke authorization"))
	}
	return connect.NewResponse(&reliantv1.RevokeClientAuthorizationResponse{Revoked: revoked}), nil
}
