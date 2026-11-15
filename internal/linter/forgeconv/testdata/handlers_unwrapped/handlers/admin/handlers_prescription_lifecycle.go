// Fixture: the half of peptides-rw1 that was CORRECT, in the same
// package as the broken half. Identical collaborator calls, identical
// illegal-transition semantics — the only difference is svcerr.Wrap, and
// that difference was the whole difference between 400 failed_precondition
// with X-Forge-Error-Reason and 500 unknown with nothing.
//
// Not one of these may fire. If the rule ever goes off here it is
// demanding a second wrap of an already-wrapped error, and it will be
// switched off within a day.
package admin

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/user/peptides-admin/gen/services/admin/v1"
	"github.com/user/peptides-admin/internal/lifecycle"
)

// SubmitPrescription moves a prescription draft → pending.
func (s *Service) SubmitPrescription(
	ctx context.Context,
	req *connect.Request[pb.SubmitPrescriptionRequest],
) (*connect.Response[pb.SubmitPrescriptionResponse], error) {
	res, err := s.deps.Lifecycle.SubmitPrescription(ctx, lifecycle.TransitionInput{ID: req.Msg.GetId()})
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return connect.NewResponse(&pb.SubmitPrescriptionResponse{
		Prescription: prescriptionResultToProto(res),
	}), nil
}

// ApprovePrescription moves a prescription pending → approved. Wrapped in
// two steps rather than one, to pin that a REASSIGNMENT through a wrapping
// call counts as wrapping just as much as an inline call does.
func (s *Service) ApprovePrescription(
	ctx context.Context,
	req *connect.Request[pb.ApprovePrescriptionRequest],
) (*connect.Response[pb.ApprovePrescriptionResponse], error) {
	res, err := s.deps.Lifecycle.ApprovePrescription(ctx, lifecycle.TransitionInput{ID: req.Msg.GetId()})
	if err != nil {
		err = svcerr.Wrap(err)
		return nil, err
	}
	return connect.NewResponse(&pb.ApprovePrescriptionResponse{
		Prescription: prescriptionResultToProto(res),
	}), nil
}

// CancelPrescription moves a prescription → cancelled. Wrapped through a
// helper this package named itself, to pin that forge does not require
// the name `svcerr.Wrap` — only that SOMETHING was applied.
func (s *Service) CancelPrescription(
	ctx context.Context,
	req *connect.Request[pb.CancelPrescriptionRequest],
) (*connect.Response[pb.CancelPrescriptionResponse], error) {
	res, err := s.deps.Lifecycle.CancelPrescription(ctx, lifecycle.CancelInput{
		ID:     req.Msg.GetId(),
		Reason: req.Msg.GetReason(),
	})
	if err != nil {
		return nil, onWire(err)
	}
	return connect.NewResponse(&pb.CancelPrescriptionResponse{
		Prescription: prescriptionResultToProto(res),
	}), nil
}

// onWire is this project's own name for the mapping. Deliberately not
// spelled like anything forge ships.
func onWire(err error) error { return svcerr.Wrap(err) }
