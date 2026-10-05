// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/modelendpoints"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// ModelEndpointService is the thin Connect layer over modelendpoints.Manager.
type ModelEndpointService struct {
	reliantv1connect.UnimplementedModelEndpointServiceHandler
	manager *modelendpoints.Manager
}

// NewModelEndpointService wires the service. router may be nil (monolith with
// no daemon router): VIA_DAEMON endpoints then report the relay unavailable.
// creds may be nil, which leaves credential storage unavailable.
func NewModelEndpointService(database db.Repository, router toolexec.DaemonRouter, creds modelendpoints.EndpointCredentials) *ModelEndpointService {
	cfg := modelendpoints.Config{
		Store:       database,
		Credentials: creds,
		Policy:      netguard.ForDeployment(tokenauthority.ControlPlaneURL()),
	}
	if router != nil {
		cfg.Daemons = routerDaemonControl{router: router}
	}
	return &ModelEndpointService{manager: modelendpoints.NewManager(cfg)}
}

// routerDaemonControl adapts the daemon router to modelendpoints.DaemonControl,
// the same two operations DaemonRegistryService exposes to the browser.
type routerDaemonControl struct{ router toolexec.DaemonRouter }

func daemonErr(err error) error {
	if errors.Is(err, toolexec.ErrLocalModelDaemonUnavailable) {
		return fmt.Errorf("%w: %v", modelendpoints.ErrDaemonOffline, err)
	}
	return err
}

func (c routerDaemonControl) Refresh(ctx context.Context, userID, daemonID string) (*reliantv1.LocalModelInventory, error) {
	inv, err := c.router.RefreshLocalModels(ctx, userID, daemonID)
	return inv, daemonErr(err)
}

func (c routerDaemonControl) SetConfiguredEndpoints(ctx context.Context, userID, daemonID string, baseURLs []string) (*reliantv1.LocalModelInventory, error) {
	if baseURLs == nil {
		baseURLs = []string{}
	}
	payload, err := json.Marshal(map[string][]string{"base_urls": baseURLs})
	if err != nil {
		return nil, err
	}
	if _, err := c.router.SendDaemonCommandToDaemon(ctx, userID, daemonID, toolexec.CommandLocalModelsSetEndpoints, payload, 15000); err != nil {
		return nil, daemonErr(fmt.Errorf("writing endpoints on machine %s: %w", daemonID, err))
	}
	inv, err := c.router.RefreshLocalModels(ctx, userID, daemonID)
	return inv, daemonErr(err)
}

func modelEndpointErr(err error) error {
	var already *connect.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &already):
		return err
	case modelendpoints.IsValidation(err):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, modelendpoints.ErrCredentialStoreUnavailable):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, db.ErrModelEndpointNotFound), errors.Is(err, modelendpoints.ErrDaemonNotOwned):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, db.ErrModelEndpointNameTaken):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, modelendpoints.ErrDaemonOffline):
		return connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func endpointUser(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, nil)
	}
	return userID, nil
}

func (s *ModelEndpointService) ListModelEndpoints(ctx context.Context, _ *connect.Request[reliantv1.ListModelEndpointsRequest]) (*connect.Response[reliantv1.ListModelEndpointsResponse], error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	eps, err := s.manager.List(ctx, userID)
	if err != nil {
		return nil, modelEndpointErr(err)
	}
	return connect.NewResponse(&reliantv1.ListModelEndpointsResponse{Endpoints: eps}), nil
}

func (s *ModelEndpointService) CreateModelEndpoint(ctx context.Context, req *connect.Request[reliantv1.CreateModelEndpointRequest]) (*connect.Response[reliantv1.CreateModelEndpointResponse], error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	ep, err := s.manager.Create(ctx, userID, req.Msg.GetEndpoint())
	if err != nil {
		return nil, modelEndpointErr(err)
	}
	return connect.NewResponse(&reliantv1.CreateModelEndpointResponse{Endpoint: ep}), nil
}

func (s *ModelEndpointService) UpdateModelEndpoint(ctx context.Context, req *connect.Request[reliantv1.UpdateModelEndpointRequest]) (*connect.Response[reliantv1.UpdateModelEndpointResponse], error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	ep, err := s.manager.Update(ctx, userID, req.Msg.GetId(), req.Msg.GetEndpoint())
	if err != nil {
		return nil, modelEndpointErr(err)
	}
	return connect.NewResponse(&reliantv1.UpdateModelEndpointResponse{Endpoint: ep}), nil
}

func (s *ModelEndpointService) DeleteModelEndpoint(ctx context.Context, req *connect.Request[reliantv1.DeleteModelEndpointRequest]) (*connect.Response[reliantv1.DeleteModelEndpointResponse], error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.manager.Delete(ctx, userID, req.Msg.GetId()); err != nil {
		return nil, modelEndpointErr(err)
	}
	return connect.NewResponse(&reliantv1.DeleteModelEndpointResponse{}), nil
}

func (s *ModelEndpointService) TestModelEndpoint(ctx context.Context, req *connect.Request[reliantv1.TestModelEndpointRequest]) (*connect.Response[reliantv1.TestModelEndpointResponse], error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	probe, latency, err := s.manager.Test(ctx, userID, req.Msg.GetId(), req.Msg.GetDraft())
	if err != nil {
		return nil, modelEndpointErr(err)
	}
	return connect.NewResponse(&reliantv1.TestModelEndpointResponse{Probe: probe, LatencyMs: latency.Milliseconds()}), nil
}
