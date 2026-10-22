// Copyright (c) 2025 Reliant Labs
package catalog

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connectionrpc"
	"github.com/reliant-labs/reliant/internal/integrations/inputoptions"
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
		return nil, svcerr.PermissionDenied("listing input options requires an interactive session")
	}
	userID, err := connectionrpc.Caller(ctx)
	if err != nil {
		return nil, err
	}
	if s.deps.InputOptions == nil {
		return nil, svcerr.Unimplemented("input options are not configured")
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
			return nil, svcerr.WrapOr(err, svcerr.Internal("listing input options failed"))
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
		return nil, svcerr.WrapOr(err, svcerr.Internal("listing input options failed"))
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
