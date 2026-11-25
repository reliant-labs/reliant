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

## The daemon: `daemon start --auto-update`

Self-run auto-update is opt-in (owner decision D7):
`DaemonConfig.auto_update`, which is `--auto-update`,
`RELIANT_DAEMON_AUTO_UPDATE` or the config file's `auto_update`. A managed
machine never builds an updater: its image is its version.

| Piece | Where |
| --- | --- |
| Boot, the updater, the in-process retry after a failed swap | `cmd/reliant/cmd/daemon_selfupdate.go` |
| `cliupdate.Daemon`: `Stage`, `Apply`, `ExecPending`, `Confirm`, `Disable` | `internal/cliupdate/daemon.go` |
| When to install: staging loop, quiet moment, drain, states | `internal/toolexec/daemonruntime/upgrade_self.go` |

Every `daemon start` on a person's machine first calls `cliupdate.Boot`, with or
without the flag. After a swap that arms forge's revert watchdog; the daemon
confirms the swap when the gateway accepts it (`recordStream(StreamConnected)`),
within `cliupdate.DefaultConfirmWithin` (5 minutes). That is longer than forge's
60s, because a revert marks the release failed on this machine. A binary that
died twice without confirming is reverted at the next start.

With auto-update on (`cliupdate.Daemon.AutoUpdate`: opted in, self-owned, not
Windows), the runtime:

1. **Stages** at start and every `DefaultStageEvery` (6h): download, verify,
   probe. It reports `STAGED` (`DAEMON_UPGRADE_STATE_STAGED = 6`, additive).
   Nothing newer is not news. A release that does not verify is `FAILED`, and
   the daemon tries again in 30 minutes.
2. **Waits for a quiet moment**: no tool call running, no background process
   running that the exec would stop, and no tool or terminal activity
   (`isActivity`: tool requests, terminal input, resize and attach, `exec.*`,
   `terminal.*` and `mcp.call_tool` commands, and each tool call's end) for 10
   minutes. A start counts as activity. Background processes survive the exec
   (R5, below), so they do not hold an install back; the ones that cannot
   (`SurvivesSelfUpdate` false: credentials-none commands, foreground commands
   moved to the background) do, and the wait never kills one.
3. A **RestartRequested** only makes it sooner: at security or emergency
   urgency, once its deadline has passed, the daemon installs without the idle
   time and despite those background processes. Any other urgency changes
   nothing.
4. **Drains** (`UPDATING`, refusing new tool requests with the undelivered
   class) for at most a minute. Work still running then declines the install
   unless it was forced. Nothing is swapped, and the daemon goes back to
   `STAGED`.
5. **`DRAINED`**, then an upgrade exit stops the runtime. `daemon start`
   (`runSelfUpdating`) hands the background processes off
   (`BackgroundManager.HandOffForExec`) and calls `ExecPending`: swap, then
   exec with the same argv, environment and pid. If the swap fails, the old
   binary is still in place, so the handoff is undone and the runtime starts
   again on it with auto-update off for that process (`Disable`). An
   unsupervised daemon is never left down.

### Background processes across the exec (R5)

Forge `docs/proposals/self-upgrade.md` §5. The exec keeps the pid, so the
commands the daemon started are still its children; what used to kill them is
that their output went through pipes whose read ends are close-on-exec.

| Piece | Where |
| --- | --- |
| File-backed output: the child writes `<journal>/<id>.stdout` / `.stderr` (O_APPEND), the daemon tails them | `internal/llm/tools/shell/background_fileoutput.go` |
| Handoff, the non-reaping exit wait, adoption, orphan stop | `internal/llm/tools/shell/background_survive.go` |
| Journal: pid, start time, pgid, cgroup, handoff offsets; `Install` | `internal/llm/tools/shell/background_journal.go` |
| Process identity (start time, parent, zombie) | `internal/osutil/identity_*.go` |

- **Only an auto-updating daemon** (`installBackgroundJournal`'s
  `restartsInPlace`) starts processes with file-backed output, on Linux and
  macOS. Every other daemon keeps pipes: nothing changes for the desktop app,
  managed machines or a daemon without `--auto-update`.
- **Handoff** (just before the exec): each file-backed process's output is
  read to the last whole line and journaled, and the record gets
  `handed_off` with the byte offsets to read on from. Everything else is
  stopped as at any restart: piped processes, credentials-none commands (they
  die with the daemon by owner decision, 2026-11-10), foreground commands
  moved to the background. PTY terminals die at the exec as before.
- **Exit without reaping, and one owner**: a file-backed process's exit is
  watched with `waitid(WNOWAIT)` (Linux; macOS polls `kinfo_proc` for its own
  zombie child). Whether this image may then reap it is decided under the
  process's ownership lock (`handoffState`: owned, handing off, handed off,
  reaping). Only an owned process is reaped, and only the image that reaped
  it finishes its output and removes its files. One that exits while handing
  off or handed off stays a zombie with its files: the old image's waiter
  parks, and the new image reaps it for its real exit status. An undo (the
  exec failed) makes it owned again and restarts a parked waiter. No wrapper
  process and no exit file: the exec keeps the parent.
- **Adoption** (`BackgroundJournal.Install` in the new image): a handed-off
  record whose pid is alive (a zombie counts), whose parent is this process
  and whose start time matches the journaled one is the same process. It keeps
  its id, output and sequence numbers, is tailed from the offsets, waited for
  with `wait4`, can be killed, and its proctree cgroup is taken back
  (`proctree.Limiter.Reattach`). Anything else is marked `stale`, as before.
- **Normal stop and crash are unchanged**: `KillAllRunning` still runs on
  every other exit. After a crash the next daemon is a new process and adopts
  nothing; a file-backed process the crash left running (no pipe breaks under
  it) is stopped at the next start while its start time proves it is the
  journaled one (`stopOrphan`).
- **Disk**: the output files are removed when the process ends (the journal's
  `.out` holds its output). While it runs, what was read is released with a
  punched hole every MiB on Linux; on macOS a file keeps its whole output until
  the process ends.

Every self-update wait reads `upgradeDrain.now`, so the tests
(`upgrade_self_test.go`) drive the idle time and deadlines on a fake clock.

Without auto-update, a person's daemon behaves as it did before: on a
RestartRequested it reports `AWAITING_OWNER` and keeps serving. When its
install is self-owned, its log and console notice add "run `reliant update`, or
start the daemon with --auto-update".
