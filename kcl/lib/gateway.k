"""
Forge KCL — Gateway API manifest builders.

Pure render functions that take typed Gateway / HTTPRoute / GRPCRoute
entities and emit the corresponding gateway.networking.k8s.io/v1
manifests. The render layer (render.k) walks these out of the Bundle
and inlines them into render_manifests' output list.

KCL package-identity note: same workaround as lib/services.k — lambda
params are typed `any` so the parent-package schemas are accepted as
duck-typed inputs. Callers are responsible for passing
already-validated entities.
"""

import .labels as labels_lib

# Ingress objects are DELIBERATELY not part of the app project: a Gateway
# and its routes are shared ingress topology, not a workload of one
# release, so they group under a fixed `forge-ingress` rather than under
# `Bundle.project`. That is why this passes a constant `part_of` instead of
# threading the project through — the value differs, the renderer does not.
_managed_labels = lambda name: str -> {str: str} {
    labels_lib.managed_labels(name, "forge-ingress")
}

# cluster_routing_label is the dedicated wire key the Go multi-cluster
# scoper (internal/cluster.ScopeManifestsToGroup) reads to route a
# manifest to ONE cluster directly. When a Gateway / HTTPRoute /
# GRPCRoute carries `cluster = <forge.Cluster>`, the builder stamps
# `forge.dev/cluster: <context>` (the DENORMALIZED kubectl context,
# `k3d-<name>`) on the rendered manifest's labels; the scoper keeps that
# manifest ONLY on the named cluster and drops it from every other. This
# is the first-class replacement for the older label-piggyback trick
# (stamping `app.kubernetes.io/name = <some service>` so the manifest
# routes via that service's group). A manifest WITHOUT this label still
# routes by `app.kubernetes.io/name` exactly as before, so existing
# consumers are unaffected.
#
# The `cluster` field is a forge.Cluster REFERENCE (not a bare name), so
# the builder reads its `.context` — this is what removed the
# name-vs-context footgun (a consumer once had to pass `k3d-control-plane`
# as a string, which only worked by coincidence). `_cluster_context`
# extracts that context from the (duck-typed) Cluster instance, or "" when
# unset; `_cluster_label` stamps the label with it.
_cluster_routing_label_key = "forge.dev/cluster"

_cluster_context = lambda cluster: any -> str {
    cluster.context if cluster else ""
}

_cluster_label = lambda context: str -> {str: str} {
    {"${_cluster_routing_label_key}" = context} if context else {}
}

# A route lands where it says, else where its parent Gateway lands: a route
# is attached to exactly one Gateway, and on any other cluster its parentRef
# resolves to nothing. Without the inheritance an unpinned route beside a
# pinned Gateway was replicated to every cluster of a multi-cluster env.
_route_cluster_context = lambda route: any, gw: any -> str {
    _own = _cluster_context(route.cluster)
    _own if _own else (_cluster_context(gw.cluster) if gw else "")
}

# Map a GatewayListener.protocol to the Gateway API spec value.
# H2C maps to Gateway API's HTTP protocol (HTTP/2 cleartext is
# expressed via the listener's underlying behavior; Envoy Gateway
# picks up h2c through the combination of HTTP + a GRPCRoute
# attachment and serves native gRPC bidi streams on it).
_listener_protocol = lambda p: str -> str {
    "HTTPS" if p == "HTTPS" else "HTTP"
}

# Effective hostname / TLS for ONE listener: the listener's own value
# when set, else the parent Gateway's default. This pair is the whole
# per-listener feature — everything downstream (listener render,
# Certificate grouping, route host resolution) reads through them, so
# the fallback rule is stated exactly once.
_listener_host = lambda l: any, gw_host: str -> str {
    l.hostname if l.hostname else gw_host
}

_listener_tls = lambda l: any, gw_tls: any -> any {
    l.tls if l.tls else gw_tls
}

