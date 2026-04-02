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
