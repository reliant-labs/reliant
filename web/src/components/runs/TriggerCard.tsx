// Copyright (c) 2025 Reliant Labs

/**
 * What fired a run nobody started by typing (WORKFLOW_UI.md §4.2, item 2): a
 * one-line inset pinned above the transcript, so a reader knows why a
 * conversation exists before they read it.
 *
 * Deliberately says nothing it cannot back up. The scheduled slot, the
 * prompt as sent and the inputs live on the launch event, which is not on the
 * wire yet (§13 G2); until it is, the card names the source and links to it,
 * and does not show placeholder text that would later change meaning (§4.4).
 */

import { Link } from "@tanstack/react-router";

import { launchKindDisplay } from "@/lib/runStatus";
import { CardInset } from "../forge-ui/card";
import { LaunchKindIcon } from "./RunRow";

interface TriggerCardProps {
  launchKind?: string | null;
  triggerId?: string;
  /** The automation's current name; unset when it has been deleted. */
  triggerName?: string;
  /** The chat whose agent started this run, when known. */
  parent?: { chatId: string; title: string };
}

export function TriggerCard({ launchKind, triggerId, triggerName, parent }: TriggerCardProps) {
  const kind = launchKindDisplay(launchKind).kind;
  // A run a person started has nothing to explain. The marker keeps the
  // absence observable to tests without rendering anything a user sees.
  if (kind === "chat.start") return <span hidden data-testid="trigger-card-absent" />;

  const unattended = kind === "schedule";

  return (
    <CardInset
      padding="sm"
      className="flex flex-wrap items-center gap-x-2 gap-y-1 px-3 text-xs text-muted-foreground"
      data-testid="trigger-card"
    >
      <LaunchKindIcon kind={kind} />
      <span className="text-foreground">
        {kind === "schedule" ? (
          <>
            Scheduled by{" "}
            {triggerId && triggerName ? (
              <Link
                to="/automations/$triggerId"
                params={{ triggerId }}
                className="font-medium hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
              >
                {triggerName}
              </Link>
            ) : (
              "an automation that has since been deleted"
            )}
          </>
        ) : kind === "agent.start_run" ? (
          <>
            Started by an agent
            {parent && (
              <>
                {" in "}
                <Link
                  to="/runs/$runId"
                  params={{ runId: parent.chatId }}
                  className="font-medium hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                >
                  {parent.title || "another run"}
                </Link>
              </>
            )}
            {" · tool call "}
            <code className="font-mono">start_run</code>
          </>
        ) : (
          launchKindDisplay(launchKind, { triggerName }).startedByLine
        )}
      </span>
      {unattended && (
        <span>· Unattended: questions and approvals were answered automatically</span>
      )}
    </CardInset>
  );
}
