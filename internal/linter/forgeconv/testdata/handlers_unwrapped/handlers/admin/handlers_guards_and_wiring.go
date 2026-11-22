// Fixture: the shapes that MUST stay silent. Every one of these was a
// live false positive against a wider cut of the rule, found by running
// it over control-plane, cp-forge and two peptides trees — not invented.
package admin

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/crud"

	pb "github.com/user/peptides-admin/gen/services/admin/v1"
	"github.com/user/peptides-admin/internal/db"
	"github.com/user/peptides-admin/pkg/middleware"
)

// RolloutDaemonImage: control-plane's shape. VerifyAuth's ONLY result is
// an error and its body is `return connect.NewError(...)` — the value is
// already a wire error, and demanding a wrap here would be demanding a
// no-op edit on correct code.
func (s *Service) RolloutDaemonImage(
	ctx context.Context,
	req *connect.Request[pb.RolloutDaemonImageRequest],
) (*connect.Response[pb.RolloutDaemonImageResponse], error) {
	if err := middleware.VerifyAuth(ctx, "admin"); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.RolloutDaemonImageResponse{}), nil
}

// GetDashboardOverview: peptides-dogfood-r2's shape, and the one that
// killed the "any (T, error) call is a domain error" cut.
// RequireAdminOrOperator returns (*Claims, error) and the error is
// connect.NewError — a package-level call, not a collaborator reached
// through the receiver.
func (s *Service) GetDashboardOverview(
	ctx context.Context,
	req *connect.Request[pb.GetDashboardOverviewRequest],
) (*connect.Response[pb.GetDashboardOverviewResponse], error) {
	user, err := middleware.RequireAdminOrOperator(ctx)
	if err != nil {
		return nil, err
	}
	_ = user
	return connect.NewResponse(&pb.GetDashboardOverviewResponse{}), nil
}

// ValidateOrder: a same-package validator. Handler packages are where
// projects put request validators that build connect errors directly, so
// a bare-identifier call is never classified.
func (s *Service) ValidateOrder(
	ctx context.Context,
	req *connect.Request[pb.ValidateOrderRequest],
) (*connect.Response[pb.ValidateOrderResponse], error) {
	if err := validateOrderRequest(req.Msg); err != nil {
		return nil, err
	}
	if err := s.checkQuota(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.ValidateOrderResponse{}), nil
}

// crudCreateOrderOp is forge's own generated CRUD wiring. Its `return
// nil, err` sites live inside func literals inside a method whose result
// is not (*connect.Response, error) — pkg/crud does the mapping. Neither
// the boundary filter nor the func-literal skip may let these through.
func (s *Service) crudCreateOrderOp() crud.CreateOp[pb.CreateOrderRequest, pb.CreateOrderResponse, *db.Order] {
	return crud.CreateOp[pb.CreateOrderRequest, pb.CreateOrderResponse, *db.Order]{
		EntityLower: "order",
		Pack: func(entity *db.Order) (*pb.CreateOrderResponse, error) {
			m, err := orderToProto(entity)
			if err != nil {
				return nil, err
			}
			return &pb.CreateOrderResponse{Order: m}, nil
		},
	}
}

// New is the service constructor — no connect type in its parameters, so
// it never crosses the RPC boundary.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}
