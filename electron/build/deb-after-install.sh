#!/bin/sh
# Reliant — .deb post-install hook
#
# electron-builder installs the app under /opt/Reliant on Debian/Ubuntu. This
# script runs after dpkg unpacks the package; we use it to expose the `reliant`
# CLI on the system PATH by symlinking the embedded Go backend binary into
# /usr/bin.
#
# The same binary serves two roles:
#   - /opt/Reliant/resources/server/linux-<arch>/reliant-backend  (spawned by GUI)
#   - /usr/bin/reliant                                            (CLI on $PATH)
#
# We do NOT copy — a symlink keeps the install a single source of truth and
# means the CLI always matches the GUI's backend.

set -e

APP_DIR="/opt/Reliant"
LINK_TARGET="/usr/bin/reliant"

# Detect the architecture directory that ships with this package.
if [ -x "$APP_DIR/resources/server/linux-arm64/reliant-backend" ]; then
  BIN="$APP_DIR/resources/server/linux-arm64/reliant-backend"
elif [ -x "$APP_DIR/resources/server/linux-amd64/reliant-backend" ]; then
  BIN="$APP_DIR/resources/server/linux-amd64/reliant-backend"
else
  echo "reliant: warning — embedded backend binary not found under $APP_DIR/resources/server" >&2
  exit 0
fi

# Replace any existing symlink. We deliberately do nothing if a non-symlink
# `reliant` already exists (e.g. installed via `brew` on Linux) — overwriting
# would be hostile.
if [ -L "$LINK_TARGET" ] || [ ! -e "$LINK_TARGET" ]; then
  ln -sf "$BIN" "$LINK_TARGET"
fi

exit 0
