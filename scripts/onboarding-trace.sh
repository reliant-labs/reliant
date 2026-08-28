#!/usr/bin/env bash
# Tail the onboarding redeem→start trace.
#
# Renderer logs reach the file via Electron IPC (electronAPI.log →
# ipcMain "log-from-renderer"), so they land in .reliant/logs/main.log
# prefixed [Renderer] — NOT in data/logs/reliant.log, which is the Go
# backend's sink and carries none of this.
#
# Usage:
#   scripts/onboarding-trace.sh          # follow live
#   scripts/onboarding-trace.sh --recent # last 50 trace lines and exit
set -euo pipefail

LOG="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/.reliant/logs/main.log"

if [[ ! -f "$LOG" ]]; then
  echo "No log at $LOG — is the Electron app running?" >&2
  exit 1
fi

if [[ "${1:-}" == "--recent" ]]; then
  grep "onboarding-trace" "$LOG" | tail -50
else
  echo "Following $LOG — redeem a DEVTEST code now. Ctrl-C to stop."
  tail -f "$LOG" | grep --line-buffered "onboarding-trace"
fi