_render_listener = lambda l: any, gw_tls: any, gw_host: str -> any {
    _host = _listener_host(l, gw_host)
    _tls = _listener_tls(l, gw_tls)
    {
        name = l.name
        port = l.port
        protocol = _listener_protocol(l.protocol)
        # An HTTPS listener MUST carry a `hostname` when the gateway has a
        # default host: the cert-manager gateway-shim iterates the listener
        # blocks to build per-host Certificates and REJECTS the whole
        # gateway config ("spec.listeners[*].hostname: Required value: the
        # hostname cannot be empty") for an HTTPS listener with no hostname,
        # which in turn wedges the cloud Gateway controller's reconcile.
        # It is also what the LB uses for SNI cert selection. Plaintext HTTP
        # listeners stay hostname-less (they serve all hosts, e.g. the ACME
        # HTTP-01 solver route).
        #
        # The hostname is the LISTENER's when it declares one, else the
        # gateway's — which is what lets one Gateway (one load balancer)
        # terminate several hostnames on separate listeners.
        if l.protocol == "HTTPS" and _host:
            hostname = _host
        # gke_certmap terminates TLS via a GCP Certificate Manager map
        # (stamped as an annotation on the Gateway, see render_gateway),
        # NOT a cluster Secret — so the HTTPS listener carries NO tls
        # block (no certificateRefs, no mode). The other modes reference
        # the Secret by name via certificateRefs.
        if l.protocol == "HTTPS" and _tls and _tls.mode != "gke_certmap":
            tls = {
                mode = "Terminate"
                certificateRefs = [
                    {
                        kind = "Secret"
                        name = _tls.secret_name
                    }
                ]
            }
        allowedRoutes = {namespaces = {"from" = "Same"}}
    }
}

# spec.allowedListeners — which ListenerSets may attach listeners to
# this Gateway. Gateway API denies all of them by default, so a Gateway
# that expects controller-managed listeners MUST say so.
#
# `selector` rides only the Selector form; the schema rejects setting it
# otherwise, so it cannot be silently ignored here.
_render_allowed_listeners = lambda al: any -> any {
    {
        namespaces = {
            from = al.from_namespaces
            if al.from_namespaces == "Selector":
                selector = {matchLabels = al.selector}
        }
    }
}

# A literal IP pin does NOT go in spec.addresses — see the block comment
# on render_gateway. These two helpers split a Gateway's addresses by the
# mechanism that carries them to the cluster.
_ip_addresses = lambda gw: any -> [any] {
    [a for a in gw.addresses if a.type == "IPAddress"]
}

_named_addresses = lambda gw: any -> [any] {
    [a for a in gw.addresses if a.type != "IPAddress"]
}

# The EnvoyProxy that carries a literal IP pin. Named off the Gateway so
# it is stable across renders and obvious in `kubectl get envoyproxy`,
# and placed in the GATEWAY's namespace because EG resolves
# infrastructure.parametersRef namespace-locally (processGatewayParamsRef
# does a plain Get in gtw.Namespace — a CR anywhere else is never found
# and the Gateway silently keeps an ephemeral address).
_lb_ip_proxy_name = lambda gw: any -> str {
    "${gw.name}-lb-ip"
}

# Render the EnvoyProxy for an IPAddress-pinned Gateway, or nothing.
#
# ONLY loadBalancerIP is set. EG already defaults envoyService.type to
# LoadBalancer and externalTrafficPolicy to Local
# (api/v1alpha1/kubernetes_helpers.go DefaultKubernetesService), so
# re-stating them here would take forge ownership of two fields it has
# no opinion about — and any later drift between forge's copy and EG's
# default would rewrite a live Service on the next reconcile.
render_gateway_lb_ip_proxy = lambda gw: any, namespace: str -> [any] {
    _ips = _ip_addresses(gw)
    [{
        apiVersion = "gateway.envoyproxy.io/v1alpha1"
        kind = "EnvoyProxy"
        metadata = {
            name = _lb_ip_proxy_name(gw)
            namespace = namespace
            labels = _managed_labels(gw.name) | _cluster_label(_cluster_context(gw.cluster))
        }
        spec = {
            provider = {
                type = "Kubernetes"
                kubernetes = {
                    envoyService = {
                        loadBalancerIP = _ips[0].value
                    }
                }
            }
        }
    }] if _ips else []
}

