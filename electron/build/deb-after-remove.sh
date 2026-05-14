#!/bin/sh
# Reliant — .deb post-remove hook
#
# Companion to deb-after-install.sh. Removes the /usr/bin/reliant symlink only
# if it still points at our embedded backend (so we never delete a user's
# Homebrew/manual CLI install).

set -e

LINK_TARGET="/usr/bin/reliant"

if [ -L "$LINK_TARGET" ]; then
  resolved=$(readlink "$LINK_TARGET" 2>/dev/null || true)
  case "$resolved" in
    /opt/Reliant/resources/server/*/reliant-backend)
      rm -f "$LINK_TARGET"
      ;;
  esac
fi

exit 0
