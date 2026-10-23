"""
Positive test — a ShellBuild `cmd` is a PLAIN KCL STRING. forge runs the
rendered value byte-for-byte, so whatever this fixture asserts about the
rendered string is exactly what reaches `sh -c`.

Two things are pinned here, because both were previously supplied by a forge
substitution pass that no longer exists:

  1. Values forge knows are composed IN KCL, from the accessor that already
     holds them — forge.target_arch(), forge.env(), the image reference. The
     rendered cmd carries the VALUE, not a token.
  2. A braced shell variable meant for the SHELL survives into the rendered
     string. In KCL that needs an escape, because braced-dollar is KCL's own
     interpolation syntax: backslash-escape the dollar in a normal string, or
     use a raw (r"...") string. A DOUBLED dollar is NOT an escape — KCL reads
     it as interpolation of an undefined name and fails to compile.

# kcl-args: env=dev target_arch="arm64"
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-dev", namespace = "dev"}

# The canonical shape: KCL values interpolated where forge used to substitute
# a token, and a shell variable escaped so the shell — not KCL — resolves it.
_arch = forge.target_arch()
_tag = forge.image_tag("dev")

_shell = fw.Workload {
    name = "sidecar"
    kind = "tool"
    image = "ghcr.io/acme/sidecar"
    build = forge.ShellBuild {
        cmd = "docker buildx build --platform=linux/${_arch} -t ghcr.io/acme/sidecar:${_tag} --build-arg HOME=\${HOME} ."
        cwd = "../sidecar"
        env = {"DOCKER_BUILDKIT" = "1"}
    }
    runtime = forge.BuildOnly {}
}

# A raw string is the other working escape, and it disables KCL interpolation
# wholesale — so every `${...}` in it belongs to the shell.
_raw = fw.Workload {
    name = "raw"
    kind = "tool"
    build = forge.ShellBuild {cmd = r"""test -n "${HOME}" && echo "$PATH" """}
    runtime = forge.BuildOnly {}
}

output = forge.render(forge.Bundle {
    project = "demo"
    workloads = [_shell, _raw]
})
_b = {w.name: w.build for w in output.workloads}

# The arch accessor resolved the `-D target_arch=` binding forge passes.
assert_arch_bound = _arch == "arm64"

# The rendered cmd carries forge's VALUES, with no token left behind.
assert_cmd_has_arch = "--platform=linux/arm64" in _b["sidecar"].cmd
assert_cmd_has_tag = "ghcr.io/acme/sidecar:latest" in _b["sidecar"].cmd
assert_cmd_no_targetarch_token = "TARGETARCH" not in _b["sidecar"].cmd

# The escaped shell variable survives KCL as a literal `${HOME}` — this is
# what forge hands the shell, which resolves it from the process env.
assert_cmd_keeps_shell_var = r"--build-arg HOME=${HOME}" in _b["sidecar"].cmd

# A raw string passes `${...}` through untouched too.
assert_raw_keeps_braced = r"${HOME}" in _b["raw"].cmd
assert_raw_keeps_bare = "$PATH" in _b["raw"].cmd

# cwd and env ride along as declared; forge merges env onto the process env.
assert_cwd = _b["sidecar"].cwd == "../sidecar"
assert_env = _b["sidecar"].env["DOCKER_BUILDKIT"] == "1"
