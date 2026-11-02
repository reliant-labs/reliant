# @reliantlabs/forge

The [forge](https://github.com/reliant-labs/forge) CLI, runnable without a Go toolchain:

```sh
npx @reliantlabs/forge version
npx @reliantlabs/forge project new my-app
```

or install it globally:

```sh
npm install -g @reliantlabs/forge
forge version
```

This package is a thin shim. On first run it downloads the prebuilt `forge`
binary for your OS and CPU from the matching
[GitHub release](https://github.com/reliant-labs/forge/releases), verifies its
sha256 against the release's `checksums.txt`, caches it, and runs it. There is no
postinstall script, so `--ignore-scripts` is fine. The package version is the forge
version.

- Cache: `~/.cache/forge/npm/<version>` (Linux), `~/Library/Caches/forge/npm/<version>` (macOS),
  `%LOCALAPPDATA%\forge\npm\<version>` (Windows). Override with `FORGE_NPM_CACHE_DIR`.
- Needs Node 18+ and `tar` on `PATH` (present on Linux, macOS and Windows 10+).
- Platforms: linux, darwin, windows on amd64 and arm64.

Other install methods (curl | sh, Homebrew, `go install`) are listed in the
[main README](https://github.com/reliant-labs/forge#install).
