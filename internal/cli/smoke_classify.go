package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// smokeResult is the verdict for one probed route. The zero value is not
// meaningful — Status is always one of the smokeStatus* constants after
// classification.
type smokeStatus string

const (
	smokeStatusPass smokeStatus = "PASS"
	smokeStatusWarn smokeStatus = "WARN"
	smokeStatusFail smokeStatus = "FAIL"
)

// smoke failure/warn reason classes. Stable strings — the --json output
// and any CI consumer key off these, so treat them as an additive
// contract (new reasons may appear; existing ones don't change meaning).
const (
	smokeReasonTLS      = "tls-transport"   // FAIL: handshake error / reset / no response
	smokeReasonMisroute = "likely-misroute" // WARN: plain Go-mux 404 (reached wrong backend)
	smokeReasonReached  = "reached-backend" // PASS: structured response from a backend
	smokeReasonCORS     = "cors-missing"    // FAIL: API route the frontend calls lacks ACAO
	smokeReasonNoAddr   = "gateway-no-address"
	// smokeReasonUnreachable is the dev (port-based) transport FAIL: a
	// localhost:<port> dial that didn't connect (connection refused /
	// timeout / EOF). Distinct from smokeReasonTLS because the dev path is
	// PLAINTEXT — there is no TLS handshake to blame, so "tls-transport"
	// would mislead. The recurring dead :28090 controller route lands here.
	smokeReasonUnreachable = "port-unreachable"
)

// smokeTarget is one probe the smoke command will issue: a route's
// (host, path) reachable through a specific gateway's external IP. The
// origin, when non-empty, is the frontend origin to assert CORS against
// for this target (API routes only).
type smokeTarget struct {
	RouteKind string // "http" | "grpc"
	RouteName string
	Gateway   string
	Host      string // the DECLARED route host (shown in the table)
	// ProbeHost is the concrete host the probe dials / sets as SNI + Host
	// header. It equals Host except for a wildcard route (`*.example.com`),
	// where the literal `*` can't TLS-handshake against a real cert, so it
	// is substituted with a sample label (smokeWildcardLabel.example.com)
	// that the wildcard cert covers and the host route matches.
	ProbeHost string
	Path      string
	Origin    string // frontend origin to CORS-check against; empty = skip CORS
}

// smokeWildcardLabel is the sample subdomain label substituted for the
// leading `*` of a wildcard route host, so the probe targets a concrete
// name the wildcard cert + host route both accept.
const smokeWildcardLabel = "smoke-probe"

// smokeRouteResult is the classified outcome for one target.
type smokeRouteResult struct {
	Target     smokeTarget
	Status     smokeStatus
	Reason     string
	StatusCode int    // HTTP status when a response was received; 0 on transport failure
	Detail     string // human-readable note (error text, content-type, ACAO presence)
}

// extractSmokeTargets turns a rendered Bundle into the ordered list of
// probe targets, one per HTTPRoute + GRPCRoute the probe can address.
//
// Two host topologies exist and BOTH must be probed:
//
//   - Route-hosted (single shared gateway): each route declares its own
//     `host` and the gateway's host is empty. The route host is the
//     address. (prod/preprod: one `public` gateway, three host-bearing
//     routes.)
//   - Gateway-hosted (per-host gateways): the host lives on the GATEWAY
//     (`public-admin` → admin-staging.…) and the routes attached to it
//     carry an empty host. (staging: public / public-admin / public-grpc,
//     routes with empty host.)
//
// So a route's EFFECTIVE host is its own `host` when set, else its
// gateway's host. Dropping empty-host routes (the old behavior) silently
// skipped every gateway-hosted route — on staging that hid two of the
// three gateways (admin + grpc), so smoke only probed `public`. We now
// resolve the host from the attached gateway, so every gateway's routes
// are probed against that gateway's own IP (resolveGatewayIPs already
// keys IPs per gateway).
//
// CORS scoping (Fix): the Origin (which arms the CORS assertion) is set
// ONLY on routes the frontend actually calls — those whose effective
// host matches one of the frontend's declared API hosts (parsed from the
// Frontend env vars VITE_API_URL / VITE_CONTROL_PLANE_API_URL /
// NEXT_PUBLIC_API_URL). A non-API route (the workspace-proxy daemon
// port-forward host, a gRPC host, a wildcard proxy host) gets the
// transport check only — it legitimately serves no CORS and must not
// FAIL for a missing Access-Control-Allow-Origin. When no frontend API
// host is derivable the CORS check is skipped for every route (we never
// guess an origin).
//
// Pure: no I/O. The gateway IP is resolved later (live cluster read), so
// extraction is unit-testable from a sample Bundle alone.
func extractSmokeTargets(e *KCLEntities) []smokeTarget {
	if e == nil {
		return nil
	}
	gatewayHosts := gatewayHostIndex(e)
	origin := frontendOrigin(e)
	apiHosts := frontendAPIHosts(e)

	var out []smokeTarget
	add := func(kind, name, gateway, host, path string) {
		// Effective host: the route's own host, or its gateway's host
		// when the route is hostless (gateway-hosted topology).
		host = strings.TrimSpace(host)
		if host == "" {
			host = gatewayHosts[gateway]
		}
		if host == "" {
			// Still no host: a path-prefix mount on a hostless shared
			// listener — the probe can't address it pre-DNS without a host
			// header to set. Skip.
			return
		}
		// CORS is asserted only against routes the frontend calls.
		targetOrigin := ""
		if origin != "" && hostMatchesAPI(host, apiHosts) {
			targetOrigin = origin
		}
		out = append(out, smokeTarget{
			RouteKind: kind,
			RouteName: name,
			Gateway:   gateway,
			Host:      host,
			ProbeHost: probeHostFor(host),
			Path:      normalizeSmokePath(path),
			Origin:    targetOrigin,
		})
	}
	for _, r := range e.HTTPRoutes {
		add("http", r.Name, r.Gateway, r.Host, r.Path)
	}
	for _, r := range e.GRPCRoutes {
		add("grpc", r.Name, r.Gateway, r.Host, r.Path)
	}
	return out
}

