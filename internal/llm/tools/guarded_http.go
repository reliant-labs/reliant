package tools

import (
	"net/http"
	"time"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/netguard"
)

// webToolTransport builds the dial-guarded transport for every outbound HTTP
// client of an in-process (PlacementServer/PlacementAny) tool. Tests override it.
var webToolTransport = func() http.RoundTripper {
	return netguard.ForDeployment(controlplane.BaseURLFromEnv()).Transport()
}

// newGuardedHTTPClient is the only way tools in this package may build an
// http.Client: hosted deployments cannot dial private, loopback, link-local or
// metadata addresses, including via redirects or DNS rebinding.
func newGuardedHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: webToolTransport()}
}