# Render a Gateway resource.
#
# PINNING A LOAD-BALANCER IP: two mechanisms, and picking the wrong one
# breaks the Gateway invisibly.
#
# Envoy Gateway translates every `spec.addresses` entry of type
# IPAddress into the backing Service's `spec.externalIPs`
# (internal/gatewayapi/address.go fills infra.Proxy.Addresses;
# resource_provider.go then does `serviceSpec.ExternalIPs =
# r.infra.Addresses` for any non-ClusterIP service). GKE runs the
# `DenyServiceExternalIPs` admission plugin, which REJECTS that Service:
#
#     services "envoy-..." is forbidden:
#     Use of external IPs is denied by admission control
#
# and the failure is near-silent. The Gateway keeps serving, because the
# Service already exists and the cloud LB is already attached — but EG
# can no longer update it, so the next listener port added to this
# Gateway never reaches the Service and never receives traffic. Nothing
# reports unhealthy; the port is just missing.
#
# So an IPAddress pin is carried by an EnvoyProxy instead
# (`provider.kubernetes.envoyService.loadBalancerIP`, bound to this one
# Gateway via `spec.infrastructure.parametersRef`), which lands on the
# Service as `spec.loadBalancerIP` — the field GCP actually reads to
# attach a reserved regional address, and one admission permits.
#
# A NamedAddress pin is left in `spec.addresses` untouched. It is a
# different mechanism for a different controller: the GKE *cloud*
# Gateway controller's extension, whose value is the NAME of a reserved
# GLOBAL reservation rather than an IP. EG's address translator ignores
# every non-IPAddress type, so it never becomes an externalIP.
#
# In cert_manager mode forge emits an EXPLICIT cert-manager Certificate
# (see render_certificate below) with full control over dnsNames /
# issuerRef. It therefore does NOT also stamp the
# `cert-manager.io/cluster-issuer` annotation on the Gateway: that
# annotation drives cert-manager's gateway-shim, which would auto-create
# a SECOND Certificate targeting the same Secret. With both present the
# shim's cert and forge's cert race two parallel ACME orders for one
# Secret — redundant churn at best, and the shim's stricter listener
# validation (every HTTPS listener must carry a hostname) can wedge the
# whole gateway reconcile. One mechanism only: the explicit Certificate.
#
# mkcert mode emits no Certificate and no annotation — the Secret is
# populated host-side by `forge cluster up`.
#
# gke_certmap mode stamps `networking.gke.io/certmap: <certmap>` on the
# Gateway: the GKE Gateway controller reads that annotation and binds the
# named GCP Certificate Manager map to the Gateway, which terminates TLS
# and serves the cert for each route hostname. No Certificate and no
# Secret are involved (the listener carries no tls block — see
# _render_listener).
render_gateway = lambda gw: any, namespace: str -> [any] {
    _labels = _managed_labels(gw.name) | _cluster_label(_cluster_context(gw.cluster))
    _annotations = {
        "networking.gke.io/certmap" = gw.tls.certmap
    } if gw.tls and gw.tls.mode == "gke_certmap" else {}
    _named = _named_addresses(gw)
    _ips = _ip_addresses(gw)
    [{
        apiVersion = "gateway.networking.k8s.io/v1"
        kind = "Gateway"
        metadata = {
            name = gw.name
            namespace = namespace
            labels = _labels
            if _annotations:
                annotations = _annotations
        }
        spec = {
            gatewayClassName = gw.gateway_class_name
            # NamedAddress pins only (GKE cloud controller, reserved
            # GLOBAL reservation by name). Omitted entirely when empty —
            # emitting `addresses: []` would churn existing gateways and
            # let the controller assign ephemerally. A literal IP is
            # DELIBERATELY absent here; it rides the EnvoyProxy below,
            # because in spec.addresses it becomes spec.externalIPs and
            # GKE admission rejects the Service.
            if _named:
                addresses = [{type = a.type, value = a.value} for a in _named]
            # Bind the EnvoyProxy that carries the literal IP pin.
            if _ips:
                infrastructure = {
                    parametersRef = {
                        group = "gateway.envoyproxy.io"
                        kind = "EnvoyProxy"
                        name = _lb_ip_proxy_name(gw)
                    }
                }
            # Which ListenerSets may attach listeners here. OMITTED when
            # unset, deliberately: Gateway API's own default is to allow
            # none, so rendering an empty block would state the default
            # as config and churn every existing Gateway for no change.
            if gw.allowed_listeners:
                allowedListeners = _render_allowed_listeners(gw.allowed_listeners)
            listeners = [_render_listener(l, gw.tls, gw.host) for l in gw.listeners]
        }
    }] + render_gateway_lb_ip_proxy(gw, namespace)
}