// gatewayHostIndex maps each gateway name to its declared host, so a
// hostless route can inherit the host of the gateway it attaches to (the
// gateway-hosted topology). Gateways with an empty host are still
// indexed (as "") — harmless; the lookup just falls through.
func gatewayHostIndex(e *KCLEntities) map[string]string {
	idx := make(map[string]string, len(e.Gateways))
	for _, g := range e.Gateways {
		idx[g.Name] = strings.TrimSpace(g.Host)
	}
	return idx
}

// frontendAPIHosts returns the set of API hostnames the frontend calls,
// parsed from the Frontend entities' env vars. We read the URL-bearing
// build-time vars the SPA/Next app points at its backend:
// VITE_API_URL, VITE_CONTROL_PLANE_API_URL, NEXT_PUBLIC_API_URL. The
// host (no scheme, no port) is extracted from each. This is what scopes
// the CORS assertion to the routes the browser actually cross-origin
// calls — every other route serves no CORS by design.
//
// Returns an empty set when no frontend declares one of these vars (CORS
// then skipped for the whole env). Deliberately does NOT fall back to the
// Firebase site / .web.app origin guess for the host match — that origin
// is the CALLER (the value we send in the Origin header), not an API host
// the backend serves.
func frontendAPIHosts(e *KCLEntities) map[string]struct{} {
	const (
		viteAPI        = "VITE_API_URL"
		viteControlAPI = "VITE_CONTROL_PLANE_API_URL"
		nextAPI        = "NEXT_PUBLIC_API_URL"
	)
	apiVarNames := map[string]struct{}{
		viteAPI:        {},
		viteControlAPI: {},
		nextAPI:        {},
	}
	hosts := map[string]struct{}{}
	for _, f := range e.Frontends {
		for _, ev := range f.EnvVars {
			if _, ok := apiVarNames[ev.Name]; !ok {
				continue
			}
			if h := hostFromURL(ev.Value); h != "" {
				hosts[strings.ToLower(h)] = struct{}{}
			}
		}
	}
	return hosts
}

// hostFromURL parses the hostname (no scheme, no port) out of a URL like
// "https://admin.example.com" → "admin.example.com". Tolerant of a bare
// host with no scheme. Returns "" when nothing host-shaped is found.
func hostFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	// No scheme — url.Parse treats the whole thing as a path. Retry with
	// a scheme prepended so Hostname() populates.
	if u, err := url.Parse("https://" + raw); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return ""
}

// hostMatchesAPI reports whether a route's effective host is one of the
// frontend's API hosts. A wildcard route host (`*.foo.com`) is never an
// API host the frontend calls by name, so it never matches (the literal
// `*` can't equal a concrete API hostname) — wildcard proxy hosts get the
// transport check only, never CORS.
func hostMatchesAPI(host string, apiHosts map[string]struct{}) bool {
	if len(apiHosts) == 0 {
		return false
	}
	_, ok := apiHosts[strings.ToLower(strings.TrimSpace(host))]
	return ok
}

