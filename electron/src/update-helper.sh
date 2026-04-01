#!/bin/bash
# Reliant Update Helper
# This script is spawned by the Electron app to perform manual updates on macOS.
# It waits for the main process to exit, then replaces the app and relaunches.
#
# Usage: update-helper.sh <pid_to_wait_for> <zip_path> <app_path> <temp_dir>

LOG_FILE="${RELIANT_UPDATE_LOG:-/tmp/reliant-update-helper.log}"

log() {
  echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" >> "$LOG_FILE"
}

log "=========================================="
log "Reliant Update Helper started"
log "Arguments: PID=$1 ZIP=$2 APP=$3 TEMP=$4"

WAIT_PID="$1"
ZIP_PATH="$2"
APP_PATH="$3"
TEMP_DIR="$4"

# Validate arguments
if [ -z "$WAIT_PID" ] || [ -z "$ZIP_PATH" ] || [ -z "$APP_PATH" ] || [ -z "$TEMP_DIR" ]; then
  log "ERROR: Missing required arguments"
  log "Usage: update-helper.sh <pid> <zip_path> <app_path> <temp_dir>"
  exit 1
fi

# Verify zip file exists
if [ ! -f "$ZIP_PATH" ]; then
  log "ERROR: ZIP file not found: $ZIP_PATH"
  exit 1
fi

# Verify app path looks valid
if [[ ! "$APP_PATH" == *.app ]]; then
  log "ERROR: APP_PATH doesn't look like a macOS app bundle: $APP_PATH"
  exit 1
fi

# Wait for the main process to exit (with timeout)
log "Waiting for PID $WAIT_PID to exit..."
WAIT_COUNT=0
MAX_WAIT=120  # 60 seconds max wait
while kill -0 "$WAIT_PID" 2>/dev/null; do
  sleep 0.5
  WAIT_COUNT=$((WAIT_COUNT + 1))
  if [ $WAIT_COUNT -ge $MAX_WAIT ]; then
    log "WARNING: Timed out waiting for process to exit, proceeding anyway"
    break
  fi
done
log "Process $WAIT_PID has exited (or timed out after ${WAIT_COUNT} half-seconds)"

# Kill any remaining reliant-backend processes that might hold file locks
# These are spawned by the Electron app and might not exit when parent dies
log "Checking for orphaned reliant-backend processes..."
BACKEND_PIDS=$(pgrep -f "reliant-backend" 2>/dev/null || true)
if [ -n "$BACKEND_PIDS" ]; then
  log "Found orphaned backend processes: $BACKEND_PIDS"
  for pid in $BACKEND_PIDS; do
    log "Killing backend process $pid..."
    kill -TERM "$pid" 2>/dev/null || true
  done
  sleep 2
  # Force kill any that didn't respond to SIGTERM
  for pid in $BACKEND_PIDS; do
    if kill -0 "$pid" 2>/dev/null; then
      log "Force killing backend process $pid..."
      kill -9 "$pid" 2>/dev/null || true
    fi
  done
fi

# Also kill any orphaned crashpad handlers from the old app
log "Checking for orphaned crashpad processes..."
CRASHPAD_PIDS=$(pgrep -f "chrome_crashpad_handler.*reliant" 2>/dev/null || true)
if [ -n "$CRASHPAD_PIDS" ]; then
  log "Found orphaned crashpad processes: $CRASHPAD_PIDS"
  for pid in $CRASHPAD_PIDS; do
    kill -9 "$pid" 2>/dev/null || true
  done
fi

# Small delay to ensure files are released
sleep 1

# Create temp directory
log "Creating temp directory: $TEMP_DIR"
rm -rf "$TEMP_DIR"
mkdir -p "$TEMP_DIR"
if [ $? -ne 0 ]; then
  log "ERROR: Failed to create temp directory"
  exit 1
fi

# Unzip the update
log "Unzipping update from: $ZIP_PATH"
log "Unzipping to: $TEMP_DIR"
unzip -q "$ZIP_PATH" -d "$TEMP_DIR" 2>> "$LOG_FILE"
UNZIP_RESULT=$?
if [ $UNZIP_RESULT -ne 0 ]; then
  log "ERROR: Failed to unzip update (exit code: $UNZIP_RESULT)"
  rm -rf "$TEMP_DIR"
  exit 1
fi

# List contents for debugging
log "Extracted contents:"
ls -la "$TEMP_DIR" >> "$LOG_FILE" 2>&1

# Find the .app in the extracted files (could be nested or at root)
NEW_APP=$(find "$TEMP_DIR" -maxdepth 2 -name "*.app" -type d | head -1)
if [ -z "$NEW_APP" ]; then
  log "ERROR: Could not find .app in extracted files"
  log "Contents of temp dir:"
  find "$TEMP_DIR" -type d >> "$LOG_FILE" 2>&1
  rm -rf "$TEMP_DIR"
  exit 1
fi
log "Found new app at: $NEW_APP"

# Verify the new app has the expected structure
if [ ! -d "$NEW_APP/Contents/MacOS" ]; then
  log "ERROR: New app doesn't have expected macOS bundle structure"
  rm -rf "$TEMP_DIR"
  exit 1
fi

# Backup old app
BACKUP_PATH="${APP_PATH}.backup"
log "Backing up old app to: $BACKUP_PATH"
rm -rf "$BACKUP_PATH" 2>> "$LOG_FILE"
mv "$APP_PATH" "$BACKUP_PATH" 2>> "$LOG_FILE"
if [ $? -ne 0 ]; then
  log "ERROR: Failed to backup old app"
  rm -rf "$TEMP_DIR"
  exit 1
fi

# Move new app into place
log "Installing new app to: $APP_PATH"
mv "$NEW_APP" "$APP_PATH" 2>> "$LOG_FILE"
if [ $? -ne 0 ]; then
  log "ERROR: Failed to install new app"
  log "Attempting to restore backup..."
  mv "$BACKUP_PATH" "$APP_PATH" 2>> "$LOG_FILE"
  rm -rf "$TEMP_DIR"
  exit 1
fi

# Remove quarantine attribute (important for apps downloaded from internet)
log "Removing quarantine attribute..."
xattr -rd com.apple.quarantine "$APP_PATH" 2>> "$LOG_FILE" || true

# Clean up
log "Cleaning up..."
rm -rf "$BACKUP_PATH" 2>> "$LOG_FILE"
rm -rf "$TEMP_DIR" 2>> "$LOG_FILE"
rm -f "$ZIP_PATH" 2>> "$LOG_FILE"

# Verify the new app exists
if [ ! -d "$APP_PATH" ]; then
  log "ERROR: App not found after update!"
  exit 1
fi

# Small delay before relaunch
sleep 1

# Relaunch the app
log "Relaunching app: $APP_PATH"
open "$APP_PATH" 2>> "$LOG_FILE"
OPEN_RESULT=$?
if [ $OPEN_RESULT -ne 0 ]; then
  log "WARNING: 'open' command returned exit code $OPEN_RESULT"
fi

log "Update complete!"
log "=========================================="
exit 0
