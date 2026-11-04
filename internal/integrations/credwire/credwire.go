// Copyright (c) 2025 Reliant Labs

// Package credwire builds the credential source integration actions
// authenticate with: saved connections, plus the delegated brokers a
// deployment registers. The worker (a run's actions) and the API server (a
// user's pickers) both build it here, so they cannot disagree about which
// integrations a control plane backs.
//
// When a control plane is configured, the GitHub broker is registered under
// ghdelegated.BrokerID, which the `github` manifest's delegated method names;
// self-hosted registers none, and `github` is a saved connection (reliant's
// own OAuth app or a pasted token).
//
// A control-plane URL without INTERNAL_SERVICE_SECRET is a boot error, never a
// silent fall-back to the self-hosted provider: hosted users' GitHub tokens
// live at control-plane, so falling back would answer "not connected" for
// every one of them.
//
//forge:exclude-contract: wiring only; the interfaces it assembles live in connauth and ghdelegated
package credwire

import (
	"errors"
	"fmt"
	"log/slog"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/ghdelegated"
)

// New builds the credential source over resolver (nil answers every request
// with a FailedPrecondition) and the brokers cp implies.
func New(resolver *connections.Resolver, cfg *configv1.ControlPlaneConfig) (connauth.Service, error) {
	cp := controlplane.EndpointFrom(cfg)
	var deps connauth.Deps
	if resolver != nil {
		deps.Resolver = resolver
	}
	if !cp.Hosted() {
		return connauth.NewInstrumented(deps), nil
	}
	secret := cp.InternalServiceSecret
	if secret == "" {
		return nil, errors.New("integrations: RELIANT_CONTROL_PLANE_URL is set but INTERNAL_SERVICE_SECRET is not; " +
			"hosted GitHub tokens come from control-plane and cannot be fetched unauthenticated")
	}
	brokers := connauth.NewBrokers()
	client := gitcredentialclient.NewInstrumented(gitcredentialclient.Deps{
		ControlPlane: cfg,
		Sign:         func() (string, error) { return auth.SignInternalServiceToken(secret) },
	})
	broker := ghdelegated.NewInstrumented(ghdelegated.Deps{Tokens: client, Installations: client, Logger: slog.Default().With("component", "ghdelegated")})
	if err := brokers.Register(ghdelegated.BrokerID, broker); err != nil {
		return nil, fmt.Errorf("integrations: %w", err)
	}
	deps.Broker = brokers.Get
	return connauth.NewInstrumented(deps), nil
}
