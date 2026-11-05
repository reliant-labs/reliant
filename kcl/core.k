"""
Forge KCL — env-map primitives for the `[EnvVar]` list shape.

`fw.Workload.env` is a map of name -> value | reference (kcl/workload.k),
which is the authoring shape for workloads. The helpers here serve the
places that still take a LIST of EnvVar (a frontend's `env_vars`): they
let those be authored as a map too.

This file introduces the target-agnostic core's model of a workload's
environment: a MAP keyed by variable name (`{str: EnvSource}`), plus the
k8s-adapter projection (`env_project`) that turns that map into the
list-shaped `[EnvVar]` the k8s renderer already consumes.

Why a MAP is the correct core (and `[EnvVar]` is a k8s wire artifact)
=====================================================================
k8s `env:` is a LIST `[{name, value/valueFrom}]`, so duplicate names are a
hard server-side-apply error — which is the ONLY reason forge models env as
`[EnvVar]` and needs `env_merge` + the hidden `_dedup_env_vars` to guard the
`base + overrides` list-`+` footgun (kcl/base.k, kcl/lib/services.k). A MAP
cannot hold two of a key, so:

  * duplicate names are STRUCTURALLY IMPOSSIBLE — the dedup footgun cannot
    occur by construction, so no `env_merge` is needed in the core;
  * authors COMPOSE with native KCL map-merge `base | overrides`
    (last-wins) — no O(n²) comprehension, no helper;
  * a single projection (`env_project`) is the ONLY place a list is
    produced — list-dedup legitimately lives there, as an adapter concern,
    because the map already guarantees uniqueness.

`{str: EnvSource}` is the authoring shape. `env_project` is the k8s
adapter's projection into the EXISTING `EnvVar` schema — so its output flows
through the unchanged render pipeline (`_render_env_var` for the JSON
contract; `_dedup_env_vars` + `_env_source` for k8s manifests) with zero
renderer changes. This is ADDITIVE: `EnvVar` and `env_merge` are untouched.

"""


schema EnvSource:
    """A target-neutral, single-channel env value — the MAP value in the
    agnostic core's `env: {str: EnvSource}`.

    Mirrors `EnvVar`'s three projection channels, but SEMANTIC (no k8s wire
    shape) and single-value-carrying (the NAME is the map key, not a field):

      * `value = "..."`      — an inline literal. Empty string is a LEGAL
                               value (mirrors EnvVar: the check treats
                               "explicitly empty" as the value channel being
                               set — `value != Undefined`, NOT `if value`),
                               so `value = ""` renders `{name, value: ""}`.
      * `from_secret = {name, key}` — read from a Secret. `name` is the
                               Secret name, `key` the key within it. Projects
                               to EnvVar.secret_ref / secret_key (k8s
                               secretKeyRef).
      * `from_config = {name, key}` — read from a ConfigMap. `name` is the
                               ConfigMap name, `key` the key within it.
                               Projects to EnvVar.config_map_ref /
                               config_map_key (k8s configMapKeyRef).
      * `from_field = "..."`  — the DOWNWARD API: read a field of the running
                               pod/container. The string is a k8s field path
                               ("metadata.name", "metadata.namespace",
                               "status.podIP", "spec.nodeName"). Projects to
                               EnvVar.field_ref (k8s valueFrom.fieldRef.
                               fieldPath). This is the channel controllers /
                               OTel resource-detection / pod-aware logging
                               need (POD_NAME, POD_NAMESPACE, POD_IP,
                               NODE_NAME). Compose has no analogue — the
                               compose adapter SKIPS from_field vars.

    EXACTLY ONE channel must be set — a malformed source (zero channels, or
    two) is a render-time CHECK FAILURE, not a silently-dropped var. This is
    the map-shape's analogue of EnvVar's own "must set a source" check.
    """
    value?: str
    from_secret?: {str: str}
    from_config?: {str: str}
    from_field?: str
    # Marks a `from_secret` whose value may be ABSENT from the environment's
    # secret store. Default False — a declared secret with no value is an
    # error, which is the point of the pre-flight. Set by config codegen from
    # `optional: true` on a `sensitive` proto field, or by hand for a secret
    # that is absent by design; see EnvVar.secret_optional for the full
    # reasoning.
    secret_optional?: bool

    check:
        # EXACTLY ONE channel. `value != Undefined` (not `value`) so an
        # explicit empty-string value counts as the value channel being set
        # — same empty-value semantics as EnvVar.
        len([_c for _c in [value != Undefined, from_secret != Undefined, from_config != Undefined, from_field != Undefined] if _c]) == 1, \
            "EnvSource must set EXACTLY ONE channel: value (empty string OK) | from_secret | from_config | from_field"
        from_secret == Undefined or ("name" in from_secret and "key" in from_secret), \
            "EnvSource.from_secret must carry both 'name' and 'key' (e.g. {name = \"my-secret\", key = \"password\"})"
        from_config == Undefined or ("name" in from_config and "key" in from_config), \
            "EnvSource.from_config must carry both 'name' and 'key' (e.g. {name = \"my-config\", key = \"log-level\"})"
        secret_optional == Undefined or secret_optional == False or from_secret != Undefined, \
            "EnvSource.secret_optional is only meaningful with from_secret — it exempts a DECLARED secret from the store pre-flight, and there is nothing to exempt without one"

