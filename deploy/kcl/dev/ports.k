"""
Every port the reliant dev stack binds or dials, stated once.

Each is `plugin.allocate_port(base, worktree)`: the primary checkout gets the
bases; each linked git worktree gets a disjoint 100-port block (base +
block*100, memoized by forge, at most `dev_stack.max_stacks` = 8 stacks). All
bases sit in 21000-21099, so stack N owns 21N00-21N99 and the whole range is
21000-21799: clear of control-plane's dev blocks (3000-3799, 6060-6961,
8081-8898, 19000-19794, 30880-31587, 38080+, 39190+), of 5433/5434, and of the
kernel's ephemeral range (32768+), so an outbound connection never lands on a
port a stack is about to bind. Keep every new base inside 21000-21099 and
distinct from the others below.
"""

import kcl_plugin.forge as plugin

_key = option("worktree") or ""
_port = lambda base: int -> int {
    plugin.allocate_port(base, _key)
}

web = _port(21000)
api = _port(21010)
api_health = _port(21011)
api_pprof = _port(21012)
worker_health = _port(21020)
worker_pprof = _port(21021)
gateway = _port(21030)
gateway_health = _port(21031)
postgres = _port(21040)
nats = _port(21050)
nats_monitor = _port(21051)
temporal = _port(21060)
temporal_ui = _port(21062)
