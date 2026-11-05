"""
Positive test — a Frontend whose code lives in ANOTHER repository,
declared as a pinned forge.GitSource instead of a filesystem path.

This is the shape that makes a cross-repo frontend buildable in CI (where
only the declaring repository is checked out) and reproducible everywhere
(the ref pins what ships, instead of "whatever was in the sibling dir").

Mirrors the real control-plane recipe: reliant-web is a Vite SPA living in
the reliant repo's web/ directory, deployed to Firebase Hosting.

Invariants pinned here:
  * `source` projects with repo / ref / subdir
  * `path` projects as null when the frontend is source-declared
  * a path-declared frontend still projects `path` with `source` null —
    the additive-change regression guard
"""

import forge

_sourced = forge.Frontend {
    name = "reliant-web"
    type = "vite"
    source = forge.GitSource {
        repo = "github.com/reliant-labs/reliant"
        ref = "v1.6.3"
        subdir = "web"
    }
    public_dir = "dist"
    runtime = forge.OnFirebase {
        project = "reliant-labs-475814"
        site = "reliant-prod"
    }
}

# Same repo root, no subdir — the whole repository IS the frontend.
_root_sourced = forge.Frontend {
    name = "docs-site"
    type = "nextjs"
    source = forge.GitSource {
        repo = "github.com/reliant-labs/docs"
        ref = "3f1a2b9c4d5e6f70819a2b3c4d5e6f7081920a1b"
    }
    runtime = forge.OnHost {}
}

# The unchanged shape: a directory in THIS repo.
_local = forge.Frontend {
    name = "admin-web"
    type = "nextjs"
    path = "admin-web"
    runtime = forge.OnHost {}
}

_bundle = forge.Bundle {
    project = "fixture"
    frontends = [_sourced, _root_sourced, _local]
}

output = forge.render(_bundle)

# ── Invariants ─────────────────────────────────────────────────────

_s = output.frontends[0]
_r = output.frontends[1]
_l = output.frontends[2]

assert_source_present = _s.source != None
assert_source_repo = _s.source.repo == "github.com/reliant-labs/reliant"
assert_source_ref = _s.source.ref == "v1.6.3"
assert_source_subdir = _s.source.subdir == "web"
# A source-declared frontend has no directory in this repo.
assert_source_path_null = _s.path == None
# The runtime is orthogonal to where the code came from.
assert_source_runtime_type = _s.runtime.type == "firebase"

# Repo root: subdir projects as null, not "".
assert_root_subdir_null = _r.source.subdir == None
assert_root_ref = _r.source.ref == "3f1a2b9c4d5e6f70819a2b3c4d5e6f7081920a1b"

# Regression guard: the pre-existing path shape is untouched.
assert_local_path = _l.path == "admin-web"
assert_local_source_null = _l.source == None
