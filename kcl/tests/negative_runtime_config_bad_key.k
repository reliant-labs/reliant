"""
Negative test — a runtime_config key must be a JavaScript identifier: the
bundle reads it as window.__FORGE_CONFIG__.<KEY>, and a hyphenated key is
unreachable that way.
"""

# expect: runtime_config keys must be JavaScript identifiers

import forge

_fe = forge.Frontend {
    name = "web"
    path = "frontends/web"
    runtime_config = { "api-url" = "https://x" }
}
