# Install

Forge is a single `forge` binary; none of the install paths needs a C toolchain,
and all but `go install` need no Go toolchain either. The one-liners are in the
[README](../README.md#install):

```bash
curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | sh   # macOS / Linux
irm https://raw.githubusercontent.com/reliant-labs/forge/main/install.ps1 | iex        # Windows (PowerShell)
brew install reliant-labs/reliant/forge                                                # Homebrew
npx @reliantlabs/forge version                                                         # npm / npx
go install github.com/reliant-labs/forge/cmd/forge@latest                              # Go toolchain
```

`install.sh` installs to `~/.local/bin` (set `FORGE_INSTALL_DIR` to change it),
checks the release's sha256 and, with `gh` or `cosign` installed, its signature
before installing. Pin a release with `FORGE_VERSION=v1.0.0`. Everything below
is what the README leaves out.

## CI and sandboxes

For a non-interactive sandbox or CI image, the `curl | sh` line is the whole
setup. Pin the version so the image is reproducible:

```bash
curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | FORGE_VERSION=v1.0.0 sh
export PATH="$HOME/.local/bin:$PATH"   # the installer prints this if it is needed
```

Releases attach `forge_<os>_<arch>.tar.gz` for linux, darwin and windows on amd64
and arm64, plus `checksums.txt`, if you would rather download one yourself.

## Verifying a release

`checksums.txt` comes from the same release as the archives, so it catches a
corrupt download, not a tampered one. Each release therefore also carries
`forge-release.sigstore.json`, a keyless Sigstore attestation over every archive
and `checksums.txt`, signed by forge's `release-attest.yml` workflow. Verify an
archive against it with either tool (no login needed):

```bash
gh attestation verify forge_linux_amd64.tar.gz --bundle forge-release.sigstore.json \
  --repo reliant-labs/forge \
  --predicate-type https://github.com/reliant-labs/forge/attestations/release-assets/v1 \
  --cert-identity-regex '^https://github\.com/reliant-labs/forge/\.github/workflows/release-attest\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)$'

cosign verify-blob-attestation --bundle forge-release.sigstore.json \
  --type https://github.com/reliant-labs/forge/attestations/release-assets/v1 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github\.com/reliant-labs/forge/\.github/workflows/release-attest\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)$' \
  forge_linux_amd64.tar.gz      # cosign 2.x also needs --new-bundle-format
```

`install.sh` runs the first of these that is installed (gh 2.49+, else cosign).
It refuses an archive whose signature does not verify. When there is no signature,
or no verifier is installed, it only warns: releases cut before signing began have
no signature. To refuse an unverified install, add `FORGE_REQUIRE_SIGNATURE=1`:

```bash
curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | FORGE_REQUIRE_SIGNATURE=1 sh
```

The npm package is checked against the release checksums pinned into it at
publish time, and `release-npm.yml` pins them only after it has verified the
signature. A release asset swapped after that cannot reach an `npx` user.
`FORGE_REQUIRE_SIGNATURE=1` makes the shim refuse to run from a copy that
carries no pin. `install.ps1` and the Homebrew formula still check only the
sha256.

## From a clone

```bash
# Build the binary into ./bin/forge
task build

# Or install onto $PATH (into $GOBIN)
task install
forge version

# Run straight from source without installing
go run ./cmd/forge version
```

On first render forge extracts KCL's native library (`libkcl.so`/`.dylib`, or
`kcl.dll` on Windows) into your user cache dir — `%TEMP%\kcl` on Windows — and
loads it from there. Set `KCL_LIB_HOME` to use another directory, which is the
fix when antivirus quarantines the DLL or `%TEMP%` is locked down.

### Repository layout

```
forge/
├── cmd/forge/       # CLI entrypoint (package main)
├── internal/        # CLI implementation, generators, templates, linters
├── kcl/             # KCL module: typed schemas + manifest render layer
├── pkg/             # Libraries projects import (serverkit, svcerr, orm, …)
│   └── components/  # UI component library shipped to scaffolded frontends
├── web-runtime/     # @reliantlabs/forge-web-runtime — the web twin of pkg/
├── proto/           # Forge's own proto annotations (forge/v1)
├── docs/            # getting-started, concepts, benefits, releasing, pkg + cross-repo sources
├── examples/        # Runnable examples
├── forge.yaml       # Project manifest
└── Taskfile.yml     # Automation entrypoints
```

The dev loop (`task test:short`, `task test`, `task lint`, `task fmt`), pre-commit
hooks and the PR process are in [CONTRIBUTING.md](../CONTRIBUTING.md).
