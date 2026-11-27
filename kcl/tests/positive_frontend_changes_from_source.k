"""
Positive test — `changes_from_source` opts a frontend built from a pinned
source out of carrying that source's Forge Changes (its repository's
changes.yaml) into the env's shape. Only `False` renders: unset and `True`
render no key, so every other frontend renders byte-identically.
"""

import forge

output = forge.render(forge.Bundle {
    project = "platform"
    frontends = [
        forge.Frontend {
            name = "out", type = "vite", port = 5173
            source = forge.GitSource {repo = "github.com/acme/shop", ref = "v1.0.0", subdir = "web"}
            runtime = forge.OnHost {}
            changes_from_source = False
        }
        forge.Frontend {
            name = "on", type = "vite", port = 5174
            source = forge.GitSource {repo = "github.com/acme/shop", ref = "v1.0.0", subdir = "web"}
            runtime = forge.OnHost {}
            changes_from_source = True
        }
        forge.Frontend {
            name = "unset", type = "vite", port = 5175
            source = forge.GitSource {repo = "github.com/acme/shop", ref = "v1.0.0", subdir = "web"}
            runtime = forge.OnHost {}
        }
    ]
})

_by = {f.name: f for f in output.frontends}
assert_opt_out_renders = _by.out.changes_from_source == False
assert_true_renders_no_key = "changes_from_source" not in _by.on
assert_unset_renders_no_key = "changes_from_source" not in _by.unset