# Render a cert-manager Certificate for a Gateway with TLS. The
# Certificate populates the Secret referenced by the Gateway's HTTPS
# listener(s); cert-manager watches Certificate, issues against the
# named ClusterIssuer, and writes the resulting cert+key into the
# Secret the Gateway's listener references via tls.certificateRefs.
#
# mkcert-mode gateways skip this manifest entirely — `forge cluster
# up` populates the Secret host-side via the mkcert binary, so cert-
# manager doesn't need to know.
#
# When a Certificate's host set is empty it is emitted WITHOUT dnsNames;
# that config is invalid (cert-manager will surface the error at apply
# time) but failing the KCL render here would block render of every
# other manifest — defer to the cluster.
#
# CERTIFICATE STRATEGY: one Certificate per distinct SECRET, whose
# dnsNames are every host served by a listener referencing that secret.
#
# The secret is the right grouping key because it is precisely what a
# Gateway listener's `certificateRefs` points at — one Secret is one
# served certificate, so one Certificate must fill it. Grouping by
# anything else would either leave a referenced Secret unfilled or make
# two Certificates race to write the same one (the exact failure the
# gateway-shim comment above describes).
#
# It also puts the multi-SAN-vs-per-listener choice in the consumer's
# hands as a consequence of a decision they were already making, with no
# new knob: give two listeners DIFFERENT secret_names and each gets its
# own single-SAN Certificate; give them the SAME secret_name and they
# share one multi-SAN Certificate. Distinct secrets is the safer default
# and the shape forge's own consumers want — a wildcard SAN
# (`*.workspaces.example.com`) requires a DNS-01 solver, so folding it
# into a cert alongside HTTP-01-solvable hosts would constrain the whole
# certificate to the stricter issuer. Separate secrets keep each host's
# issuance independent, so one failing ACME order cannot take down the
# renewal of an unrelated hostname.
_cert_entries = lambda gw: any -> [any] {
    _https = [l for l in gw.listeners if l.protocol == "HTTPS"]
    _from_listeners = [
        {
            listener = l.name
            tls = _listener_tls(l, gw.tls)
            host = _listener_host(l, gw.host)
        } for l in _https if _listener_tls(l, gw.tls)
    ]
    # A Gateway that declares cert_manager TLS but has no HTTPS listener
    # still gets its Certificate (the listener may be added by an env
    # overlay, and the Secret is legitimately pre-provisioned).
    _fallback = [{listener = "", tls = gw.tls, host = gw.host}] if gw.tls else []
    [e for e in (_from_listeners if _from_listeners else _fallback) if e.tls.mode == "cert_manager"]
}

render_certificate = lambda gw: any, namespace: str -> [any] {
    _entries = _cert_entries(gw)
    # Group by the Secret each entry fills. Dict construction dedups on
    # key, so iterating the dict yields one group per distinct secret in
    # first-appearance order (deterministic output).
    _by_secret = {e.tls.secret_name: [x for x in _entries if x.tls.secret_name == e.tls.secret_name] for e in _entries}
    [_render_one_certificate(gw, namespace, _by_secret[_secret]) for _secret in _by_secret]
}

# Render the single Certificate that fills one Secret. `group` is every
# listener entry referencing that secret; they share an issuer (the
# Secret holds one cert, which one issuer signs) and contribute their
# hosts as SANs.
_render_one_certificate = lambda gw: any, namespace: str, group: [any] -> any {
    _first = group[0]
    # A Certificate named after the gateway alone would collide as soon
    # as a second Secret appears, so only the gateway-level cert keeps
    # the bare `<gateway>-tls` name; a listener-level override is
    # qualified by the listener that introduced it.
    _name = "${gw.name}-tls" if _first.listener == "" or (gw.tls and _first.tls.secret_name == gw.tls.secret_name) else "${gw.name}-${_first.listener}-tls"
    # Dedup hosts (two listeners on one secret often share a hostname —
    # e.g. :80 and :443) while preserving declaration order.
    _hosts = list({e.host: None for e in group if e.host})
    {
        apiVersion = "cert-manager.io/v1"
        kind = "Certificate"
        metadata = {
            name = _name
            namespace = namespace
            # The Certificate fills the Secret its Gateway's listener
            # references, so it lands on the Gateway's cluster and nowhere
            # else — a copy elsewhere is a second ACME order for the same
            # hosts, against the issuer's rate limit.
            labels = _managed_labels(gw.name) | _cluster_label(_cluster_context(gw.cluster))
        }
        spec = {
            secretName = _first.tls.secret_name
            issuerRef = {
                name = _first.tls.cert_issuer
                kind = "ClusterIssuer"
                group = "cert-manager.io"
            }
            if _hosts:
                dnsNames = _hosts
        }
    }
}

