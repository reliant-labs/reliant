// Copyright (c) 2025 Reliant Labs
package model_endpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/modelendpoints"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// routerDaemonControl adapts the daemon router to modelendpoints.DaemonControl,
// the same two operations DaemonRegistryService exposes to the browser.
type routerDaemonControl struct{ router Router }

// daemonErr reads a relay failure that says the machine is not connected as
// modelendpoints.ErrDaemonOffline. The router's own text (which gateway, why)
// is transport detail: it stays the cause, for the log, and never reaches the
// wire.
func daemonErr(err error) error {
	if errors.Is(err, toolexec.ErrLocalModelDaemonUnavailable) {
		return svcerr.WithCause(modelendpoints.ErrDaemonOffline, err)
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

func endpointUser(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", svcerr.ErrUnauthenticated
	}
	return userID, nil
}

func (s *Service) ListModelEndpoints(ctx context.Context, _ *connect.Request[reliantv1.ListModelEndpointsRequest]) (*connect.Response[reliantv1.ListModelEndpointsResponse], error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	eps, err := s.manager.List(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("model endpoint request failed"))
	}
	return connect.NewResponse(&reliantv1.ListModelEndpointsResponse{Endpoints: eps}), nil
}

func (s *Service) CreateModelEndpoint(ctx context.Context, req *connect.Request[reliantv1.CreateModelEndpointRequest]) (*connect.Response[reliantv1.CreateModelEndpointResponse], error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	ep, err := s.manager.Create(ctx, userID, req.Msg.GetEndpoint())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("model endpoint request failed"))
	}
	return connect.NewResponse(&reliantv1.CreateModelEndpointResponse{Endpoint: ep}), nil
}

func (s *Service) UpdateModelEndpoint(ctx context.Context, req *connect.Request[reliantv1.UpdateModelEndpointRequest]) (*connect.Response[reliantv1.UpdateModelEndpointResponse], error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	ep, err := s.manager.Update(ctx, userID, req.Msg.GetId(), req.Msg.GetEndpoint())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("model endpoint request failed"))
	}
	return connect.NewResponse(&reliantv1.UpdateModelEndpointResponse{Endpoint: ep}), nil
}

func (s *Service) DeleteModelEndpoint(ctx context.Context, req *connect.Request[reliantv1.DeleteModelEndpointRequest]) (*connect.Response[reliantv1.DeleteModelEndpointResponse], error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.manager.Delete(ctx, userID, req.Msg.GetId()); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("model endpoint request failed"))
	}
	return connect.NewResponse(&reliantv1.DeleteModelEndpointResponse{}), nil
}

func (s *Service) TestModelEndpoint(ctx context.Context, req *connect.Request[reliantv1.TestModelEndpointRequest]) (*connect.Response[reliantv1.TestModelEndpointResponse], error) {
	userID, err := endpointUser(ctx)
	if err != nil {
		return nil, err
	}
	probe, latency, err := s.manager.Test(ctx, userID, req.Msg.GetId(), req.Msg.GetDraft())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("model endpoint request failed"))
	}
	return connect.NewResponse(&reliantv1.TestModelEndpointResponse{Probe: probe, LatencyMs: latency.Milliseconds()}), nil
}
