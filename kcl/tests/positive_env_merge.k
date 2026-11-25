"""
Positive test — `forge.env_merge(base, overrides)` is the explicit,
public, last-wins replacement for the `base + overrides` list-`+` footgun.

Pins the three load-bearing claims:

  1. NAME-KEYED OVERRIDE-WINS — a name present in both lists survives
     ONCE, carrying the `overrides` value (later wins).
  2. POSITION = last-occurrence — an overridden var moves to the position
     its override occupies, exactly as the render-time `_dedup_env_vars`
     safety net produced for `base + overrides`. This is the
     render-equivalence guarantee: migrating a `+` chain to env_merge does
     NOT reorder the rendered container env.
  3. (Rendering is no longer this helper's concern: a workload's env is a
     `{name: value | ref}` MAP, which cannot hold a duplicate name and merges
     last-wins with `|` — see positive_workload_env_channels.k. env_merge
     remains for [EnvVar] lists such as Frontend.env_vars.)
"""

import forge

_base = [
    forge.EnvVar { name = "LOG_LEVEL", value = "info" }
    forge.EnvVar { name = "DATABASE_URL", secret_ref = "db", secret_key = "url" }
    forge.EnvVar { name = "REGION", value = "us-east" }
]

_overrides = [
    forge.EnvVar { name = "LOG_LEVEL", value = "debug" }   # supersedes base
    forge.EnvVar { name = "FEATURE_X", value = "on" }       # new
]

_merged = forge.env_merge(_base, _overrides)

# ── (1) override-wins, one entry per name ──────────────────────────
# 3 base names + 1 new name (FEATURE_X); LOG_LEVEL collapses to one.
assert_count = len(_merged) == 4
_by_name = {ev.name: ev.value for ev in _merged}
assert_log_level_overridden = _by_name["LOG_LEVEL"] == "debug"
assert_region_preserved = _by_name["REGION"] == "us-east"
assert_feature_added = _by_name["FEATURE_X"] == "on"

# ── (2) position = last-occurrence (render-equivalent ordering) ────
# base+overrides = [LOG_LEVEL, DATABASE_URL, REGION, LOG_LEVEL', FEATURE_X];
# keeping the LAST occurrence of each name yields, in order:
#   DATABASE_URL, REGION, LOG_LEVEL', FEATURE_X
# (the first LOG_LEVEL is dropped; LOG_LEVEL takes the override's slot).
assert_order_0 = _merged[0].name == "DATABASE_URL"
assert_order_1 = _merged[1].name == "REGION"
assert_order_2 = _merged[2].name == "LOG_LEVEL"
assert_order_3 = _merged[3].name == "FEATURE_X"

# Secret-ref entry round-tripped intact through the merge.
assert_secret_ref_kept = _merged[0].secret_ref == "db" and _merged[0].secret_key == "url"
