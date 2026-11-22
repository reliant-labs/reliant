# Self-update: `reliant update`

A `reliant` binary that nothing else owns replaces itself with the newest signed
release of its channel. This is phase E2 of forge's
`docs/proposals/self-upgrade.md`: forge's `pkg/selfupdate` is the mechanism
(stage, verify, swap, confirm, revert), and `internal/cliupdate` points it at
reliant's release channel. The channel is `daemon-channels.json` and the
release's Sigstore signature (`dev-docs/cli-release-artifacts.md`).

## The pieces

| Piece | Where |
| --- | --- |
| Channel, signing contract, `Config`, `Advice` | `internal/cliupdate/cliupdate.go` |
| `reliant update [--check] [--channel latest\|alpha] [--yes] [--json]` | `cmd/reliant/cmd/update.go` |
| Staging, verification, swap, revert | forge `pkg/selfupdate`, `pkg/selfupdate/sigstore` |
| State: staged releases, previous binaries, `state.json`, TUF cache | `~/.reliant/upgrade/` (machine-level, shared by every instance) |

`internal/cliupdate` pins the same signer identity, issuer, predicate type and
asset name as the release doc's "Signing" table.
`TestSigningContractMatchesTheReleaseDoc` holds the two equal, and
`TestVerifiesARealReliantRelease` verifies v1.26.0's real signed `checksums.txt`
against a pinned Sigstore trusted root, so a contract change that would refuse
real releases fails a test. Production fetches the trusted root through TUF.

## Channels

A build follows the channel of its tag: `alpha` for a `-rc`, `-beta` or
`-alpha` tag, `latest` for everything else, including pseudo-versions.
`--channel` overrides it. An `alpha` client takes the newer of `alpha` and
`latest` by `commit_time_unix`. A stable release after an RC train writes only
`latest`, so without this an alpha client would stay on the last RC.

A build with neither a version nor a build number (a plain `go build`) cannot be
ordered against any release. `reliant update` refuses it with `ErrDevBuild`.

## Who may be replaced

`selfupdate.Detect` decides who may replace a binary. Heuristics only refuse;
they never grant.

| Install | How it is recognised | Advice |
| --- | --- | --- |
| Release archive in a writable directory (`~/.local/bin`) | nothing else matched | `reliant update` |
| The desktop app's bundled `reliant-backend`, and the `reliant` symlinks into it (macOS, the .deb's `/usr/bin/reliant`, `~/.local/bin`) | path: `*.app/Contents/`, `resources/server/`, an AppImage mount | update the desktop app |
| The desktop app's *copies* outside the bundle (Windows `%LOCALAPPDATA%\Reliant\bin\reliant.exe`, NSIS `$INSTDIR\cli\reliant.exe`) | a `reliant.exe.managed-by` file naming `reliant-desktop`, written beside the copy by `electron/src/cli-installer.js` and `electron/build/installer.nsh` | update the desktop app |
| The workspace image (`Dockerfile`, `Dockerfile.debug`) | `-X github.com/reliant-labs/forge/pkg/selfupdate.managedBy=reliant-workspace-image` | the platform updates it |
| A Reliant cloud machine | `RELIANT_DAEMON_TYPE=managed` (`isManagedMachine`) | the platform updates it |
| Homebrew, snap, nix, dpkg/rpm/pacman | path, then the package database | the package manager's command |
| A directory this user cannot write (`/usr/local/bin`) | a write probe | `sudo reliant update`, or move it to `~/.local/bin` |

`release.yml`'s `build-backend` binaries are **not** stamped. They are both the
desktop app's `reliant-backend` and the standalone CLI archives, the same bytes,
so a stamp there would stop every CLI archive from updating itself. The path
rules cover them inside the bundle. `TestWorkspaceImageIsStampedAndTheCLIArchivesAreNot`
and `TestDetectRefusesTheDesktopAppsInstalls` hold this. Electron does not set
`FORGE_MANAGED_BY` in the daemon's environment: it would leak into every agent
shell and mark forge and other forge-built binaries as managed.

## `reliant update`

1. Read the channel (`Check`). `--check` stops here and reports: current,
   newest, whether an update is available, and who owns the install.
2. Refuse an install it does not own, naming the advice above. On Windows
   (where `pkg/selfupdate` cannot swap a running `.exe` yet), refuse with the
   download URL.
3. Ask, unless `--yes` is given. With no terminal and no `--yes`, it refuses.
4. `Prepare`: download the archive, `checksums.txt` and the bundle, verify them,
   check the archive against the *verified* `checksums.txt`, extract it, and
   probe the binary (`reliant version`).
5. `Request` then `ExecPending`: copy the running binary to `previous/`, rename
   the staged one over it, record the swap, and exec the new binary as
   `reliant update --swapped --from <old>`. That hidden run calls
   `selfupdate.Boot` and `Confirm`, then reports. A binary that never confirms
   is put back by the next `Boot`.

A running daemon keeps the old image (the rename leaves its inode alone) until
it restarts. Its next start's `Boot` sees the confirmed swap and does nothing.
