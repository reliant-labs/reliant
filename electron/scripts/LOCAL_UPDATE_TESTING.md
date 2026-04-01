# Local Update Testing Guide

This guide explains how to test the app update flow locally without pushing releases.

## Overview

The update system can be tested locally by:
1. Building two versions of the app (old and new)
2. Running a local HTTP server with the "new" version's update files
3. Running the "old" version with `RELIANT_UPDATE_URL` pointing to your local server

Since both versions are unsigned, updates work between them without code signing issues.

## Prerequisites

- Node.js and npm installed
- Go installed (for backend build)
- Python 3 (for the local HTTP server)
- macOS (for testing macOS updates)

## Step-by-Step Testing

### Step 1: Build the "Old" Version

First, create a version of the app that will be "updated from":

```bash
# From the project root directory

# Edit electron/package.json - change version to something lower
# For example, change "0.2.5-rc1" to "0.2.4-rc1"
# (You can use any version lower than your "new" version)

# Build and install the app
./scripts/quick-install.sh --rebuild

# The app is now installed at /Applications/Reliant.app
# Move it somewhere safe before building the new version
mv /Applications/Reliant.app ~/Desktop/Reliant-OLD.app
```

### Step 2: Build the "New" Version

Now build the version that will be the "update target":

```bash
# Edit electron/package.json - restore or increase the version number
# For example: "0.2.5-rc1" (higher than the old version)

# Build and install the app
./scripts/quick-install.sh --rebuild

# The build artifacts you need are in electron/dist/:
# - Reliant-X.X.X-mac-arm64.zip (the update package)
# - dev-mac.yml (the update metadata - rename to alpha-mac.yml for testing)
```

### Step 3: Set Up the Local Update Server

```bash
# Copy the update files to the test server directory
mkdir -p ~/.reliant-update-test-server
cp electron/dist/Reliant-*-mac-arm64.zip ~/.reliant-update-test-server/

# The dev build generates dev-mac.yml, but the app looks for alpha-mac.yml
# (because RC versions use the alpha channel), so rename it when copying:
cp electron/dist/dev-mac.yml ~/.reliant-update-test-server/alpha-mac.yml

# Start the local server (from project root)
./electron/scripts/local-update-server.sh

# The server will run on http://localhost:8080
```

### Step 4: Run the Old Version with Custom Update URL

In a new terminal:

```bash
# Run the old version, pointing it to your local update server
# IMPORTANT: Use --env flag, not shell prefix (macOS open doesn't inherit shell env vars)
open --env RELIANT_UPDATE_URL=http://localhost:8080/ ~/Desktop/Reliant-OLD.app
```

### Step 5: Trigger the Update

1. Open the app's Settings
2. Go to the "About" or "Updates" section
3. Click "Check for Updates"
4. The app should find the new version from your local server
5. Click "Download" to download the update
6. Click "Restart" to install the update
7. Verify the app restarts with the new version

## Checking Logs

To see what's happening during the update:

```bash
# View electron-log output (main process logs)
tail -f ~/Library/Logs/Reliant/main.log

# View update helper logs (if manual installation is triggered)
tail -f /tmp/reliant-update-helper.log
```

## Troubleshooting

### "No update available"

- Make sure the version in your "new" build is higher than the "old" build
- Check that `alpha-mac.yml` (or `latest-mac.yml`) is in the server directory
- Verify the server is running: `curl http://localhost:8080/alpha-mac.yml`

### Update downloads but doesn't install

- Check the update helper log: `cat /tmp/reliant-update-helper.log`
- Check the main app log: `tail -100 ~/Library/Logs/Reliant/main.log`
- Verify the .zip file is valid: `unzip -t ~/.reliant-update-test-server/*.zip`

### CORS or network errors

- Make sure you're using `http://` not `https://` for localhost
- Check that the server is running on the correct port

## Understanding the YML Files

The `latest-mac.yml` or `alpha-mac.yml` file contains update metadata:

```yaml
version: 0.2.5-rc35
files:
  - url: Reliant-0.2.5-rc35-mac-arm64.zip
    sha512: <base64-encoded-sha512-hash>
    size: 123456789
  - url: Reliant-0.2.5-rc35-mac-x64.zip
    sha512: <base64-encoded-sha512-hash>
    size: 123456789
path: Reliant-0.2.5-rc35-mac-arm64.zip
sha512: <base64-encoded-sha512-hash>
releaseDate: '2025-01-28T12:00:00.000Z'
```

The app uses this to:
1. Check if a newer version is available
2. Download the correct file for the architecture
3. Verify the download integrity via SHA512

## Reverting to Production

To stop using the local server and go back to production updates:

Simply don't set `RELIANT_UPDATE_URL`. The app defaults to the production server.

```bash
# This uses production updates (default behavior)
open /Applications/Reliant.app
```

## Quick Reference

| Command | Description |
|---------|-------------|
| `./scripts/quick-install.sh --rebuild` | Build and install the app locally |
| `./electron/scripts/local-update-server.sh` | Start local update server on port 8080 |
| `./electron/scripts/local-update-server.sh 9000` | Start on custom port |
| `open --env RELIANT_UPDATE_URL=http://localhost:8080/ ~/Desktop/Reliant-OLD.app` | Run old app with local server |
| `tail -f ~/Library/Logs/Reliant/main.log` | Watch app logs |
| `curl http://localhost:8080/alpha-mac.yml` | Test server is working |
| `cp electron/dist/dev-mac.yml ~/.reliant-update-test-server/alpha-mac.yml` | Copy and rename YML file |
