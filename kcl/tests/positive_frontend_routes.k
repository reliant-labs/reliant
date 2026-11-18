"""
Positive test — Frontend.routes projects to the render as the CRUD-page
allowlist, and is absent when unset (every CRUD entity).

`["none"]` is a value, not a slug: no generated pages at all.
"""

import forge

output = forge.render(forge.Bundle {
    project = "fixture"
    frontends = [
        forge.Frontend {name = "ops", path = "frontends/ops", routes = ["users", "usage-events"], runtime = forge.OnHost {}}
        forge.Frontend {name = "site", path = "frontends/site", routes = ["none"], runtime = forge.OnHost {}}
        forge.Frontend {name = "app", path = "frontends/app", runtime = forge.OnHost {}}
    ]
})

_by_name = {f.name: f for f in output.frontends}
assert_allowlist = _by_name["ops"].routes == ["users", "usage-events"]
assert_none_value = _by_name["site"].routes == ["none"]
assert_unset_is_every_entity = "routes" not in _by_name["app"]
