"""
Image reference resolution.

A workload's `image` IS its registry. The author writes the full reference
(`ghcr.io/acme/api`, `localhost:5051/api`, `us-central1-docker.pkg.dev/p/r/api`)
and forge contributes only two things it alone knows: the resolved TAG and,
after a push, the DIGEST. There is no env-level registry to prefix — an
environment does not have a registry, a workload does.

So `image_ref` composes rather than assembles: it keeps the author's host
verbatim and appends `:<tag>` / `@<digest>`. Which env a workload is bound to
changes its tag and its digest, never its registry.
"""

# Does `image` already carry its OWN tag or digest (`name:tag`,
# `name@sha256:...`), so the env tag must NOT be appended? A registry-port
# colon (`localhost:5050/foo`) sits BEFORE the last `/`, so a tag colon is
# the rightmost `:` after the rightmost `/`. A digest is any `@`.
has_tag_or_digest = lambda image: str -> bool {
    _last_slash = image.rfind("/")
    _tail = image[_last_slash + 1:] if _last_slash >= 0 else image
    ("@" in image) or (":" in _tail)
}

# Does `image` name a registry HOST? Docker's own heuristic: the first path
# segment is a host iff it contains a `.` or a `:`, or is exactly `localhost`.
#
# This is no longer a "should the env registry be prefixed" question — there is
# no env registry to prefix. It is the completeness check: an image WITHOUT a
# host is an incomplete declaration, and forge refuses it at render rather
# than emitting a reference the kubelet resolves against Docker Hub and fails
# to pull. `nginx` and `library/nginx` are therefore refused too: they are
# almost always a forgotten host, and an author who genuinely means Docker Hub
# can say so (`docker.io/library/nginx`).
has_registry_host = lambda image: str -> bool {
    _first_slash = image.find("/")
    _head = image[:_first_slash] if _first_slash > 0 else ""
    _head != "" and (("." in _head) or (":" in _head) or (_head == "localhost"))
}

# The registry HOST an image names (`ghcr.io`, `localhost:5051`), or "" when it
# names none. The push target, the login target, and the local-vs-cloud signal
# the build-platform guard keys off — all read from the image, because the
# image is the only place a registry is declared.
registry_host = lambda image: str -> str {
    _first_slash = image.find("/")
    _head = image[:_first_slash] if _first_slash > 0 else ""
    _head if (_head != "" and (("." in _head) or (":" in _head) or (_head == "localhost"))) else ""
}

# `image` minus any `:tag` / `@digest` — the REPOSITORY, host INCLUDED.
#
# This is the key for build state and the release ledger. The host is kept
# because the host is part of the declaration now: two envs that build the
# same app to two different registries push two genuinely different
# repositories, and a key that dropped the host would collapse them onto one
# ledger entry and let a prod promotion read a digest that only exists in the
# dev registry.
repository = lambda image: str -> str {
    _no_digest = image.split("@")[0]
    _last_slash = _no_digest.rfind("/")
    _colon = _no_digest.rfind(":")
    _no_digest[:_colon] if _colon > _last_slash else _no_digest
}

# The KEY `image` is looked up under in the `image_digests` map.
#
# A DIGEST-pinned image has no key: it already names its bytes, and nothing
# may override that.
#
# A TAG-pinned image (`ghcr.io/acme/reliant:e2e`) is keyed VERBATIM, tag
# included, because a digest is only true of the exact tag it was captured
# for. The untagged repository may carry a digest captured for some OTHER tag
# of the same image — pinning that here would ship bytes that were never
# pushed under this tag, which is a worse failure than the stale tag it was
# meant to fix.
#
# An UNPINNED image is keyed by its repository: the env tag is the only tag in
# play, so the repository is unambiguous.
digest_key = lambda image: str -> str {
    "" if "@" in image else image
}

# The full reference for `image`:
#
#   1. It pins its own DIGEST: kept verbatim — it already names its bytes.
#   2. `image_digests` has a digest under this image's key (digest_key):
#      the digest is appended. For an unpinned image that is
#      `<image>@<digest>`; for a TAG-pinned image the tag is KEPT and the
#      digest appended — `<image>:<tag>@<digest>` — so the reference stays
#      readable while the kubelet pulls by digest.
#   3. A tag-pinned image with no digest for that tag: kept verbatim.
#   4. Else the env tag: `<image>:<tag>`.
#
# The author's host is never touched in any branch. `image_ref` composes onto
# the declaration; it does not assemble one.
#
# WHY A TAG-PINNED IMAGE IS DIGEST-PINNED TOO. An image that pins its own tag
# is still one forge BUILDS and PUSHES (control-plane's e2e env pins
# `reliant:e2e`, and forge's ShellBuild pushes it). Returning it verbatim
# meant a rebuild under the same tag left spec.image byte-identical, so the
# Deployment did not roll out and — with imagePullPolicy IfNotPresent — the
# nodes kept the previous digest, while the deploy reported success. That is
# a silent no-op deploy, the exact failure digest pinning exists to prevent.
image_ref = lambda image: str, tag: str, digests: {str:str} -> str {
    _key = digest_key(image)
    _digest = digests[_key] if (_key and _key in digests) else ""
    image + "@" + _digest if _digest \
        else (image if has_tag_or_digest(image) \
        else "${image}:${tag}")
}
