# Reliant Electron Desktop App

Packages the web app + Go backend into a native desktop application.

## Architecture

- **Main Process (`src/main.js`)**: App lifecycle, backend process management, window creation
- **Preload Script (`src/preload.js`)**: Safely exposes APIs to the renderer process
- **Supporting Modules** (`src/`): Backend management, window management, auth storage, auto-updates, logging, data migration
- **Web App**: React frontend from `../web/`
- **Go Backend**: API server from the main project directory

## Development

### Prerequisites

- Node.js 18+
- Go 1.25+
- npm

### Setup

```bash
npm install
npm run dev
```

This starts the Go backend (dynamic port via `.env.ports`), Vite dev server on port 3000, and Electron with hot reloading.

### From project root

```bash
npm run start:electron
```

## Building

```bash
# Development build
npm run build

# Production package (outputs to ../dist/)
npm run package

# From project root (full build including backend + web)
npm run build:electron
npm run package:electron
```

## Configuration

The Electron app automatically handles port allocation, backend URL configuration, backend process lifecycle, and platform-specific features.

### Platform Integration

- **macOS**: Native window controls, dock integration
- **Windows**: NSIS installer, system tray
- **Linux**: AppImage distribution

## `reliant` CLI on $PATH

The Electron app ships the same Go binary (`./cmd/reliant/`) as both the
embedded backend (spawned by the GUI) and the `reliant` CLI users invoke from
a terminal. There is only one copy on disk per architecture; the CLI is either
a symlink to it (macOS/Linux) or a stable copy of it (Windows, where symlinks
need admin).

How it gets onto $PATH, per platform:

| Platform | Where         | Mechanism                                         | When |
|----------|---------------|---------------------------------------------------|------|
| macOS    | tiered (see below) | Symlink to `Reliant.app/Contents/Resources/server/mac-<arch>/reliant-backend` | First app launch (silent, no sudo). User can re-run with sudo from Settings → About. |
| Linux .deb | `/usr/bin/reliant` | Symlink, created by `build/deb-after-install.sh` | `apt install` time |
| Linux AppImage | `~/.local/bin/reliant` | Symlink to the AppImage-mounted backend | First app launch (silent). User may need to add `~/.local/bin` to PATH. |
| Windows  | `%LOCALAPPDATA%\Reliant\bin\reliant.exe` and `$INSTDIR\cli\reliant.exe` | Copy + HKCU\Environment\Path edit, via NSIS hook `build/installer.nsh` | NSIS install time. Silent first-run fallback also exists. |

#### macOS directory picker (silent first-run)

Electron inherits `$PATH` from launchd, which is usually just
`/usr/bin:/bin:/usr/sbin:/sbin` and misses Homebrew + user-local dirs. The
installer therefore resolves the **user's effective shell `$PATH`** by spawning
`$SHELL -lc 'echo $PATH'` (zsh or bash, whichever the user's login shell is),
falling back to `process.env.PATH` if the spawn fails.

It then walks a tiered preference list and picks the **first directory that is
BOTH on the user's `$PATH` AND writable without sudo** (writability is
`fs.accessSync(dir, W_OK)`, or the deepest existing ancestor if `dir` doesn't
exist yet — the installer creates it via `mkdirSync(..., { recursive: true })`):

1. `/opt/homebrew/bin` — Apple Silicon Homebrew default
2. `/usr/local/bin` — Intel Homebrew / classic Unix system bindir
3. `~/.local/bin` — XDG user-local
4. `~/bin` — legacy user bindir

**Fallback chain** if no tier is both on-PATH and writable:

- Pick the first tier that is writable even if it is **not** on `$PATH`, install
  there, and emit a warning (logged + surfaced via the `getCliStatus` IPC
  channel the onboarding step uses) telling the user to add that dir to their
  `$PATH` manually.
- If nothing is writable at all, defer to the **osascript sudo prompt** wired up
  to the Settings → About → Install CLI Command button (targets
  `/usr/local/bin`).

Each tier's `(onPath, writable)` status is logged at install time for debugging.

### Verifying after a build

After `npm run dist:mac` / `dist:win` / `dist:linux`, install the artifact and run:

```sh
# macOS
which reliant && reliant version

# Linux (deb)
dpkg -L reliant | grep /usr/bin/reliant
which reliant && reliant version

# Linux (AppImage)
# 1) Run the AppImage once to trigger the first-run installer.
# 2) Check ~/.local/bin/reliant exists; ensure ~/.local/bin is on $PATH.
ls -la ~/.local/bin/reliant
reliant version

# Windows (cmd or PowerShell)
where.exe reliant
reliant version
# If `reliant` isn't found, open a NEW terminal — NSIS broadcasts
# WM_SETTINGCHANGE so new shells pick up the user PATH edit.
```

If silent install fails (e.g. read-only `/usr/local/bin`, locked-down corporate
machine), the **Settings → About → Install CLI Command** button in the GUI
re-runs the installer, escalating to `osascript`/`pkexec` on macOS/Linux for
a system-wide install in `/usr/local/bin`.

### Source of truth

- `src/cli-installer.js` — silent first-run + sudo-fallback shared logic
- `build/installer.nsh` — Windows NSIS install/uninstall hooks
- `build/deb-after-install.sh` / `deb-after-remove.sh` — Debian package hooks
- `scripts/install-cli.sh` — manual standalone installer (kept for users who
  installed via .dmg only and want to (re)install the CLI from a terminal)

## API Communication

The web app detects when running in Electron and connects to the local backend:
- **HTTP API**: `http://localhost:<backend-port>/api`
- **gRPC Streaming**: Real-time communication via gRPC

## File Structure

```
electron/
├── src/
│   ├── main.js              # Main Electron process
│   ├── preload.js            # Preload script for renderer
│   ├── backend-manager.js    # Go backend lifecycle
│   ├── backend-auth.js       # Backend authentication
│   ├── auth-storage.js       # Auth token storage
│   ├── window-manager.js     # Window creation/management
│   ├── window-config.js      # Window configuration
│   ├── window-state-client.js # Window state persistence
│   ├── browser-manager.js    # External browser handling
│   ├── chunked-downloader.js # Download management
│   ├── data-migration.js     # Data migration utilities
│   ├── logger.js             # Logging
│   └── update-helper.sh      # Update script
├── electron-builder.js           # Build config (production)
├── electron-builder.common.js    # Shared build config
├── electron-builder.dev.js       # Build config (dev)
├── electron-builder.pr.js        # Build config (PR)
├── electron-builder.alpha.js     # Build config (alpha)
├── package.json
└── README.md
```

## Troubleshooting

### Backend fails to start
Check that a port was allocated in `.env.ports` and that Go is in PATH. Check console logs in DevTools.

### Build fails
Ensure web app builds successfully first (`npm run build` in `web/`), then verify Go backend compiles.
