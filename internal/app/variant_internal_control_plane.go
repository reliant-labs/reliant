// yours: scaffolded once by `forge scaffold variant`, never touched again
package app

import (
	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
)

// InternalControlPlane is the api-server's control-plane client signed as
// the internal service (the project-move preview's facts, the org usage
// view's list prices). CallerControlPlane presents the caller's bearer.
type InternalControlPlane struct{ controlplane.Service }

// InternalControlPlaneDeps is the control plane's endpoint and secret.
type InternalControlPlaneDeps struct {
	ControlPlane *configv1.ControlPlaneConfig
}

// NewInternalControlPlane is nil when the control plane is not hosted or
// this process has no internal-service secret.
//
//forge:optional-component: a self-hosted deployment, or one without the internal-service secret, cannot sign as the internal service
//forge:root-only api
func NewInternalControlPlane(d InternalControlPlaneDeps) (*InternalControlPlane, error) {
	cp := ControlPlaneEndpoint(d.ControlPlane)
	if !cp.Hosted() || cp.InternalServiceSecret == "" {
		return nil, nil
	}
	secret := cp.InternalServiceSecret
	return &InternalControlPlane{Service: controlplane.NewInstrumented(controlplane.Deps{
		ControlPlane: d.ControlPlane,
		Sign:         func() (string, error) { return auth.SignInternalServiceToken(secret) },
	})}, nil
}

// Client is the client as a plain controlplane.Service: an untyped nil when
// the node is absent, never a nil pointer inside a consumer's interface.
func (c *InternalControlPlane) Client() controlplane.Service {
	if c == nil {
		return nil
	}
	return c.Service
}