// probeHostFor returns the concrete host the probe should dial for a
// declared route host. For a wildcard host (`*.example.com`) the literal
// `*` is replaced with smokeWildcardLabel so the probe targets a real
// name the wildcard cert covers; any other host is returned unchanged.
func probeHostFor(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "*.") {
		return smokeWildcardLabel + host[1:] // "*.example.com" -> "smoke-probe.example.com"
	}
	return host
}

// normalizeSmokePath defaults an empty route path to "/" and ensures a
// leading slash. The probe URL is https://<host><path>, so a missing or
// relative path would otherwise produce a malformed target.
func normalizeSmokePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

// frontendOrigin derives the browser origin the frontend serves from, so
// API routes can be CORS-checked against it. Today the only derivable
// source is a Firebase Hosting frontend: site `<site>` → https://<site>.web.app.
// Returns "" when no frontend declares a derivable origin (CORS check is
// then skipped — absence of a frontend is not a failure).
//
// Model note: there is no first-class "frontend origin" / custom-domain
// field on FrontendEntity — we reconstruct the .web.app origin from the
// Firebase site name. A custom production domain (the common real case)
// is invisible here, so the CORS check only covers the default Firebase
// origin. A `Frontend.origins []string` (or a custom_domain field) would
// make this exact rather than reconstructed.
func frontendOrigin(e *KCLEntities) string {
	for _, f := range e.Frontends {
		if f.Runtime.Firebase == nil {
			continue
		}
		site := strings.TrimSpace(f.Runtime.Firebase.Site)
		if site == "" {
			continue
		}
		return "https://" + site + ".web.app"
	}
	return ""
}

// classifyTransportError maps a probe transport error (dial/TLS/read
// failure, i.e. no usable HTTP response) to the TLS/transport FAIL class.
// This is the ERR_CONNECTION_CLOSED bucket: a stuck cert, an
// un-programmed gateway, or a reset all land here.
func classifyTransportError(err error) smokeRouteResult {
	return smokeRouteResult{
		Status: smokeStatusFail,
		Reason: smokeReasonTLS,
		Detail: transportErrorDetail(err),
	}
}

// transportErrorDetail trims a raw transport error to a short, stable
// human note. Full URLs / IPs in the wrapped error are noise in the
// table; the class (tls-transport) already carries the meaning.
func transportErrorDetail(err error) string {
	if err == nil {
		return "no response"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "tls:"), strings.Contains(msg, "certificate"), strings.Contains(msg, "handshake"):
		return "TLS handshake failed"
	case strings.Contains(msg, "connection reset"):
		return "connection reset"
	case strings.Contains(msg, "connection refused"):
		return "connection refused"
	case strings.Contains(msg, "EOF"):
		return "connection closed (EOF)"
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline exceeded"):
		return "timeout"
	default:
		return msg
	}
}

// classifyResponse classifies a received HTTP response (transport
// succeeded — TLS + routing worked) into PASS vs the misroute WARN. The
// route path is taken into account: a plain Go-mux 404 at a *declared
// non-root path* is the misroute signal, but the same 404 at the root
// "/" is expected for a Connect/RPC backend (root is almost never a
// served path) and only proves the backend was reached.
//
// The signal that matters most is TRANSPORT: any structured response
// proves TLS + routing reached a backend. The one response that flags a
// problem is the Go default mux 404 — a *plain* `404 text/plain` body of
// "404 page not found" — at a path the route deliberately points at: the
// host reached *a* backend that doesn't serve that path (the reliant.v1.*
// → admin-server misroute). That's a WARN: routing works but points
// somewhere wrong.
//
// Everything else — 200/401/403/415, a Connect error envelope, a
// structured (JSON / non-default) 404, OR a plain root-path 404 — is
// PASS: the backend answered.
func classifyResponse(statusCode int, contentType, body string) smokeRouteResult {
	return classifyResponseForPath(statusCode, contentType, body, "/non-root")
}

