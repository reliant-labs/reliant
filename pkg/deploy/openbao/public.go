package openbao

// The public endpoint (Spec.Public): OpenBao published on a Gateway so clusters
// elsewhere can read from it. Everything here is derived from the Spec and is
// as pure and deterministic as the rest of the package.
//
// The shape, in the order a request meets it:
//
//	client -> Gateway listener (TLS terminated, ClientTrafficPolicy)
//	       -> HTTPRoute (the allowlist: three paths, nothing else is routed)
//	       -> BackendTrafficPolicy (body cap, rate limits)
//	       -> BackendTLSPolicy (re-encrypt, verify OpenBao's certificate)
//	       -> OpenBao's Gateway listener: its own TLS port, which the NetworkPolicy
//	          opens to that Gateway's proxy pods and to nothing else, and which is
//	          the only listener that trusts X-Forwarded-For
//
// Envoy Gateway v1.9.2 (internal/templates/ingress/envoy/VERSION) is the only
// controller forge installs, and every field below is one its translator
// accepted: the objects were run through `egctl x translate` from that tag, and
// the upstream TLS context and route table it emits were run in Envoy 1.39.1
// (the proxy image v1.9.2 ships) against a real OpenBao (public_smoke_test.go).

import (
	"cmp"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/reliant-labs/forge/pkg/secretaccess"
)

// Public is Spec.Public. The KCL lowering fills it from forge.OpenBaoPublic and
// from the Gateway that field names.
type Public struct {
	// Host is the DNS name clients dial.
	Host string `json:"host"`
	// Gateway, Listener and GatewayNamespace say where the route attaches. The
	// listener is the HTTPS one forge adds to the Gateway for Host.
	Gateway          string `json:"gateway"`
	GatewayNamespace string `json:"gatewayNamespace"`
	Listener         string `json:"listener"`

	// RequestsPerSecond is the ceiling on all sources together,
	// PerSourceRequestsPerSecond on one source address, MaxConnections on the
	// listener. All are per Envoy replica.
	RequestsPerSecond          int `json:"requestsPerSecond"`
	PerSourceRequestsPerSecond int `json:"perSourceRequestsPerSecond"`
	MaxConnections             int `json:"maxConnections"`
	// RequestTimeout is a Gateway API duration (30s, 1m30s).
	RequestTimeout string `json:"requestTimeout"`

	// Port is the TLS port OpenBao opens for the Gateway, beside its
	// Listeners. Only the Gateway's proxy pods can reach it.
	Port int `json:"port"`
	// ProxyNamespace is where Envoy Gateway runs the proxy pods; the
	// NetworkPolicy admits the pods of Gateway there and nothing else.
	ProxyNamespace string `json:"proxyNamespace"`
	// TrustedProxyCIDRs are the peer ranges OpenBao trusts to set
	// X-Forwarded-For on Port. A peer outside them is refused (HTTP 400), as is
	// a request with no header: nothing but Envoy should be talking to Port.
	TrustedProxyCIDRs []string `json:"trustedProxyCIDRs"`
}

const (
	gatewayAPIGroup   = "gateway.networking.k8s.io"
	envoyGatewayGroup = "gateway.envoyproxy.io"
	// publicPortName names the Gateway listener's Service and container port.
	publicPortName = "public"
	// publicMaxBody caps a request body at the edge. The largest legitimate one
	// is a login: a role name and a service-account JWT, a few KiB.
	publicMaxBody = "16Ki"

	segmentPattern   = `[A-Za-z0-9][A-Za-z0-9._-]*`
	namePattern      = `[A-Za-z_][A-Za-z0-9_]*`
	clusterIDPattern = `[A-Za-z0-9][A-Za-z0-9_-]*`
)

