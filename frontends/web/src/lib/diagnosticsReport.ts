/**
 * The text "Copy diagnostics" puts on the clipboard: what a support thread
 * needs first — which build, which server, what the live stream is doing, and
 * the last errors this device saw (lib/clientDiagnostics.ts).
 *
 * Pure, so the format is pinned by a test and the screen only gathers facts.
 * No secrets go in: no tokens, no message bodies, ids only.
 */

import type { ClientErrorEntry } from "./clientDiagnostics";

export interface DiagnosticsFacts {
  app: string | null;
  server: string;
  release: string | null;
  url: string;
  userAgent: string;
  viewport: string;
  online: boolean;
  surface: string;
  stream: {
    status: string;
    lastSequence: number;
    subscribedChatId: string | null;
    daemonLastSeen: number | null;
  };
  userId: string | null;
  projectId: string | null;
  generatedAt: string;
}

function ago(unixSeconds: number | null, now: number): string {
  if (!unixSeconds) return "never";
  const seconds = Math.max(0, Math.round(now / 1000 - unixSeconds));
  return `${seconds}s ago`;
}

export function formatDiagnosticsReport(facts: DiagnosticsFacts, errors: readonly ClientErrorEntry[]): string {
  const now = Date.parse(facts.generatedAt) || Date.now();
  const lines = [
    "Reliant diagnostics",
    `Generated: ${facts.generatedAt}`,
    "",
    `App: ${facts.app ?? "unknown"}`,
    `Server: ${facts.server}`,
    ...(facts.release ? [`Release: ${facts.release}`] : []),
    `Surface: ${facts.surface}`,
    `URL: ${facts.url}`,
    `User agent: ${facts.userAgent}`,
    `Viewport: ${facts.viewport}`,
    `Online: ${facts.online ? "yes" : "no"}`,
    `User: ${facts.userId ?? "signed out"}`,
    `Project: ${facts.projectId ?? "none"}`,
    "",
    `Update stream: ${facts.stream.status}`,
    `  last sequence: ${facts.stream.lastSequence}`,
    `  subscribed chat: ${facts.stream.subscribedChatId ?? "none"}`,
    `  machine heartbeat: ${ago(facts.stream.daemonLastSeen, now)}`,
    "",
    `Recent errors (${errors.length}):`,
  ];
  if (errors.length === 0) lines.push("  none");
  // Newest first: the one that prompted the report is usually the last.
  for (const error of [...errors].reverse()) {
    lines.push(`- ${error.at} [${error.source}]${error.count > 1 ? ` x${error.count}` : ""} ${error.path}`);
    lines.push(`  ${error.message}`);
    if (error.stack) {
      for (const frame of error.stack.split("\n").slice(1)) lines.push(`    ${frame.trim()}`);
    }
  }
  return lines.join("\n");
}
