"""
Negative test — `EnvVar { name = "FOO" }` (no value, no secret_ref,
no config_map_ref) is rejected.

Pins the "field absent" branch of the new EnvVar check block:
distinguishing "field absent" from "field present but empty" was the
whole point of making `value` optional. Absent must still fail; only
explicitly-set empty string is the new legal case.
"""

# expect: EnvVar must set value (empty string is OK)

import forge

_bad = forge.EnvVar { name = "FOO" }