# Effective hostname for a route: the route's own override wins, then
# the LISTENER it attaches to, then the gateway default.
#
# The listener sits in the middle deliberately. A route override is the
# most specific statement of intent, but a listener that declares a
# hostname is what the load balancer actually serves on that section —
# so inheriting the gateway's default there would emit an HTTPRoute whose
# hostname does not match its own listener, and Gateway API would drop
# the route as unattachable.
_route_host = lambda route: any, gw: any, listener: any -> str {
    _listener_default = _listener_host(listener, gw.host) if listener else gw.host
    route.host if route.host else _listener_default
}

# Effective path: listener.path_prefix + route.path. Empty result
# collapses to "/" so the path-match isn't an empty string.
_route_path = lambda route: any, listener: any -> str {
    _prefix = listener.path_prefix
    _p = route.path
    _full = "${_prefix}${_p}" if _prefix or _p else ""
    _full if _full else "/"
}

# Resolve a route's target listener from the parent gateway, or None
# if the route names a listener that doesn't exist (KCL schema check
# catches that case earlier — this is defensive).
_lookup_listener = lambda gw: any, listener_name: str -> any {
    [l for l in gw.listeners if l.name == listener_name][0]
}

# Resolve a route's parent gateway by name. Returns None if the
# referenced gateway doesn't exist in this env (caller short-circuits).
_lookup_gateway = lambda gateways: [any], gateway_name: str -> any {
    _matches = [g for g in gateways if g.name == gateway_name]
    _matches[0] if _matches else None
}

# ---------------------------------------------------------------------------
# Backend resolution — where a route's traffic actually goes.
#
# A route names its backend as a SERVICE (a name, used verbatim) or as a
# WORKLOAD (resolved per env from the runtime that env binds it to). The
# workload case is the ADR-0003 F2 primitive: control-plane declares
# `/auth/token -> admin-server` ONCE and it renders as the Service in prod,
# where admin-server is cluster-bound, and as the HOST PROCESS in dev,
# where it is not.
#
# WHY A `Backend` CRD FOR THE HOST CASE. Envoy Gateway refuses an
# ExternalName Service as a backendRef outright — "Service ... is of type
# ExternalName, which is not supported as a backend; use an Envoy Gateway
# Backend resource with an FQDN endpoint instead" (verified against EG
# v1.9.2). So the host backend is a gateway.envoyproxy.io/v1alpha1 Backend
# with an fqdn endpoint at the cluster's host gateway. Nothing
# machine-specific is rendered: the host is the constant host.k3d.internal
# and the port is the workload's own declared listen_port.
# ---------------------------------------------------------------------------

# The k3d kubectl-context prefix. A route lands on a LOCAL cluster when the
# context it lands on is a k3d one — that is the whole signal, and it is
# declarative: k3d's own convention is `k3d-<name>`, which forge.Cluster
# denormalizes into `.context` and a ClusterTarget spells directly.
_K3D_CONTEXT_PREFIX = "k3d-"

# A host-process backend is only reachable from a cluster running on the
# SAME machine. On a cloud cluster `host.k3d.internal` resolves to nothing
# (or worse, to a node), so the route is refused rather than rendered into
# a 503 nobody can explain.
_is_local_cluster = lambda context: str -> bool {
    context.startswith(_K3D_CONTEXT_PREFIX)
}

# The kubectl context a route lands on: its own cluster pin, else its
# gateway's, else the env's primary cluster_target. Mirrors
# _route_cluster_context, which answers the same question for the routing
# LABEL; this one needs the fallback too, because an unpinned route in a
# single-cluster env still lands somewhere.
_route_landing_context = lambda route: any, gw: any, bundle: any -> str {
    _pinned = _route_cluster_context(route, gw)
    _target = _bundle_cluster_target(bundle)
    _pinned if _pinned else (_target.cluster if _target else "")
}