var (
	publicHostRe     = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]*[a-z0-9])?\.)+[a-z]{2,}$`)
	gatewayDurationR = regexp.MustCompile(`^([0-9]{1,5}(h|m|s|ms)){1,4}$`)
)

// PublicMatch is one entry of the route's allowlist, as an HTTPRoute match.
type PublicMatch struct {
	Method string
	// Type is a Gateway API path match type: Exact or RegularExpression. The
	// allowlist never uses PathPrefix.
	Type  string
	Value string
}

// PublicAllowlist is the whole of what the public host routes. Anything else,
// whatever the method, matches no route and Envoy answers 404.
//
//   - Login is PUT and POST: ESO's Vault client writes with PUT (the design
//     said POST; OpenBao treats them alike).
//   - GET auth/token/lookup-self is what ESO calls to validate a SecretStore
//     and to decide whether to log in again. Without it every SecretStore on a
//     remote cluster stays NotReady. It reveals nothing but the caller's own
//     token, which the caller holds.
//   - The read is one KV value of one env. The patterns are the ones
//     pkg/secretaccess enforces on write (a test pins them to it), so the edge
//     never routes a path no policy could grant.
//
// Query strings are not part of the match, so `?version=N` (a pinned read)
// passes.
func (s Spec) PublicAllowlist() []PublicMatch {
	root := cmp.Or(s.Access.Root, secretaccess.DefaultRoot)
	login := `^/v1/auth/` + secretaccess.MountPrefix + clusterIDPattern + `/login$`
	read := `^/v1/` + regexp.QuoteMeta(s.KV.Mount) + `/data/` + regexp.QuoteMeta(root) +
		`/` + segmentPattern + `/` + segmentPattern + `/` + namePattern + `$`
	return []PublicMatch{
		{"PUT", "RegularExpression", login},
		{"POST", "RegularExpression", login},
		{"GET", "Exact", "/v1/auth/token/lookup-self"},
		{"GET", "RegularExpression", read},
	}
}

func (s Spec) publicName() string { return s.Name + "-public" }

func (s Spec) validatePublic() []error {
	p := s.Public
	if p == nil {
		return nil
	}
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf("public: "+format, a...)) }

	if !publicHostRe.MatchString(p.Host) {
		bad("host %q is not a DNS name. Set public.host to the name clients dial (secrets.example.com)", p.Host)
	}
	for _, f := range []struct{ what, v string }{
		{"gateway", p.Gateway}, {"listener", p.Listener},
	} {
		if f.v == "" {
			bad("%s is required: it names where the route attaches", f.what)
		}
	}
	for _, f := range []struct{ what, v string }{
		{"gatewayNamespace", p.GatewayNamespace}, {"proxyNamespace", p.ProxyNamespace},
	} {
		if !dnsLabel.MatchString(f.v) {
			bad("%s %q must be an RFC-1123 label", f.what, f.v)
		}
	}
	if p.RequestsPerSecond < 1 || p.PerSourceRequestsPerSecond < 1 || p.MaxConnections < 1 {
		bad("requestsPerSecond, perSourceRequestsPerSecond and maxConnections must each be at least 1: a limit of zero closes the endpoint. Unpublish the store instead of setting one to 0")
	}
	if p.PerSourceRequestsPerSecond > p.RequestsPerSecond {
		bad("perSourceRequestsPerSecond (%d) exceeds requestsPerSecond (%d): one source could take the whole budget. Lower perSourceRequestsPerSecond or raise requestsPerSecond", p.PerSourceRequestsPerSecond, p.RequestsPerSecond)
	}
	if !gatewayDurationR.MatchString(p.RequestTimeout) {
		bad("requestTimeout %q is not a Gateway API duration: whole numbers with h, m, s or ms (30s, 1m30s). Fractions and microseconds are refused by the API server", p.RequestTimeout)
	}
	if len(p.TrustedProxyCIDRs) == 0 {
		bad("trustedProxyCIDRs is empty: the Gateway listener refuses a peer it does not trust, so it would refuse every request. Name the ranges Envoy Gateway's proxy pods get addresses from (your pod CIDR)")
	}
	for _, c := range p.TrustedProxyCIDRs {
		if _, err := netip.ParsePrefix(c); err != nil {
			bad("trustedProxyCIDRs entry %q is not a CIDR (10.0.0.0/8). Fix it", c)
		}
	}
	if p.Port < 1 || p.Port > 65535 {
		bad("port %d must be 1..65535", p.Port)
	}
	if p.Port == ClusterPort {
		bad("port %d is the cluster port. Pick another", p.Port)
	}
	for _, l := range s.Listeners {
		if l.Port == p.Port {
			bad("port %d is already listener %d's. The Gateway gets a listener of its own so that only it is trusted to set X-Forwarded-For: pick a port no listener uses", p.Port, l.Port)
		}
	}
	return errs
}

// publicObjects are the Gateway-side and Service-side objects of the endpoint.
// The Gateway and its listener are rendered by KCL (they belong to the env's
// ingress); these hang off them.
func (s Spec) publicObjects() []any {
	p := s.Public
	if p == nil {
		return nil
	}
	var out []any
	if p.GatewayNamespace != s.Namespace {
		out = append(out, s.publicReferenceGrant())
	}
	return append(out, s.publicBackendTLSPolicy(), s.publicHTTPRoute(), s.publicBackendTrafficPolicy(), s.publicClientTrafficPolicy())
}

func (s Spec) metaIn(namespace, name string) map[string]any {
	return map[string]any{"name": name, "namespace": namespace, "labels": toAnyMap(s.labels())}
}

// A route may point at a Service in another namespace only if that namespace
// says so. The grant names the one Service, so it cannot be read as access to
// anything else OpenBao's namespace holds.
func (s Spec) publicReferenceGrant() map[string]any {
	return map[string]any{
		"apiVersion": gatewayAPIGroup + "/v1beta1", "kind": "ReferenceGrant",
		"metadata": s.metaIn(s.Namespace, s.publicName()+"-from-"+s.Public.GatewayNamespace),
		"spec": map[string]any{
			"from": []any{map[string]any{"group": gatewayAPIGroup, "kind": "HTTPRoute", "namespace": s.Public.GatewayNamespace}},
			"to":   []any{map[string]any{"group": "", "kind": "Service", "name": s.Name}},
		},
	}
}

// The Gateway re-encrypts to OpenBao and verifies it: the CA is the one the
// converge Job publishes (or the one the BYO certificate's owner published) as
// tls.caConfigMap, and the name checked, and sent as SNI, is the Service's own
// DNS name, which the server certificate always carries. So the public host
// never has to appear in the internal CA's certificate. The policy lives in the
// Service's namespace, as the API requires.
func (s Spec) publicBackendTLSPolicy() map[string]any {
	return map[string]any{
		"apiVersion": gatewayAPIGroup + "/v1", "kind": "BackendTLSPolicy",
		"metadata": s.metaIn(s.Namespace, s.publicName()),
		"spec": map[string]any{
			"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": s.Name, "sectionName": publicPortName}},
			"validation": map[string]any{
				"hostname":          s.Host(),
				"caCertificateRefs": []any{map[string]any{"group": "", "kind": "ConfigMap", "name": s.TLS.CAConfigMap}},
			},
		},
	}
}

func (s Spec) publicHTTPRoute() map[string]any {
	p := s.Public
	var matches []any
	for _, m := range s.PublicAllowlist() {
		matches = append(matches, map[string]any{"method": m.Method, "path": map[string]any{"type": m.Type, "value": m.Value}})
	}
	return map[string]any{
		"apiVersion": gatewayAPIGroup + "/v1", "kind": "HTTPRoute",
		"metadata": s.metaIn(p.GatewayNamespace, s.publicName()),
		"spec": map[string]any{
			"parentRefs": []any{map[string]any{"name": p.Gateway, "sectionName": p.Listener}},
			"hostnames":  []any{p.Host},
			"rules": []any{map[string]any{
				"matches":     matches,
				"backendRefs": []any{map[string]any{"name": s.Name, "namespace": s.Namespace, "port": int64(p.Port)}},
				"timeouts":    map[string]any{"request": p.RequestTimeout},
			}},
		},
	}
}

// The rate limits are Envoy's local limiter, so per proxy replica.
//
// The total is a bucket that matches every request (path prefix /), NOT the
// rule-with-no-selector Envoy Gateway calls the default: with a per-source rule
// that matches every address, the default bucket is never consumed
// (alwaysConsumeDefaultTokenBucket=false), so a "total" declared that way caps
// nothing. Measured on Envoy 1.39.1: a default of 10/s beside a per-source 40/s
// let one source through at 40/s.
//
// Per-source buckets come as an IPv4 and an IPv6 rule because a CIDR selector
// matches one family; without ::/0 an IPv6 client has no per-source bucket.
// Envoy takes the smallest-rate bucket first, so a source that exhausts its own
// bucket is refused before it can draw on the total.
func (s Spec) publicBackendTrafficPolicy() map[string]any {
	p := s.Public
	rule := func(selector map[string]any, rps int) map[string]any {
		return map[string]any{
			"clientSelectors": []any{selector},
			"limit":           map[string]any{"requests": int64(rps), "unit": "Second"},
		}
	}
	perSource := func(cidr string) map[string]any {
		return rule(map[string]any{"sourceCIDR": map[string]any{"type": "Distinct", "value": cidr}}, p.PerSourceRequestsPerSecond)
	}
	return map[string]any{
		"apiVersion": envoyGatewayGroup + "/v1alpha1", "kind": "BackendTrafficPolicy",
		"metadata": s.metaIn(p.GatewayNamespace, s.publicName()),
		"spec": map[string]any{
			"targetRefs":    []any{map[string]any{"group": gatewayAPIGroup, "kind": "HTTPRoute", "name": s.publicName()}},
			"requestBuffer": map[string]any{"limit": publicMaxBody},
			"rateLimit": map[string]any{"type": "Local", "local": map[string]any{"rules": []any{
				rule(map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/"}}, p.RequestsPerSecond),
				perSource("0.0.0.0/0"),
				perSource("::/0"),
			}}},
		},
	}
}

// The listener's policy. Envoy is this endpoint's edge, so it must not believe
// a client's X-Forwarded-For: the header is removed before Envoy appends the
// address of the peer it sees, which leaves exactly one entry, the client. That
// one entry is what OpenBao's audit log records (see publicListenerConfig) and
// what the per-source limits key on. Escaped slashes are refused so that the
// path Envoy matches is the path OpenBao receives.
func (s Spec) publicClientTrafficPolicy() map[string]any {
	p := s.Public
	return map[string]any{
		"apiVersion": envoyGatewayGroup + "/v1alpha1", "kind": "ClientTrafficPolicy",
		"metadata": s.metaIn(p.GatewayNamespace, s.publicName()),
		"spec": map[string]any{
			"targetRefs": []any{map[string]any{"group": gatewayAPIGroup, "kind": "Gateway", "name": p.Gateway, "sectionName": p.Listener}},
			"connection": map[string]any{"connectionLimit": map[string]any{"value": int64(p.MaxConnections)}},
			"timeout":    map[string]any{"http": map[string]any{"requestReceivedTimeout": p.RequestTimeout}},
			"path":       map[string]any{"escapedSlashesAction": "RejectRequest"},
			"headers":    map[string]any{"earlyRequestHeaders": map[string]any{"remove": []any{"x-forwarded-for"}}},
		},
	}
}

// publicIngressRule admits the proxy pods of the one Gateway, on the one TLS
// port they dial. It is a rule of its own, so the Gateway is not also admitted
// to a transitional plaintext listener.
func (s Spec) publicIngressRule() (networkingv1.NetworkPolicyIngressRule, bool) {
	p := s.Public
	if p == nil {
		return networkingv1.NetworkPolicyIngressRule{}, false
	}
	tcp := corev1.ProtocolTCP
	port := intstr.FromInt32(int32(p.Port))
	return networkingv1.NetworkPolicyIngressRule{
		From: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: nsSelector(p.ProxyNamespace),
			PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"app.kubernetes.io/name":                         "envoy",
				"app.kubernetes.io/component":                    "proxy",
				"app.kubernetes.io/managed-by":                   "envoy-gateway",
				"gateway.envoyproxy.io/owning-gateway-name":      p.Gateway,
				"gateway.envoyproxy.io/owning-gateway-namespace": p.GatewayNamespace,
			}},
		}},
		Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &port}},
	}, true
}

// publicListener is the listener the Gateway dials. It is separate from the
// ones in-cluster callers use so that X-Forwarded-For can be trusted on it and
// nowhere else: the NetworkPolicy admits only the Gateway's proxy pods to this
// port, so no other client can set the header. Both refusals are set EXPLICITLY:
// OpenBao's documentation says they default to true, but the code (helper/
// configutil/listener.go, v2.6.2) reads them as plain bools, so unset they are
// false, and a request from outside the trusted ranges would be served with its
// header dropped instead of refused. A peer outside the trusted ranges, or a
// request with no header, is refused with 400. Envoy removes any client-sent header and appends
// the address of the peer it sees (publicClientTrafficPolicy), so what OpenBao
// takes as the client, the last entry, is the address that connected to the
// Gateway, and its audit log names it instead of the proxy.
//
// Metrics stay off this port; the allowlist would not route them either.
func (s Spec) publicListener() string {
	if s.Public == nil {
		return ""
	}
	q := make([]string, len(s.Public.TrustedProxyCIDRs))
	for i, c := range s.Public.TrustedProxyCIDRs {
		q[i] = strconv.Quote(c)
	}
	return fmt.Sprintf("listener \"tcp\" {\n  address = \"0.0.0.0:%d\"\n  tls_cert_file = \"%s/tls.crt\"\n  tls_key_file = \"%s/tls.key\"\n  tls_min_version = \"tls12\"\n  x_forwarded_for_authorized_addrs = [%s]\n  x_forwarded_for_reject_not_authorized = true\n  x_forwarded_for_reject_not_present = true\n  telemetry {\n    disallow_metrics = true\n  }\n}\n\n",
		s.Public.Port, tlsDir, tlsDir, strings.Join(q, ", "))
}
