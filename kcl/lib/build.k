"""
Build-side facts forge derives and binds on every render.

A ShellBuild's `cmd` is a plain KCL string that forge runs byte-for-byte —
there is no substitution pass and forge exports no `${TARGETARCH}`-style
variables into the shell. So anything forge knows and the command needs has
to be readable IN KCL, where the command is composed. That is what this file
is for.

The accessors are re-exported on the top-level `forge` namespace
(kcl/render.k), so a main.k calls `forge.target_arch()` rather than reaching
into `lib`.
"""

# target_arch() — the GOARCH the build targets, as forge resolved it.
#
# forge binds `-D target_arch=<arch>` on every render, from the same
# precedence the rest of the build uses:
#
#   1. `--target-arch` on the command line
#   2. the env's declared cluster platform (cluster_target.platform, else the
#      first cluster-bound workload's runtime platform)
#   3. forge.yaml `deploy.target_arch`
#   4. the host's GOARCH
#
# Use it to cross-compile inside a ShellBuild, where `${TARGETARCH}` used to
# be substituted:
#
#     build = forge.ShellBuild {
#         cmd = "docker buildx build --platform=linux/${forge.target_arch()} ..."
#     }
#
# `default` covers a plain `kcl run` with no binding. It defaults to "amd64"
# — the same default forge.yaml's deploy.target_arch falls back to, and NOT
# the empty string: an empty arch reaches `--platform=linux/` (which buildx
# rejects) or, worse, `GOARCH=` (which silently builds for the host).
target_arch = lambda default: str = "amd64" -> str {
    option("target_arch") or default
}