// classifyResponseForPath is classifyResponse with the route path in
// hand, so a plain root-path 404 is distinguished from a plain 404 at a
// declared sub-path. classifyResponse defaults to the non-root treatment
// (the strict reading) for callers that don't carry a path.
func classifyResponseForPath(statusCode int, contentType, body, path string) smokeRouteResult {
	if isPlainGoMux404(statusCode, contentType, body) {
		if isRootPath(path) {
			return smokeRouteResult{
				Status:     smokeStatusPass,
				Reason:     smokeReasonReached,
				StatusCode: statusCode,
				Detail:     "backend reached (plain 404 at root path — expected for a Connect/RPC backend)",
			}
		}
		return smokeRouteResult{
			Status:     smokeStatusWarn,
			Reason:     smokeReasonMisroute,
			StatusCode: statusCode,
			Detail:     "plain 404 (default Go mux) — host reached a backend that doesn't serve this declared path",
		}
	}
	return smokeRouteResult{
		Status:     smokeStatusPass,
		Reason:     smokeReasonReached,
		StatusCode: statusCode,
		Detail:     fmt.Sprintf("backend answered (%s)", emptyAs(contentType, "no content-type")),
	}
}

// isRootPath reports whether the route path is the bare root mount, where
// a plain 404 is expected (a Connect/RPC backend serves
// /<package>.<Service>/<Method>, not "/").
func isRootPath(path string) bool {
	p := strings.TrimSpace(path)
	return p == "" || p == "/"
}

// isPlainGoMux404 detects the http.NotFound default response: status 404,
// a text/plain content-type, and the literal "404 page not found" body.
// A structured 404 (JSON, Connect envelope, custom page) is NOT this —
// that backend deliberately answered, so it's a PASS.
func isPlainGoMux404(statusCode int, contentType, body string) bool {
	if statusCode != http.StatusNotFound {
		return false
	}
	ct := strings.ToLower(strings.TrimSpace(contentType))
	// http.NotFound sets "text/plain; charset=utf-8".
	if !strings.HasPrefix(ct, "text/plain") {
		return false
	}
	return strings.Contains(strings.TrimSpace(body), "404 page not found")
}

// applyCORSVerdict escalates a transport-PASS result to a CORS FAIL when
// the target carries a frontend origin (an API route the frontend calls)
// but the response carried no Access-Control-Allow-Origin. A missing
// ACAO on a route the browser must call is a real, browser-only bug —
// it's a FAIL, not a warning.
//
// CORS is only asserted on PASS results: a route that already failed
// transport (TLS) or warned (misroute) has a more fundamental problem;
// layering a CORS verdict on top would bury the root cause. The header
// is read case-insensitively (http.Header canonicalizes, but a raw map
// from a test may not).
func applyCORSVerdict(res smokeRouteResult, origin string, respHeaders http.Header) smokeRouteResult {
	if origin == "" || res.Status != smokeStatusPass {
		return res
	}
	if hasACAO(respHeaders) {
		res.Detail += "; CORS ok"
		return res
	}
	return smokeRouteResult{
		Target:     res.Target,
		Status:     smokeStatusFail,
		Reason:     smokeReasonCORS,
		StatusCode: res.StatusCode,
		Detail:     fmt.Sprintf("backend answered but no Access-Control-Allow-Origin for frontend origin %s", origin),
	}
}

// hasACAO reports whether the response advertises any
// Access-Control-Allow-Origin (the wildcard "*" or an explicit origin
// both satisfy it for a probe — we assert the header's presence, not an
// exact echo, because servers legitimately answer "*").
func hasACAO(h http.Header) bool {
	if h == nil {
		return false
	}
	for k, vals := range h {
		if strings.EqualFold(k, "Access-Control-Allow-Origin") {
			for _, v := range vals {
				if strings.TrimSpace(v) != "" {
					return true
				}
			}
		}
	}
	return false
}

// smokeSummary tallies the per-route results into the summary line and
// the overall exit verdict (anyFail). Results are sorted for stable
// output ordering.
type smokeSummary struct {
	Pass    int
	Warn    int
	Fail    int
	AnyFail bool
}

func summarizeSmoke(results []smokeRouteResult) smokeSummary {
	var s smokeSummary
	for _, r := range results {
		switch r.Status {
		case smokeStatusPass:
			s.Pass++
		case smokeStatusWarn:
			s.Warn++
		case smokeStatusFail:
			s.Fail++
			s.AnyFail = true
		}
	}
	return s
}

// sortSmokeResults orders results by gateway, then host, then path for a
// stable, scannable table.
func sortSmokeResults(results []smokeRouteResult) {
	sort.SliceStable(results, func(i, j int) bool {
		a, b := results[i].Target, results[j].Target
		if a.Gateway != b.Gateway {
			return a.Gateway < b.Gateway
		}
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		return a.Path < b.Path
	})
}