# The hostname a POD in the landing cluster dials to reach the machine
# running it. Declared per target (ClusterTarget.host_gateway), defaulting
# to k3d's host.k3d.internal — never a literal invented here.
_route_host_gateway = lambda bundle: any -> str {
    _target = _bundle_cluster_target(bundle)
    _target.host_gateway if _target and _target.host_gateway else "host.k3d.internal"
}

# `bundle` is Undefined when a consumer calls the public route builder
# without it, which is legal for a `service` backend. Reading
# `.workloads` off Undefined is an EvaluationError, not a falsy value, so
# the Undefined check has to gate the attribute access itself rather than
# ride along in the same expression.
_bundle_workloads = lambda bundle: any -> [any] {
    bundle.workloads if bundle is not Undefined and bundle else []
}

_bundle_cluster_target = lambda bundle: any -> any {
    _ct = bundle.cluster_target if bundle is not Undefined and bundle else None
    _ct if _ct else None
}

_lookup_workload = lambda bundle: any, name: str -> any {
    _matches = [w for w in _bundle_workloads(bundle) if w.name == name]
    _matches[0] if _matches else None
}

# A workload's exposed ports, as plain ints. Used for inference and for
# the "which port did you mean" refusal.
_workload_ports = lambda w: any -> [int] {
    [p.port for p in (w.ports or [])] if w else []
}

_host_listen_ports = lambda rt: any -> [int] {
    rt.listen_ports if rt and rt.listen_ports is not Undefined else []
}

# The name a route's HOST backend renders as. Derived from the route so two
# routes to the same host workload cannot collide on one Backend object
# whose port only one of them meant.
_host_backend_name = lambda route: any -> str {
    "${route.name}-host"
}

# THE RESOLUTION. Returns the backendRef entry for the route's rule, and
# asserts — naming the route, the workload and its runtime — when the
# target cannot be reached from where the route lands.
#
# `kind` is omitted for a Service backendRef (Gateway API defaults to
# Service) and stated explicitly for the EG Backend.
_backend_ref = lambda route: any, gw: any, bundle: any -> any {
    _wl_name = route.workload if route.workload else ""
    _w = _lookup_workload(bundle, _wl_name) if _wl_name else None
    _rt = _w.runtime if _w else None
    _rt_type = _rt.type if _rt else ""
    _context = _route_landing_context(route, gw, bundle)

    # A workload-backed route needs the Bundle to resolve against. The
    # public re-exported builders accept a route + gateways only, which is
    # enough for a `service` backend; a `workload` backend is a statement
    # about the ENV, so it needs the env.
    assert not (_wl_name and (bundle is Undefined or not bundle)), \
        "route '${route.name}' targets workload '${_wl_name}', which is resolved from the environment — render it through forge.render(bundle), or pass the bundle to the route builder. A `service` backend needs no bundle; a `workload` backend is a statement about where that workload runs in THIS env"

    assert not (_wl_name and bundle is not Undefined and bundle and not _w), \
        "route '${route.name}' targets workload '${_wl_name}', which this env declares no workload for. Declare it in Bundle.workloads, or name a Service directly with `service = \"...\"`. Workloads this env has: ${[w.name for w in _bundle_workloads(bundle)]}"

    assert not (_w and not _rt), \
        "route '${route.name}' targets workload '${_wl_name}', which binds no runtime in this env, so there is nowhere to send the traffic. Bind it, e.g. `wl.${_wl_name} | {runtime = forge.OnCluster {target = ...}}` or `forge.OnHost {}`"

    # OnHost is reachable ONLY from a cluster on the same machine.
    assert not (_rt_type == "host" and not _is_local_cluster(_context)), \
        "route '${route.name}' targets workload '${_wl_name}', which runs on the HOST (forge.OnHost) while the route lands on cluster context '${_context}', which is not a local k3d cluster. A host process is reachable from a cluster running on the same machine (via its host gateway) and from nowhere else — bind '${_wl_name}' to forge.OnCluster in this env, or declare this route only in the envs where it runs on the host"

    # Every other runtime has no address a gateway can route to.
    assert not (_rt_type in ["compose", "hosted", "build-only"]), \
        "route '${route.name}' targets workload '${_wl_name}', whose runtime is ${_rt_type} — there is no address a Gateway can route to. A route's backend must be a cluster workload (forge.OnCluster, reached via its Service) or a host process on a local k3d cluster (forge.OnHost, reached via the cluster's host gateway). ${'An OnHosted workload is addressed by the control plane, not by a route in this env' if _rt_type == 'hosted' else ('A compose service publishes its ports on the host, so bind it to forge.OnHost if a route must reach it' if _rt_type == 'compose' else 'A build-only workload never runs')}"

    _cluster_ports = _workload_ports(_w) if _rt_type == "cluster" else []
    _listen = _host_listen_ports(_rt) if _rt_type == "host" else []
    _candidates = _cluster_ports if _rt_type == "cluster" else _listen

    # Port inference: unambiguous only when the target exposes exactly one.
    assert not (_w and route.port == Undefined and len(_candidates) == 0), \
        "route '${route.name}' targets workload '${_wl_name}' and sets no `port`, and that workload declares no port to infer one from. ${'Declare `listen_ports` on its forge.OnHost runtime (the host port it binds, from fp.allocate_port)' if _rt_type == 'host' else 'Declare `ports` on the workload'}, or set `port` on the route"

    assert not (_w and route.port == Undefined and len(_candidates) > 1), \
        "route '${route.name}' targets workload '${_wl_name}', which exposes ${len(_candidates)} ports (${_candidates}), so `port` cannot be inferred — set it on the route to say which one this route reaches"

    _port = route.port if route.port != Undefined else (_candidates[0] if _candidates else 0)

    # A Service backendRef states no `kind` — Gateway API defaults to
    # Service. The host backend states group+kind, and carries no port:
    # the port lives on the Backend's own fqdn endpoint.
    _service_ref = {name = route.service, port = _port}
    _cluster_ref = {name = _wl_name, port = _port}
    _host_ref = {group = "gateway.envoyproxy.io", kind = "Backend", name = _host_backend_name(route)}
    _workload_ref = _cluster_ref if _rt_type == "cluster" else _host_ref

    _service_ref if not _wl_name else _workload_ref
}

