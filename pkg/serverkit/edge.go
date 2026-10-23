package serverkit

import (
	"fmt"
	"net/http"
)

// Edge is the HTTP layers a listener puts in front of its routes: CORS,
// security headers, the request id and an outermost wrapper of the
// project's own. Each is a factory the project supplies — the matching logic
// stays in the project's middleware — and Config decides which are on.
// serverkit.Run builds its edge from the Server's fields; a listener in a
// composition graph builds one directly for ListenerDeps.Edge.
type Edge struct {
	CORS            func(origins []string, allowCredentials bool) func(http.Handler) http.Handler
	SecurityHeaders func(production bool) func(http.Handler) http.Handler
	RequestID       func() func(http.Handler) http.Handler
	Outer           func(http.Handler) http.Handler
}

// Wrap returns the edge for cfg: CORS when cfg.CORSEnabled, security headers
// when cfg.SecurityHeaders, then the request id outermost, inside Outer.
// CORS and security headers are security controls, so one cfg turns on but
// whose factory is nil is an error — never a listener serving with the layer
// silently absent.
func (e Edge) Wrap(cfg Config) (func(http.Handler) http.Handler, error) {
	if cfg.CORSEnabled() && e.CORS == nil {
		return nil, fmt.Errorf("serverkit: CORS is enabled (origins set, or Environment=%s) but no CORS middleware was supplied — refusing to serve with CORS unenforced", EnvDevelopment)
	}
	if cfg.SecurityHeaders && e.SecurityHeaders == nil {
		return nil, fmt.Errorf("serverkit: Config.SecurityHeaders is enabled but no security-headers middleware was supplied — refusing to serve without security headers")
	}
	return func(handler http.Handler) http.Handler {
		if cfg.CORSEnabled() {
			handler = e.CORS(cfg.CORSOrigins, cfg.CORSAllowCredentials)(handler)
		}
		// HSTS only outside development.
		if cfg.SecurityHeaders {
			handler = e.SecurityHeaders(!cfg.IsDevelopment())(handler)
		}
		// Outermost of serverkit's own layers, so CORS, security headers and
		// logging all see the correlation id.
		if e.RequestID != nil {
			handler = e.RequestID()(handler)
		}
		if e.Outer != nil {
			handler = e.Outer(handler)
		}
		return handler
	}, nil
}
