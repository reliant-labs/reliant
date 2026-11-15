"""
ACCEPTANCE TEST for `forge.env_override` — the CORRECT way to layer per-service
env overrides over a config projection, REPLACING each key WHOLE instead of
deep-merging it.

The footgun this fixes
======================
Native KCL `|` deep-merges dict values that are schema instances. So
    base | { "X" = {from_secret = ...} }
where base had `"X" = {from_config = ...}` FUSES the two EnvSources into ONE
instance carrying BOTH channels — which trips EnvSource's exactly-one-channel
check. `env_override` REPLACES the whole EnvSource per key, so a config key
re-channelled by an override yields a single-channel source, not a two-channel
invalid one. Bare `|` stays safe only for ADDING new keys.
"""

import forge

# A stand-in "config projection": a base env map where APP_URL comes from a
# ConfigMap (the from_config channel) and LOG_LEVEL is an inline value.
_base = {
    "LOG_LEVEL" = forge.EnvSource {value = "info"}
    "APP_URL" = forge.EnvSource {from_config = {name = "app-config", key = "app-url"}}
}

# The override RE-CHANNELS APP_URL from from_config -> from_secret, and ADDS a
# brand-new NATS_PASSWORD secret key.
_overrides = {
    "APP_URL" = forge.EnvSource {from_secret = {name = "app-secrets", key = "app-url"}}
    "NATS_PASSWORD" = forge.EnvSource {from_secret = {name = "cp-nats", key = "password"}}
}

_merged = forge.env_override(_base, _overrides)

# ── THE PROOF: APP_URL is now a SINGLE-channel from_secret, NOT a fused
#    two-channel source. from_config was REPLACED whole, not merged into. ──
assert_app_url_from_secret = _merged["APP_URL"].from_secret == {name = "app-secrets", key = "app-url"}
assert_app_url_no_config = _merged["APP_URL"].from_config == Undefined
# Only ONE channel is set on the replaced source — the exactly-one invariant
# a bare `|` would have violated.
assert_app_url_single_channel = len([_c for _c in [
    _merged["APP_URL"].value != Undefined,
    _merged["APP_URL"].from_secret != Undefined,
    _merged["APP_URL"].from_config != Undefined,
    _merged["APP_URL"].from_field != Undefined,
] if _c]) == 1

# Untouched base key survives unchanged; new override key is added.
assert_log_level_survived = _merged["LOG_LEVEL"].value == "info"
assert_nats_added = _merged["NATS_PASSWORD"].from_secret == {name = "cp-nats", key = "password"}
assert_keys = sorted([_k for _k in _merged]) == ["APP_URL", "LOG_LEVEL", "NATS_PASSWORD"]
# Base-key ORDER is preserved (base keys first, then appended override-only
# keys) — the insertion-order contract env_project relies on.
assert_order = [_k for _k in _merged] == ["LOG_LEVEL", "APP_URL", "NATS_PASSWORD"]

# ── END-TO-END: the merged map projects to a valid, single-channel [EnvVar]
#    through the k8s adapter (this render would FAIL if APP_URL were the fused
#    two-channel source a bare `|` produces). ──
_projected = forge.env_project(_merged)
_by_name = {e.name: e for e in _projected}
assert_proj_app_url_secret = _by_name["APP_URL"].secret_ref == "app-secrets" and _by_name["APP_URL"].secret_key == "app-url"
assert_proj_app_url_no_config = _by_name["APP_URL"].config_map_ref == ""
assert_proj_log_value = _by_name["LOG_LEVEL"].value == "info"

# The FUSE hazard bare `|`/union produces (a two-channel EnvSource that aborts
# the render) is proven in negative_env_override_fuse.k — it can't be evaluated
# here without failing the whole fixture, which is exactly the point.
