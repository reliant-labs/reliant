// Copyright (c) 2025 Reliant Labs

/**
 * How a background process's state reads, in one place.
 *
 * The Commands panel, the log viewer, the composer's background-work strip,
 * a shell tool call's live card and the mobile panel all show the same
 * process states; each used to carry its own switch (and its own colours:
 * green "running", which the styling contract keeps for success). This is the
 * one answer, rendered with forge-ui's <Status>: in progress is a neutral dot
 * that pulses, a clean exit is success, a failure is danger, a stop is
 * neutral.
 */

import type { StatusTone } from "../components/forge-ui/status";
import { BackgroundProcessStatus } from "../gen/reliant/v1/common_pb";

export interface ProcessStatusView {
  /** The word: "Running", "Completed", "Exit 2", "Stopped". */
  label: string;
  /** The word where a row is narrow: "Run", "OK", "E2", "Stop". */
  shortLabel: string;
  tone: StatusTone;
  /** Still running: the dot pulses and Stop applies. */
  running: boolean;
}

export function processStatusView(status: BackgroundProcessStatus, exitCode?: number): ProcessStatusView {
  switch (status) {
    case BackgroundProcessStatus.RUNNING:
      return { label: "Running", shortLabel: "Run", tone: "neutral", running: true };
    case BackgroundProcessStatus.COMPLETED:
      return exitCode === undefined || exitCode === 0
        ? { label: "Completed", shortLabel: "OK", tone: "success", running: false }
        : { label: `Exit ${exitCode}`, shortLabel: `E${exitCode}`, tone: "danger", running: false };
    case BackgroundProcessStatus.FAILED:
      return {
        label: exitCode === undefined ? "Failed" : `Failed (exit ${exitCode})`,
        shortLabel: "Fail",
        tone: "danger",
        running: false,
      };
    case BackgroundProcessStatus.KILLED:
      return { label: "Stopped", shortLabel: "Stop", tone: "neutral", running: false };
    case BackgroundProcessStatus.KILLED_EXTERNALLY:
      return { label: "Stopped outside Reliant", shortLabel: "Stop", tone: "warning", running: false };
    case BackgroundProcessStatus.STALE:
      return { label: "Lost on restart", shortLabel: "Lost", tone: "warning", running: false };
    default:
      return { label: "Unknown", shortLabel: "?", tone: "neutral", running: false };
  }
}

/** Compact elapsed time between two instants: "8s", "4m", "1h12m". */
export function formatElapsed(startMs: number, endMs: number): string {
  const seconds = Math.max(0, Math.floor((endMs - startMs) / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours}h${minutes % 60}m`;
  return `${Math.floor(hours / 24)}d`;
}

/** An RFC3339 time as epoch ms, or null when absent or unparseable. */
export function parseProcessTime(value: string | undefined): number | null {
  if (!value) return null;
  const parsed = new Date(value).getTime();
  return Number.isNaN(parsed) ? null : parsed;
}

/**
 * The part of a command a row names it by: the script of `npm run dev`, the
 * target of `make build` / `task test`, else the command itself.
 */
export function shortCommand(command: string, max = 48): string {
  const parts = command.trim().split(/\s+/);
  if ((parts[0] === "npm" || parts[0] === "pnpm" || parts[0] === "yarn") && parts[1] === "run" && parts[2]) {
    return parts[2];
  }
  if ((parts[0] === "make" || parts[0] === "task") && parts[1] && parts.length === 2) {
    return parts[1];
  }
  const firstLine = command.split("\n")[0]!.trim();
  return firstLine.length > max ? `${firstLine.slice(0, max - 1)}…` : firstLine;
}
