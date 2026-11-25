// yours: scaffolded once by `forge scaffold variant`, never touched again
package app

import (
	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/controlplane"
)

// CallerControlPlane is the api-server's control-plane client that presents
// the caller's own bearer (cloning from GitHub, deleting the platform
// account, an org's hosted apps, coverage reports, waking a suspended
// machine). InternalControlPlane is the one signed as the internal service.
// The composition graph identifies a node by its Go type, so each
// configuration is a type of its own (`forge skill load architecture/graph-checks`).
type CallerControlPlane struct{ controlplane.Service }

// CallerControlPlaneDeps is the control plane's endpoint.
type CallerControlPlaneDeps struct {
	ControlPlane *configv1.ControlPlaneConfig
}

// NewCallerControlPlane is nil when self-hosted, and never defaults to
// localhost: a deployment would then fail against a control plane that is
// not there.
//
//forge:optional-component: a self-hosted deployment has no control plane
//forge:root-only api
func NewCallerControlPlane(d CallerControlPlaneDeps) (*CallerControlPlane, error) {
	if !ControlPlaneEndpoint(d.ControlPlane).Hosted() {
		return nil, nil
	}
	return &CallerControlPlane{Service: controlplane.NewInstrumented(controlplane.Deps{ControlPlane: d.ControlPlane})}, nil
}

// Client is the client as a plain controlplane.Service: an untyped nil when
// the node is absent, never a nil pointer inside a consumer's interface.
func (c *CallerControlPlane) Client() controlplane.Service {
	if c == nil {
		return nil
	}
	return c.Service
}
