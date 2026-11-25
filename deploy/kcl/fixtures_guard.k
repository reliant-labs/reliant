"""
A frontend in fixture mode reaches nothing live (reliant#3454).

Fixture mode is turned on per env by a frontend's `fixtures` field (forge#2719)
or its two runtime-config keys. It serves the production build of
frontends/web on a sample-data pack: every RPC is answered in the page, and
src/fixtures/outside.ts answers the Supabase sign-in. The build still carries
the real sign-in and API code, so an env that turns fixture mode on must also
leave that code nothing to reach. Pointing one of its origins at a live service
by mistake must be a render error, not a preview that signs in to prod.

inert_frontend refuses a fixture-mode frontend with any origin in its runtime
config (every key ending in _URL, and OTEL_ENDPOINT) that is neither empty nor
a host under `.invalid`. RFC 6761 reserves that TLD so it never resolves. A
WorkloadURL value is refused too, because it names a live workload. A frontend
not in fixture mode is returned unchanged.

inert_when_fixtures applies it to every frontend of a bundle. deploy/kcl/preview
renders through it. control-plane, which declares the hosted preview frontend
in its prod env, imports this module (`import reliant.fixtures_guard`) and
applies inert_frontend to that frontend.

Tested both ways by internal/forgetest/fixtures_guard_test.go, over
deploy/kcl/testdata/fixtures_guard_*.k. This file and those fixtures are the
only files outside deploy/kcl/preview that may name the fixture keys, because
they refuse them (internal/forgetest/fixtures_env_test.go).
"""
import forge
import regex

# An origin that cannot resolve: http(s), any host under .invalid, an optional
# port and path.
INERT_ORIGIN = r"^https?://([A-Za-z0-9-]+\.)+invalid(:[0-9]+)?(/[^?#\s]*)?$"

# The runtime-config keys that name somewhere the page may send a request.
is_origin_key = lambda k: str -> bool {
    k.endswith("_URL") or k == "OTEL_ENDPOINT"
}

# True when the frontend turns fixture mode on: forge's field, or the key
# set by hand.
fixtures_on = lambda f: forge.Frontend -> bool {
    _rc = f.runtime_config or {}
    _key = _rc.FIXTURES_SCENARIO if "FIXTURES_SCENARIO" in _rc else ""
    bool(f.fixtures) or (typeof(_key) == "str" and _key.strip() != "")
}

# The origin keys of f whose values could reach a live service.
live_origins = lambda f: forge.Frontend -> [str] {
    _rc = f.runtime_config or {}
    sorted([k for k, v in _rc if is_origin_key(k) and not (typeof(v) == "str" and (v == "" or regex.match(v, INERT_ORIGIN)))])
}

inert_frontend = lambda f: forge.Frontend -> forge.Frontend {
    _live = live_origins(f) if fixtures_on(f) else []
    assert not _live, \
        "frontend ${f.name} serves a fixture pack, but its runtime config points ${_live} at a live origin. " \
        "A fixture-mode env keeps the production build's sign-in and API code, so every origin must be empty or a host under .invalid " \
        "(reliant deploy/kcl/fixtures_guard.k)."
    f
}

inert_when_fixtures = lambda b: forge.Bundle -> forge.Bundle {
    _checked = [inert_frontend(f) for f in (b.frontends or [])]
    b
}
