"""
forge.WorkloadURL resolution — the ONE place a workload's public URL is
decided for a referrer forge resolves itself.

A `forge.WorkloadURL { workload = "api" }` names a sibling and means "its
public URL, in this environment". WHO resolves it depends on where the
REFERRER runs (kcl/render.k `_env_var`):

  * a HOSTED workload or frontend keeps the reference; the control plane
    resolves it against what it runs, and re-resolves when a hostname moves.
  * every other referrer resolves it NOW, from what the env declares, so the
    dev document, the host process's env and the cluster record carry a URL
    and nothing downstream sees a reference.

Resolution order for a named target, first match wins:

  1. An HTTPRoute (or GRPCRoute) whose `service` is the target, through a
     declared Gateway listener: <scheme>://<host>:<port><path>. The URL a
     browser actually reaches in a cluster env.
  2. A cluster workload whose EXPOSED port declares custom `domains`:
     https://<first domain>.
  3. A host workload's first `listen_ports`: http://<host>:<port>.
  4. A frontend (dev-served): http://<host>:<port><base_path>.

<host> is `localhost` for a referrer on the same machine (a host process,
a browser). For a CLUSTER referrer it is the referrer's target
`host_gateway` (host.k3d.internal by default): the pod's localhost is the
pod itself, and the host process listens on the machine running the
cluster. A compose workload named as a target resolves the same way,
through its `listen_ports` on the host.

Anything else is REFUSED with a message naming the target and the fix.
Refusing beats guessing: a guessed URL is a frontend that calls the wrong
origin and a backend whose CORS admits nobody, both of which surface as a
"CORS error" that names neither cause. The one refusal worth knowing: a host
workload with NO `listen_ports` is given an ephemeral port by `forge env up`
AFTER the render, so the render cannot know it. Declare `listen_ports =
[plugin.resolve_port(...)]` (the scaffold does).
"""

# The resolved URL of `name` as `from_host` reaches the machine running the
# env ("localhost", or a cluster's host gateway), or "" when nothing
# declares one.
resolve = lambda name: str, bundle: any, from_host: str = "localhost" -> str {
    # A route reaches `name` whether it named it as a `service` or as a
    # `workload` (ADR-0003 F2): both are the same backend, so a URL that
    # resolved through one spelling and not the other would depend on how
    # the route was written rather than on where the traffic goes.
    _route_urls = [u for u in [_route_url(r, bundle) for r in (bundle.http_routes + bundle.grpc_routes) if (r.service and r.service == name) or (r.workload and r.workload == name)] if u]
    _ws = [w for w in bundle.workloads if w.name == name]
    _rt = _ws[0].runtime if _ws else Undefined
    _domains = [d for p in (_ws[0].ports or []) if p.expose for d in (p.domains or [])] \
        if _ws and _rt and _rt.type == "cluster" else []
    _host_ports = _rt.listen_ports if _ws and _rt and _rt.type == "host" and _rt.listen_ports else []
    _frontends = [f for f in bundle.frontends if f.name == name]
    _route_urls[0] if _route_urls else \
        ("https://" + _domains[0] if _domains else \
        ("http://" + from_host + ":" + str(_host_ports[0]) if _host_ports else \
        ("http://" + from_host + ":" + str(_frontends[0].port) + _frontend_base(_frontends[0]) if _frontends else "")))
}

_frontend_base = lambda f: any -> str {
    f.base_path if f.base_path else ""
}

_route_url = lambda r: any, bundle: any -> str {
    _gws = [g for g in bundle.gateways if g.name == r.gateway]
    _ls = [l for g in _gws for l in g.listeners if l.name == r.listener]
    _l = _ls[0] if _ls else None
    _host = (r.host if r.host else (_l.hostname if _l.hostname else _gws[0].host)) if _l else ""
    _scheme = "https" if _l and _l.protocol == "HTTPS" else "http"
    _default_port = (_l.protocol == "HTTPS" and _l.port == 443) or (_l.protocol != "HTTPS" and _l.port == 80) if _l else False
    _path = ((_l.path_prefix + r.path).replace("//", "/")) if _l else ""
    (_scheme + "://" + (_host if _host else "localhost") + ("" if _default_port else ":" + str(_l.port)) + (_path.rstrip("/") if _path != "/" else "")) if _l else ""
}
