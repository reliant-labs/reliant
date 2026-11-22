// Fixture: the peptides-rw1 reproduction, verbatim in shape.
//
// An agent hand-wrote this file over forge's born stub (which returned
// svcerr.Wrap(svcerr.WithReason(...))) and left four `return nil, err`
// sites with no svcerr import at all. Against a live binary every one of
// these RPCs answered 500 {"code":"unknown"} with no X-Forge-Error-Reason,
// while the prescription file beside it — same package, same phase —
// answered 400 failed_precondition with the header.
//
// All four handlers must fire.
package admin

import (
	"context"

	"connectrpc.com/connect"

	pb "github.com/user/peptides-admin/gen/services/admin/v1"
	"github.com/user/peptides-admin/internal/lifecycle"
)

// SubmitOrder moves an order draft → pending.
func (s *Service) SubmitOrder(
	ctx context.Context,
	req *connect.Request[pb.SubmitOrderRequest],
) (*connect.Response[pb.SubmitOrderResponse], error) {
	res, err := s.deps.Lifecycle.SubmitOrder(ctx, lifecycle.TransitionInput{ID: req.Msg.GetId()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.SubmitOrderResponse{Order: orderResultToProto(res)}), nil
}

// ApproveOrder moves an order pending → approved.
func (s *Service) ApproveOrder(
	ctx context.Context,
	req *connect.Request[pb.ApproveOrderRequest],
) (*connect.Response[pb.ApproveOrderResponse], error) {
	res, err := s.deps.Lifecycle.ApproveOrder(ctx, lifecycle.TransitionInput{ID: req.Msg.GetId()})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.ApproveOrderResponse{Order: orderResultToProto(res)}), nil
}

// ShipOrder moves an order approved → shipped. This is the RPC the
// forensics probed: DRAFT → SHIPPED is an illegal transition, the
// interactor returns svcerr.WithReason(svcerr.FailedPrecondition(...),
// "invalid_status_transition"), and this return throws all of it away.
func (s *Service) ShipOrder(
	ctx context.Context,
	req *connect.Request[pb.ShipOrderRequest],
) (*connect.Response[pb.ShipOrderResponse], error) {
	res, err := s.deps.Lifecycle.ShipOrder(ctx, lifecycle.ShipInput{
		ID:             req.Msg.GetId(),
		TrackingNumber: req.Msg.GetTrackingNumber(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.ShipOrderResponse{Order: orderResultToProto(res)}), nil
}

// CancelOrder moves an order from any non-terminal status → cancelled.
func (s *Service) CancelOrder(
	ctx context.Context,
	req *connect.Request[pb.CancelOrderRequest],
) (*connect.Response[pb.CancelOrderResponse], error) {
	res, err := s.deps.Lifecycle.CancelOrder(ctx, lifecycle.CancelInput{
		ID:     req.Msg.GetId(),
		Reason: req.Msg.GetReason(),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.CancelOrderResponse{Order: orderResultToProto(res)}), nil
}