# Alias for lambda-body schema instantiation (see schema.k header — KCL
# v0.11 accepts BARE-identifier schema literals inside lambda bodies).
_EnvVarForProject = EnvVar

# env_project(m) — the k8s ADAPTER's projection: the agnostic-core env MAP
# `{str: EnvSource}` -> the list-shaped `[EnvVar]` the k8s renderer already
# consumes (kcl/lib/services.k `_env_source`, kcl/render.k `_render_env_var`).
#
# This is the ONLY place a list is produced from the core env map, and it is
# where list-shaped concerns (the map->list flattening) legitimately live —
# an adapter concern. It SUBSUMES `env_merge`'s last-wins dedup: the map key
# is the var name, so uniqueness is already guaranteed and no dedup pass is
# needed (the output is dedup-free by construction).
#
# ORDER is INSERTION order — the author's authoring order, preserved. KCL
# dicts (`{str: EnvSource}`) iterate in insertion order, and native `|`
# map-merge keeps base-key positions and appends new keys (verified
# empirically). This is LOAD-BEARING: k8s `env` supports dependent expansion
# — `VALUE: "$(EARLIER_VAR)/x"` resolves ONLY against entries appearing
# EARLIER in the list. A previous revision sorted by key here, which silently
# broke `$(VAR)` interpolation (a key `A = "$(Z)"` sorted before `Z` left
# `$(Z)` literal at runtime) AND was a regression vs `env_merge` (which keeps
# author order). Iterating in insertion order restores author control of
# ordering so `$(VAR)` works. Authors who want cross-referencing env just
# declare the referenced var EARLIER in the map literal.
#
# Channel mapping (mirrors _env_source's k8s projection exactly):
#   value       -> EnvVar.value
#   from_secret -> EnvVar.secret_ref (name) + secret_key (key)  [secretKeyRef]
#   from_config -> EnvVar.config_map_ref (name) + config_map_key (key)  [configMapKeyRef]
#   from_field  -> EnvVar.field_ref (k8s field path)            [fieldRef]
#
# Native map-merge is how authors COMPOSE before projecting:
#   _env = _base_env | { "LOG_LEVEL" = {value = "debug"} }   # last-wins, no helper
#   env_vars = forge.env_project(_env)
env_project = lambda m: {str: EnvSource} -> [EnvVar] {
    [
        _EnvVarForProject {
            name = _k
            if m[_k].value != Undefined:
                value = m[_k].value
            if m[_k].from_secret != Undefined:
                secret_ref = m[_k].from_secret.name
                secret_key = m[_k].from_secret.key
                # Carried, not dropped: the pre-flight reads the PROJECTED
                # EnvVar, so an optional flag that stopped at the agnostic
                # core would be silently lost exactly where it is consumed.
                if m[_k].secret_optional != Undefined:
                    secret_optional = m[_k].secret_optional
            if m[_k].from_config != Undefined:
                config_map_ref = m[_k].from_config.name
                config_map_key = m[_k].from_config.key
            if m[_k].from_field != Undefined:
                field_ref = m[_k].from_field
        }
        for _k in m
    ]
}

# env_override(base, overrides) — layer per-service env `overrides` over the
# config projection `base`, REPLACING each key WHOLE (last-wins), NOT
# deep-merging it. This is the CORRECT way to compose service overrides on top
# of `appConfigEnvMap(cfg)` (or any base env map).
#
# Why a helper and not bare `|`
# =============================
# Composing env maps with bare `|` is a footgun because whether an existing key
# is REPLACED or its channels are FUSED depends on the UNION operator KCL uses
# under the hood. When the override merges INTO a key that already exists in the
# base with a DIFFERENT channel — the `:`/union form KCL takes for a nested
# schema value — the two EnvSources are DEEP-MERGED into ONE instance carrying
# BOTH `from_config` AND `from_secret`, which trips EnvSource's exactly-one-
# channel check and aborts the render (see negative_env_override_fuse.k). Bare
# `|` is only reliably safe for ADDING NEW keys (keys absent from the base):
# there is nothing to fuse. The moment an override RE-CHANNELS an existing
# config key (from_config -> from_secret, value -> from_config, …) you MUST use
# `env_override`, which sidesteps the operator subtlety entirely by REPLACING
# each overridden key's WHOLE EnvSource.
#
# The contract: for every key present in `overrides`, the result carries the
# override's WHOLE EnvSource (the base value is discarded, not merged); keys
# only in `base` are untouched; keys only in `overrides` are appended. Base key
# ORDER is preserved (base keys first, in base insertion order, then new
# override keys) — the same insertion-order contract `env_project` relies on
# for `$(VAR)` interpolation. The trailing `|` is safe because the two
# comprehensions share NO keys (base keys vs override-only keys), so no value
# is ever fused — it is the ADD-only use `|` is legitimate for.
#
#   env = env_override(appConfigEnvMap(cfg), {
#       "LITELLM_MASTER_KEY" = {from_secret = {name = "cp-litellm", key = "master-key"}}
#   })
env_override = lambda base: {str: EnvSource}, overrides: {str: EnvSource} -> {str: EnvSource} {
    {_k: (overrides[_k] if _k in overrides else base[_k]) for _k in base} \
        | {_k: overrides[_k] for _k in overrides if _k not in base}
}
