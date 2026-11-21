// Copyright (c) 2025 Reliant Labs

/**
 * The change's audit trail (ListChangeEvents), newest first: who did what,
 * whether it was a person, an agent acting for one, or an automation token,
 * and how a gated action was approved. People and agents share this one
 * trail, because they act through the same API.
 */

import { useId } from "react";

import Card from "@/components/forge-ui/card";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { useChangeEvents } from "@/hooks/changes-queries";
import type { ChangeEvent } from "@/services/controlPlane/changes/changesTypes";
import { describeChangeError } from "@/services/controlPlane/changes/errors";

const ACTIONS: Record<string, string> = {
  create: "created it",
  update_draft: "edited the draft",
  set_preview: "changed the preview list",
  launch: "launched it",
  ramp: "changed the share",
  roll_out: "rolled it out",
  stop: "stopped it",
  end_run: "ended the run",
};

const ACTORS: Record<string, string> = { person: "", agent: " (an agent acting for them)", token: " (automation token)" };

const APPROVALS: Record<string, string> = { console_session: "approved in the console", step_up: "approved with an emailed code" };

function render(value: unknown): string {
  if (value === undefined || value === null || value === "") return "none";
  return typeof value === "string" ? value : JSON.stringify(value);
}

function describe(event: ChangeEvent): string {
  const who = `${event.actorId || "Someone"}${ACTORS[event.actorKind] ?? ""}`;
  const what = ACTIONS[event.action] ?? event.action;
  const approved = APPROVALS[event.approvalMethod];
  return `${who} ${what}${approved ? `, ${approved}${event.approvedBy && event.approvedBy !== event.actorId ? ` by ${event.approvedBy}` : ""}` : ""}`;
}

export function ChangeHistory({ changeId }: { changeId: string }) {
  const id = useId();
  const events = useChangeEvents(changeId);
  return (
    <Card padding="md" aria-labelledby={`${id}-title`} className="space-y-3">
      <h2 id={`${id}-title`} className="text-section text-ink">
        History
      </h2>
      {events.isLoading ? (
        <SkeletonLoader variant="text" count={3} />
      ) : events.error ? (
        <p className="text-sm text-ink-muted">{describeChangeError(events.error)}</p>
      ) : (events.data?.events.length ?? 0) === 0 ? (
        <p className="text-sm text-ink-muted">Nothing has happened to this change yet.</p>
      ) : (
        <ol className="space-y-2" aria-label="History" data-testid="change-history">
          {events.data!.events.map((event) => (
            <li key={event.id || `${event.revision}-${event.action}`} className="text-ui">
              <div className="flex flex-wrap items-baseline gap-x-2">
                <span className="text-ink">{describe(event)}</span>
                <span className="text-meta text-ink-muted tabular-nums">
                  r{event.revision}
                  {event.createdAt ? ` · ${event.createdAt.toLocaleString()}` : ""}
                </span>
              </div>
              {event.diff && Object.keys(event.diff).length > 0 && (
                <ul className="mt-0.5 text-meta text-ink-muted">
                  {Object.entries(event.diff).map(([field, change]) => (
                    <li key={field}>
                      <span className="font-mono">{field}</span>: {render(change?.from)} → {render(change?.to)}
                    </li>
                  ))}
                </ul>
              )}
            </li>
          ))}
        </ol>
      )}
    </Card>
  );
}
