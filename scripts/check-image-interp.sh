#!/usr/bin/env bash
# Assert every architecture of a built image carries an ELF interpreter the
# image can actually satisfy.
#
# THE BUG THIS CATCHES. The binary is dynamically linked despite
# CGO_ENABLED=0: the forge module pulls kcl-lang.io -> ebitengine/purego,
# which emits cgo_import_dynamic directives. Go then picks the interpreter
# path from the BUILDER's libc. Build in an alpine (musl) toolchain and
# cross-compile, and the arm64 leg asks for
#
#     /lib/ld-musl-aarch64.so.1
#
# while the debian runtime ships only glibc's ld-linux. exec then fails with
#
#     exec /usr/local/bin/reliant: no such file or directory
#
# which names the BINARY even though the binary is present — the kernel is
# reporting the missing INTERPRETER. That crashlooped three prod deployments.
#
# WHY A BUILD-ONLY CHECK MISSES IT. `docker build` succeeds, the image pushes,
# the manifest is a valid linux/arm64, and `crane config` reports the right
# platform. Nothing is wrong until something tries to RUN it. The amd64 leg
# happens to be fine, so a single-arch smoke test passes too.
#
# Usage: check-image-interp.sh <image-ref> [arch ...]   (default: amd64 arm64)
set -euo pipefail

IMAGE="${1:?usage: check-image-interp.sh <image-ref> [arch ...]}"
shift || true
ARCHES=("$@")
[ ${#ARCHES[@]} -eq 0 ] && ARCHES=(amd64 arm64)

command -v crane >/dev/null || { echo "crane is required" >&2; exit 2; }

fail=0
for arch in "${ARCHES[@]}"; do
  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT

  # Distinguish "this arch genuinely isn't published" from "we could not read
  # the registry at all". Collapsing the two into a silent SKIP is how a guard
  # ends up green against an image it never actually inspected — including on
  # an auth failure, which is the common CI misconfiguration.
  if ! crane manifest "$IMAGE" >/dev/null 2>"$work/err"; then
    echo "  ${arch}: FAIL — cannot read $IMAGE: $(tr -d '\n' < "$work/err")" >&2
    fail=1; rm -rf "$work"; trap - EXIT; continue
  fi

  if ! crane export --platform "linux/${arch}" "$IMAGE" - 2>/dev/null \
      | tar -xO usr/local/bin/reliant > "$work/bin" 2>/dev/null || [ ! -s "$work/bin" ]; then
    echo "  ${arch}: SKIP (no linux/${arch} published for this image)"
    rm -rf "$work"; trap - EXIT; continue
  fi

  # Read PT_INTERP from the program headers rather than grepping strings: a Go
  # binary embeds several loader paths as data, and only the header says which
  # one the kernel will actually use.
  interp="$(python3 - "$work/bin" <<'PY'
import struct,sys
d=open(sys.argv[1],'rb').read(65536)
if d[:4]!=b'\x7fELF': print("NOT-ELF"); raise SystemExit
e='<' if d[5]==1 else '>'
ph=struct.unpack_from(e+'Q',d,0x20)[0]; es=struct.unpack_from(e+'H',d,0x36)[0]; n=struct.unpack_from(e+'H',d,0x38)[0]
for i in range(n):
    o=ph+i*es
    if struct.unpack_from(e+'I',d,o)[0]==3:
        off=struct.unpack_from(e+'Q',d,o+0x08)[0]; sz=struct.unpack_from(e+'Q',d,o+0x20)[0]
        print(d[off:off+sz].rstrip(b'\x00').decode()); raise SystemExit
print("STATIC")
PY
)"

  case "$interp" in
    STATIC)
      echo "  ${arch}: OK (static, no interpreter needed)" ;;
    *ld-musl*)
      echo "  ${arch}: FAIL — wants ${interp}, but the runtime base is debian (glibc)." >&2
      echo "           Build in a glibc toolchain (golang:*-bookworm), not alpine." >&2
      fail=1 ;;
    *)
      # Dynamic and non-musl: the loader must exist in the image, or exec dies
      # with a misleading "no such file or directory" naming the binary.
      if crane export --platform "linux/${arch}" "$IMAGE" - 2>/dev/null \
          | tar -tf - 2>/dev/null | grep -qF "${interp#/}"; then
        echo "  ${arch}: OK (${interp} present in image)"
      else
        echo "  ${arch}: FAIL — wants ${interp}, which is NOT in the image." >&2
        fail=1
      fi ;;
  esac

  rm -rf "$work"; trap - EXIT
done

exit "$fail"
