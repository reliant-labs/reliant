package middleware

import (
	"net/http"
	"strings"
)

// SecurityHeadersConfig controls which headers SecurityHeadersMiddleware sets
// and the exact values used. Zero-value fields fall back to sensible OWASP
// defaults suitable for a Connect/HTTP API server.
type SecurityHeadersConfig struct {
	// Enabled disables the middleware entirely when false. Allows operators
	// to opt out without unwinding the handler chain.
	Enabled bool

	// Production toggles headers that only make sense when served over
	// HTTPS in a real deployment (currently: Strict-Transport-Security).
	// Typically mirrors cfg.Environment != "development".
	Production bool

	// ContentSecurityPolicy overrides the default CSP. The default is
	// "default-src 'none'; frame-ancestors 'none'; base-uri 'none'" which
	// is appropriate for an API server that does not serve HTML. If the
	// server also serves HTML (e.g. an admin UI), supply a CSP that
	// permits the required sources explicitly.
	ContentSecurityPolicy string

	// PermissionsPolicy overrides the default Permissions-Policy. The
	// default denies access to camera, microphone, geolocation,
	// interest-cohort (FLoC), payment, and USB.
	PermissionsPolicy string

	// ReferrerPolicy overrides the default Referrer-Policy
	// ("strict-origin-when-cross-origin").
	ReferrerPolicy string

	// HSTSMaxAge overrides the default Strict-Transport-Security max-age
	// (2 years, per MDN / OWASP guidance). Only emitted when Production is
	// true. Set to a negative value to disable HSTS even in production.
	HSTSMaxAge int
}

// DefaultSecurityHeadersConfig returns a SecurityHeadersConfig with OWASP
// defaults suitable for an API server. Callers should toggle Enabled and
// Production based on their runtime config.
func DefaultSecurityHeadersConfig() SecurityHeadersConfig {
	return SecurityHeadersConfig{
		Enabled:               true,
		Production:            false,
		ContentSecurityPolicy: "default-src 'none'; frame-ancestors 'none'; base-uri 'none'",
		PermissionsPolicy:     "camera=(), microphone=(), geolocation=(), interest-cohort=(), payment=(), usb=()",
		ReferrerPolicy:        "strict-origin-when-cross-origin",
		HSTSMaxAge:            63072000, // 2 years
	}
}

// SecurityHeadersMiddleware returns an HTTP middleware that sets OWASP-recommended
// security response headers:
//
//   - Content-Security-Policy — locks down what the browser may load
//   - X-Content-Type-Options: nosniff — prevents MIME sniffing
//   - Referrer-Policy — limits Referer leakage
//   - Permissions-Policy — opts out of powerful browser APIs
//   - Strict-Transport-Security — forces HTTPS (production only)
//
// Headers relevant to health/pprof endpoints (health checks don't need CSP
// stringency, but we still set nosniff/referrer/permissions there for
// consistency and defense in depth). Callers wire this in at the outermost
// HTTP layer so every response benefits — including /healthz, /readyz,
// /metrics, and REST/webhook routes that bypass the Connect interceptor
// chain.
//
// When cfg.Enabled is false, this becomes a no-op.
func SecurityHeadersMiddleware(cfg SecurityHeadersConfig) func(http.Handler) http.Handler {
	if !cfg.Enabled {
		return func(next http.Handler) http.Handler { return next }
	}

	csp := cfg.ContentSecurityPolicy
	if csp == "" {
		csp = "default-src 'none'; frame-ancestors 'none'; base-uri 'none'"
	}
	perms := cfg.PermissionsPolicy
	if perms == "" {
		perms = "camera=(), microphone=(), geolocation=(), interest-cohort=(), payment=(), usb=()"
	}
	ref := cfg.ReferrerPolicy
	if ref == "" {
		ref = "strict-origin-when-cross-origin"
	}
	hstsMaxAge := cfg.HSTSMaxAge
	if hstsMaxAge == 0 {
		hstsMaxAge = 63072000
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()

			// Loosen CSP for endpoints that return non-JSON debug/plaintext
			// content. /debug/pprof/* in particular serves HTML index pages
			// that the strict "default-src 'none'" policy would break when
			// pprof is consulted from a browser.
			effectiveCSP := csp
			if strings.HasPrefix(r.URL.Path, "/debug/pprof") {
				effectiveCSP = "default-src 'self'; frame-ancestors 'none'; base-uri 'none'"
			}

			h.Set("Content-Security-Policy", effectiveCSP)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", ref)
			h.Set("Permissions-Policy", perms)

			// HSTS is only safe to advertise over HTTPS and only in
			// production. In dev, emitting HSTS on localhost can pin
			// browsers to https://localhost for two years.
			if cfg.Production && hstsMaxAge > 0 {
				h.Set("Strict-Transport-Security",
					"max-age="+itoa(hstsMaxAge)+"; includeSubDomains")
			}

			next.ServeHTTP(w, r)
		})
	}
}

// itoa avoids pulling in strconv just for a header value.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
