"""
Positive test — `release_urls` turns a frontend's per-release URLs off. Only
`False` renders: unset and `True` render no key, so every other frontend
renders byte-identically. False is accepted on a runtime that serves no
release URLs (here the dev server), because it holds there by construction and
a frontend is declared once and bound per env.
"""

import forge

output = forge.render(forge.Bundle {
    project = "platform"
    frontends = [
        forge.Frontend {
            name = "off", type = "vite", port = 5173, path = "frontends/off"
            runtime = forge.OnHost {}
            release_urls = False
        }
        forge.Frontend {
            name = "on", type = "vite", port = 5174, path = "frontends/on"
            runtime = forge.OnHost {}
            release_urls = True
        }
        forge.Frontend {
            name = "unset", type = "vite", port = 5175, path = "frontends/unset"
            runtime = forge.OnHost {}
        }
    ]
})

_by = {f.name: f for f in output.frontends}
assert_opt_out_renders = _by.off.release_urls == False
assert_true_renders_no_key = "release_urls" not in _by.on
assert_unset_renders_no_key = "release_urls" not in _by.unset