# True when this route's backend is the host process — the case that needs
# a Backend object rendered beside the route.
_needs_host_backend = lambda route: any, gw: any, bundle: any -> bool {
    _w = _lookup_workload(bundle, route.workload) if route.workload else None
    _rt = _w.runtime if _w else None
    True if _rt and _rt.type == "host" else False
}

# The EG Backend for a host-process target: one fqdn endpoint at the
# cluster's host gateway, on the port the workload itself declares.
_render_host_backend = lambda route: any, gw: any, bundle: any, namespace: str -> [any] {
    _w = _lookup_workload(bundle, route.workload)
    _rt = _w.runtime
    _listen = _host_listen_ports(_rt)
    _port = route.port if route.port != Undefined else (_listen[0] if _listen else 0)
    [{
        apiVersion = "gateway.envoyproxy.io/v1alpha1"
        kind = "Backend"
        metadata = {
            name = _host_backend_name(route)
            namespace = namespace
            labels = _managed_labels(route.name) | _cluster_label(_route_cluster_context(route, gw))
        }
        spec = {
            endpoints = [
                {
                    fqdn = {
                        hostname = _route_host_gateway(bundle)
                        port = _port
                    }
                }
            ]
        }
    }]
}

# ---------------------------------------------------------------------------
# Traffic policy — one BackendTrafficPolicy per route that declares one.
# Field names are Envoy Gateway v1.9.2's, verified against the CRD.
# ---------------------------------------------------------------------------

_render_retry = lambda r: any -> any {
    {
        numRetries = r.attempts
        retryOn = {triggers = r.on}
        if r.per_try_timeout != Undefined:
            perRetry = {timeout = r.per_try_timeout}
    }
}

_render_active_health_check = lambda hc: any -> any {
    {
        type = "HTTP"
        interval = hc.interval
        timeout = hc.timeout
        healthyThreshold = hc.healthy_threshold
        unhealthyThreshold = hc.unhealthy_threshold
        http = {
            path = hc.path
            if hc.expected_statuses != Undefined:
                expectedStatuses = hc.expected_statuses
        }
    }
}

