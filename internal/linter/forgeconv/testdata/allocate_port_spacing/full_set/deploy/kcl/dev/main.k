import kcl_plugin.forge as fp

# The full control-plane deploy/kcl/dev/main.k allocate_port base set,
# reproduced verbatim for this fixture. See the package doc comment in
# allocate_port_spacing.go for the incident this pins.
_key = option("worktree") or ""

# This comment MENTIONS allocate_port(28090, _key) for exposition only,
# the same way control-plane's real main.k narrates its own port math in
# nearby comments — it must not be double-counted as a second call site.
_gateway_http_port = fp.allocate_port(28080, _key)
_gateway_grpc_port = fp.allocate_port(29190, _key)

_gateway_controller_port = fp.allocate_port(28090, _key)

_reliant_grpc_port = fp.allocate_port(3091, _key)

_reliant_health_port = fp.allocate_port(8081, _key)

_reliant_web_port = fp.allocate_port(3000, _key)
_internal_console_port = fp.allocate_port(3002, _key)
_admin_server_port = fp.allocate_port(8090, _key)

_reliant_worker_health_port = fp.allocate_port(8082, _key)
