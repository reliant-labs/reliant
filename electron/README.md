# Reliant Electron Desktop App

This directory contains the Electron wrapper for the Reliant AI coding assistant, which packages the web application with the Go backend into a native desktop application.

## Architecture

The Electron app consists of:
- **Main Process (`src/main.js`)**: Manages the application lifecycle, backend process, and window creation
- **Preload Script (`src/preload.js`)**: Safely exposes APIs to the renderer process
- **Web App**: The React frontend from `../web/` directory
- **Go Backend**: The API server from the main project directory

## Development

### Prerequisites

- Node.js 18+
- Go 1.25+
- npm

### Setup

1. Install dependencies:
```bash
npm install
```

2. Start development mode:
```bash
npm run dev
```

This will:
- Start the Go backend on a dynamically assigned port (see `.env.ports`)
- Start the Vite dev server on port 3000
- Launch Electron with hot reloading

### Alternative: Run from project root

```bash
# From the project root
npm run start:electron
```

## Building

### Development Build

```bash
npm run build
```

### Production Package

```bash
npm run package
```

This creates platform-specific packages in the `../dist/` directory.

### From Project Root

```bash
# Full build including backend and web
npm run build:electron

# Package only
npm run package:electron
```

## Configuration

The Electron app automatically:
- Finds available ports for backend and frontend
- Configures the web app to connect to the correct backend URL
- Handles backend process lifecycle
- Provides platform-specific features

## Features

### Multi-Instance Support

The backend can support multiple web app instances running simultaneously, each connecting via gRPC streaming to the same Go backend process.

### Platform Integration

- **macOS**: Native window controls, dock integration
- **Windows**: NSIS installer, system tray
- **Linux**: AppImage distribution

### Security

- Context isolation enabled
- Node integration disabled in renderer
- Preload script for safe API exposure
- External link handling

## File Structure

```
electron/
├── src/
│   ├── main.js          # Main Electron process
│   └── preload.js       # Preload script for renderer
├── build/
│   ├── icon.png         # App icon
│   └── entitlements.mac.plist  # macOS entitlements
├── package.json         # Electron app config
└── README.md           # This file
```

## API Communication

The web app automatically detects when running in Electron and configures itself to communicate with the local backend:

- **HTTP API**: Direct connection to `http://localhost:<backend-port>/api`
- **gRPC Streaming**: Real-time communication via gRPC streaming to the backend

## Troubleshooting

### Backend fails to start

1. Check that a port was allocated (see `.env.ports`)
2. Ensure Go is installed and in PATH
3. Check console logs in DevTools

### Web app can't connect

1. Verify backend is running (check console)
2. Check network tab in DevTools for failed requests
3. Ensure firewall allows local connections

### Build fails

1. Ensure all dependencies are installed
2. Check that web app builds successfully first
3. Verify Go backend compiles

## Development Tips

- Use `Ctrl+Shift+I` (or `Cmd+Opt+I` on macOS) to open DevTools
- Backend logs appear in the main Electron console
- Frontend logs appear in the renderer DevTools console
- Set `NODE_ENV=development` for additional debug output