// Copyright (c) 2025 Reliant Labs

/**
 * "Waiting for machine", as one component (WORKFLOW_UI.md §9.2).
 *
 * A run can be live and blocked on a machine: its last tool call hit a
 * daemon that is suspended or still starting. The server says so
 * structurally (`ChatActivity.WAITING_FOR_DAEMON`, G7); this component says
 * what that means for the machine's CURRENT state, from the daemon registry,
 * and offers the one action that helps.
 *
 *   suspended  → asleep; "Wake it" (the resume ResumeDaemonPill uses)
 *   starting   → starting…, a pulsing dot, no action (it clears on attach)
 *   offline    → open Reliant on that machine; no action (§14.1 decision 6)
 *   deleted    → the machine was removed
 *   quota      → the shared resume copy, plus Billing (only known after a
 *                refused wake; the registry does not report entitlement)
 *
 * It is a banner under the run header, not a toast and not a transcript
 * message: it is the run's state.
 */

import { useState } from "react";

import type { Chat } from "@/api/client";
import { ChatActivity } from "@/gen/reliant/v1/chat_pb";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";
import { useDaemonList, useResumeDaemon } from "@/hooks/useOnboardingQueries";
import { useGoToBilling } from "@/hooks/useGoToBilling";
import { useTriggers } from "@/hooks/trigger-queries";
import { isQuotaResumeError, formatResumeError } from "@/lib/daemon-resume";
import { cn } from "@/lib/utils";
import { useActivityStore } from "@/store/activityStore";
import StatusDot from "../forge-ui/status_dot";

type MachineState = "suspended" | "starting" | "offline" | "deleted" | "online" | "unknown";

function machineState(status: DaemonStatus | undefined): MachineState {
  switch (status) {
    case undefined:
      return "deleted";
    case DaemonStatus.SUSPENDED:
      return "suspended";
    case DaemonStatus.PENDING:
      return "starting";
    case DaemonStatus.DISCONNECTED:
    case DaemonStatus.FAILED:
      return "offline";
    case DaemonStatus.ACTIVE:
    case DaemonStatus.IDLE:
      return "online";
    default:
      return "unknown";
  }
}

interface MachineStatusProps {
  daemonId: string;
  /** The run was started by an automation (adds the schedule note). */
  automation?: boolean;
  className?: string;
}

export function MachineStatus({ daemonId, automation, className }: MachineStatusProps) {
  const daemons = useDaemonList();
  const goToBilling = useGoToBilling();
  // The raw refusal decides whether Billing is the fix; the copy is shared.
  const [resumeError, setResumeError] = useState("");
  const resume = useResumeDaemon({
    onError: (err) => setResumeError(err instanceof Error ? err.message : "Failed to resume environment"),
  });

  // Until the registry answers, "not in the list" would read as deleted.
  if (daemons.isLoading || !daemons.data) return null;

  const daemon = daemons.data.find((d) => d.daemonId === daemonId);
  const state = machineState(daemon?.status);
  const name = daemon?.hostname || "This machine";
  const waking = resume.isPending;

  let body: React.ReactNode;
  switch (state) {
    case "suspended":
      body = (
        <>
          <p className="text-sm text-foreground">
            <span className="font-semibold">{name}</span> is asleep. This run is waiting for it.
          </p>
          {automation && (
            <p className="text-sm text-muted-foreground">
              Schedules wake it when they start; it went to sleep during this run.
            </p>
          )}
        </>
      );
      break;
    case "starting":
      body = (
        <p className="flex items-center gap-2 text-sm text-foreground">
          <span data-testid="machine-status-starting-dot">
            <StatusDot variant="pending" pulse />
          </span>
          <span>
            <span className="font-semibold">{name}</span> is starting…
          </span>
        </p>
      );
      break;
    case "offline":
      body = (
        <p className="text-sm text-foreground">
          <span className="font-semibold">{name}</span> is offline. Open Reliant on that machine to continue.
        </p>
      );
      break;
    case "deleted":
      body = <p className="text-sm text-foreground">The machine this run used was removed.</p>;
      break;
    default:
      // Online (it attached; the run state will catch up) or unknown.
      body = (
        <p className="text-sm text-foreground">
          This run is waiting for <span className="font-semibold">{name}</span>.
        </p>
      );
  }

  return (
    <div
      role="status"
      data-testid="machine-status"
      className={cn(
        "flex flex-wrap items-start justify-between gap-3 rounded-md border border-border border-l-2 border-l-warning bg-card px-4 py-3",
        className,
      )}
    >
      <div className="min-w-0 space-y-1">
        {body}
        {resumeError && (
          <p className="text-sm text-destructive-ink" role="alert">
            {formatResumeError(resumeError)}
          </p>
        )}
      </div>
      <div className="flex shrink-0 items-center gap-2">
        {state === "suspended" && (
          <button
            type="button"
            disabled={waking}
            onClick={() => {
              setResumeError("");
              resume.mutate(daemonId);
            }}
            className="inline-flex h-8 items-center rounded-md border border-border px-3 text-sm font-medium text-foreground transition-colors hover:bg-muted/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 disabled:opacity-60"
          >
            {waking ? "Waking…" : "Wake it"}
          </button>
        )}
        {resumeError && isQuotaResumeError(resumeError) && (
          <button
            type="button"
            onClick={goToBilling}
            className="inline-flex h-8 items-center rounded-md border border-border px-3 text-sm font-medium text-foreground transition-colors hover:bg-muted/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            Billing
          </button>
        )}
      </div>
    </div>
  );
}

/**
 * The banner on run detail: rendered only while the run is waiting for its
 * machine. Activity follows the live stream (the activity store, written by
 * `chat_activity_changed`), falling back to the loaded chat until an event
 * arrives. The machine is the chat's own, else its automation's.
 */
export function RunMachineBanner({ chat }: { chat: Chat }) {
  const liveActivity = useActivityStore((state) => state.activities.get(chat.id));
  const activity = liveActivity ?? chat.activity;
  const triggers = useTriggers();

  if (activity !== ChatActivity.WAITING_FOR_DAEMON) return null;

  const triggerDaemonId = chat.triggerId
    ? triggers.data?.find((trigger) => trigger.id === chat.triggerId)?.daemonId
    : undefined;
  const daemonId = chat.activeDaemonId || triggerDaemonId;
  if (!daemonId) return null;

  // A builder test is attended, like a chat: a person pressed Run.
  const automation =
    !!chat.launchKind &&
    chat.launchKind !== "chat.start" &&
    chat.launchKind !== "agent.start_run" &&
    chat.launchKind !== "builder.test";
  return <MachineStatus daemonId={daemonId} automation={automation} />;
}
