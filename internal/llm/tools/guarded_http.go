package tools

import (
	"net/http"
	"time"

	"github.com/reliant-labs/reliant/internal/netguard"
)

// webToolTransport builds the dial-guarded transport for every outbound HTTP
// client of an in-process (PlacementServer/PlacementAny) tool, under the
// deployment's policy (ToolsOptions.ControlPlaneURL). Tests override it.
var webToolTransport = func(policy netguard.Policy) http.RoundTripper {
	return policy.Transport()
}

// newGuardedHTTPClient is the only way tools in this package may build an
// http.Client: hosted deployments cannot dial private, loopback, link-local or
// metadata addresses, including via redirects or DNS rebinding.
func newGuardedHTTPClient(policy netguard.Policy, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: webToolTransport(policy)}
}