_render_passive_health_check = lambda od: any -> any {
    {
        consecutive5XxErrors = od.consecutive_5xx
        consecutiveGatewayErrors = od.consecutive_gateway_errors
        interval = od.interval
        baseEjectionTime = od.base_ejection_time
        maxEjectionPercent = od.max_ejection_percent
    }
}

# `kind` is the route's own kind so the policy targets the route it was
# declared on — a BackendTrafficPolicy attaches by targetRef, and the
# policy and its target must be in the same namespace.
render_route_traffic_policy = lambda route: any, gw: any, route_kind: str, namespace: str -> [any] {
    _t = route.traffic
    _hc = _t.health_check if _t and _t.health_check else None
    _od = _t.outlier_detection if _t and _t.outlier_detection else None
    [{
        apiVersion = "gateway.envoyproxy.io/v1alpha1"
        kind = "BackendTrafficPolicy"
        metadata = {
            name = route.name
            namespace = namespace
            labels = _managed_labels(route.name) | _cluster_label(_route_cluster_context(route, gw))
        }
        spec = {
            targetRefs = [
                {
                    group = "gateway.networking.k8s.io"
                    kind = route_kind
                    name = route.name
                }
            ]
            if _t.retries != Undefined:
                retry = _render_retry(_t.retries)
            if _t.timeout != Undefined:
                timeout = {http = {requestTimeout = _t.timeout}}
            if _hc or _od:
                healthCheck = {
                    if _hc:
                        active = _render_active_health_check(_hc)
                    if _od:
                        passive = _render_passive_health_check(_od)
                }
        }
    }] if _t else []
}

render_http_route = lambda route: any, gateways: [any], namespace: str, bundle: any = Undefined -> [any] {
    _gw = _lookup_gateway(gateways, route.gateway)
    _listener = _lookup_listener(_gw, route.listener) if _gw else None
    _host = _route_host(route, _gw, _listener) if _gw else ""
    _path = _route_path(route, _listener) if _listener else "/"
    _labels = _managed_labels(route.name) | _cluster_label(_route_cluster_context(route, _gw))
    ([{
        apiVersion = "gateway.networking.k8s.io/v1"
        kind = "HTTPRoute"
        metadata = {
            name = route.name
            namespace = namespace
            labels = _labels
        }
        spec = {
            parentRefs = [
                {
                    name = route.gateway
                    sectionName = route.listener
                }
            ]
            if _host:
                hostnames = [_host]
            rules = [
                {
                    matches = [
                        {
                            path = {
                                type = "PathPrefix"
                                value = _path
                            }
                        }
                    ]
                    backendRefs = [_backend_ref(route, _gw, bundle)]
                }
            ]
        }
    }] + (_render_host_backend(route, _gw, bundle, namespace) if _needs_host_backend(route, _gw, bundle) else []) \
       + render_route_traffic_policy(route, _gw, "HTTPRoute", namespace)) if _gw and _listener else []
}

render_grpc_route = lambda route: any, gateways: [any], namespace: str, bundle: any = Undefined -> [any] {
    _gw = _lookup_gateway(gateways, route.gateway)
    _listener = _lookup_listener(_gw, route.listener) if _gw else None
    _host = _route_host(route, _gw, _listener) if _gw else ""
    _labels = _managed_labels(route.name) | _cluster_label(_route_cluster_context(route, _gw))
    # GRPCRoute path matches a gRPC service+method. Empty path means
    # match-everything; explicit path is interpreted as a method match
    # when prefixed with "/".
    ([{
        apiVersion = "gateway.networking.k8s.io/v1"
        kind = "GRPCRoute"
        metadata = {
            name = route.name
            namespace = namespace
            labels = _labels
        }
        spec = {
            parentRefs = [
                {
                    name = route.gateway
                    sectionName = route.listener
                }
            ]
            if _host:
                hostnames = [_host]
            rules = [
                {
                    backendRefs = [_backend_ref(route, _gw, bundle)]
                }
            ]
        }
    }] + (_render_host_backend(route, _gw, bundle, namespace) if _needs_host_backend(route, _gw, bundle) else []) \
       + render_route_traffic_policy(route, _gw, "GRPCRoute", namespace)) if _gw and _listener else []
}
