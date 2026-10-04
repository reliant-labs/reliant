// Copyright (c) 2025 Reliant Labs

/**
 * The runs an agent started from this chat, as a compact strip in the chat's
 * header (§14.1 decision 4). Agent-started runs are never in the sidebar, so
 * this — and Runs — is where a user finds what their agent kicked off.
 *
 * It renders nothing at all until the chat has a child, so an ordinary chat's
 * header is unchanged. Mount it for the OPEN chat only: each instance is one
 * ListRuns query, and a sidebar row must never pay for it.
 */

import { Link } from "@tanstack/react-router";
import { Bot } from "lucide-react";

import { useChildRuns } from "@/hooks/run-queries";
import { runStatusFromDisplayState } from "@/lib/runStatus";
import { useCapability } from "@/lib/surfaceContext";
import { RunStatusDot } from "../ui/RunStatusIndicator";

export function ChildRunsStrip({ chatId }: { chatId: string }) {
  // No Runs area on this surface (the /m header): nothing to link to, so do
  // not even ask.
  const runsArea = useCapability("runsArea");
  const children = useChildRuns(chatId, runsArea);
  const runs = children.data?.runs ?? [];
  if (!runsArea || runs.length === 0) return null;

  const more = children.data?.hasMore ?? false;
  const count = more ? `${runs.length}+` : String(runs.length);
  const label = `${count} ${runs.length === 1 && !more ? "run" : "runs"} started here`;

  return (
    <div
      className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground"
      data-testid="child-runs-strip"
    >
      <Bot className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <Link
        to="/workflows/runs"
        search={{ parent: chatId }}
        className="rounded-sm font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      >
        {label}
      </Link>
      <ul className="flex min-w-0 flex-wrap items-center gap-1" aria-label="Runs started here">
        {runs.map((run) => {
          const status = runStatusFromDisplayState(run.displayState, run.outcome);
          return (
            <li key={run.chatId} className="min-w-0">
              <Link
                to="/workflows/runs/$runId"
                params={{ runId: run.chatId }}
                title={`${run.title || "Untitled run"} · ${status.label}`}
                className="inline-flex max-w-[12rem] items-center gap-1.5 rounded-full border border-border/60 bg-background px-2 py-0.5 hover:bg-muted/50 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
              >
                <RunStatusDot status={status} size="sm" />
                <span className="truncate">{run.title || "Untitled run"}</span>
              </Link>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
