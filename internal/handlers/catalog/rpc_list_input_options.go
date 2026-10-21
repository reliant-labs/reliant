// Copyright (c) 2025 Reliant Labs
package catalog

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/integrations/inputoptions"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ListInputOptions lists the values a picker offers for an integration-defined
// input type, as the caller.
//
// Auth: the caller is the authenticated user and nobody else. The user id comes
// from the verified credential, never the request, so the connection a request
// names is looked up among THAT user's connections and another user's id reads
// as a missing one. A machine credential (an access token, which is what an
// agent in a daemon holds) is refused: the options are the person's own view of
// their provider account, and an agent reaches the provider only through the
// integration tools the workflow or the model was given, each of which is its
// own action with its own audit.
func (s *Service) ListInputOptions(ctx context.Context, req *connect.Request[reliantv1.ListInputOptionsRequest]) (*connect.Response[reliantv1.ListInputOptionsResponse], error) {
	if _, machine := auth.MachineTokenFromContext(ctx); machine {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("listing input options requires an interactive session"))
	}
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	if s.deps.InputOptions == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("input options are not configured"))
	}
	msg := req.Msg
	scope := make(map[string]any, len(msg.GetScope()))
	for k, v := range msg.GetScope() {
		scope[k] = v.AsInterface()
	}
	identity := actingIdentityName(msg.GetActingIdentity())

	if len(msg.GetResolve()) > 0 {
		res, err := s.deps.InputOptions.Resolve(ctx, inputoptions.ResolveRequest{
			UserID: userID, Type: msg.GetType(), ConnectionID: msg.GetConnectionId(), Scope: scope,
			Values: msg.GetResolve(), ActingIdentity: identity,
		})
		if err != nil {
			return nil, s.inputOptionsError(ctx, err)
		}
		return connect.NewResponse(&reliantv1.ListInputOptionsResponse{
			Options:         inputOptionsToProto(res.Options),
			ConnectionState: connectionStateToProto(res.State),
		}), nil
	}

	res, err := s.deps.InputOptions.List(ctx, inputoptions.ListRequest{
		UserID: userID, Type: msg.GetType(), ConnectionID: msg.GetConnectionId(), Scope: scope,
		Query: msg.GetQuery(), PageToken: msg.GetPageToken(), PageSize: int(msg.GetPageSize()), ActingIdentity: identity,
	})
	if err != nil {
		return nil, s.inputOptionsError(ctx, err)
	}
	return connect.NewResponse(&reliantv1.ListInputOptionsResponse{
		Options:         inputOptionsToProto(res.Options),
		NextPageToken:   res.NextPageToken,
		ConnectionState: connectionStateToProto(res.State),
	}), nil
}

func actingIdentityName(i reliantv1.ActingIdentity) string {
	if i == reliantv1.ActingIdentity_ACTING_IDENTITY_APP {
		return "app"
	}
	return "owner"
}

func connectionStateToProto(st inputoptions.State) reliantv1.InputOptionsConnectionState {
	switch st {
	case inputoptions.StateConnected:
		return reliantv1.InputOptionsConnectionState_INPUT_OPTIONS_CONNECTION_STATE_CONNECTED
	case inputoptions.StateNeedsConnection:
		return reliantv1.InputOptionsConnectionState_INPUT_OPTIONS_CONNECTION_STATE_NEEDS_CONNECTION
	}
	return reliantv1.InputOptionsConnectionState_INPUT_OPTIONS_CONNECTION_STATE_UNSPECIFIED
}

func inputOptionsToProto(in []inputoptions.Option) []*reliantv1.InputOption {
	out := make([]*reliantv1.InputOption, 0, len(in))
	for _, o := range in {
		p := &reliantv1.InputOption{
			Value: o.Value, Label: o.Label, Description: o.Description, Group: o.Group, Warnings: o.Warnings,
		}
		if len(o.Annotations) > 0 {
			p.Annotations = make(map[string]*structpb.Value, len(o.Annotations))
			for k, v := range o.Annotations {
				if pv, err := structpb.NewValue(v); err == nil {
					p.Annotations[k] = pv
				}
			}
		}
		out = append(out, p)
	}
	return out
}

// inputOptionsError maps the option service's typed failures onto wire codes.
// What reaches the caller is the service's own message, which never carries a
// credential or a provider response body.
func (s *Service) inputOptionsError(ctx context.Context, err error) error {
	var upstream *inputoptions.UpstreamError
	switch {
	case errors.Is(err, inputoptions.ErrUnknownType), errors.Is(err, inputoptions.ErrConnectionNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New(inputoptions.Message(err)))
	case errors.Is(err, inputoptions.ErrNoOptions), errors.Is(err, inputoptions.ErrBadScope),
		errors.Is(err, inputoptions.ErrBadPageToken), errors.Is(err, inputoptions.ErrBadRequest):
		return connect.NewError(connect.CodeInvalidArgument, errors.New(inputoptions.Message(err)))
	case errors.As(err, &upstream):
		if upstream.Retryable {
			return connect.NewError(connect.CodeUnavailable, errors.New(upstream.Message))
		}
		return connect.NewError(connect.CodeFailedPrecondition, errors.New(upstream.Message))
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	logging.ErrorContext(ctx, "list input options failed", "error", err)
	return connect.NewError(connect.CodeInternal, fmt.Errorf("listing input options failed"))
}
