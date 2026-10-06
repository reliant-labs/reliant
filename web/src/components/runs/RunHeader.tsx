// Copyright (c) 2025 Reliant Labs

/**
 * The run detail header (WORKFLOW_UI.md §4.2, item 1): title, status, who
 * started the run, where it runs, how long it has taken, and the actions its
 * state allows.
 *
 * Status comes from lib/runStatus over the chat's lifecycle pair and activity
 * (the same inputs every other surface uses), and the "started by" line from
 * the launch-kind vocabulary in the same module.
 *
 * Once a run has stopped it offers "Re-run (current definition)" (§14.1
 * decision 3: there is no definition snapshot, so the label says which
 * definition it uses). A failed run's Retry is the same action, not a second
 * path. A run an automation fired also offers "Run automation now", which fires the
 * automation itself and stays unattended under its policy.
 */

import { Link } from "@tanstack/react-router";
import { CalendarClock, MessageSquarePlus, Pause, Play, RotateCcw, Square, Workflow } from "lucide-react";

import type { Chat } from "@/api/client";
import type { LaunchEvent } from "@/api/run-grpc";
import { useDaemonStatus } from "@/hooks/useDaemonStatus";
import { isLiveRunStatus, launchKindDisplay, runStatus, type LaunchKindDisplay } from "@/lib/runStatus";
import { formatAbsoluteTime } from "@/lib/relativeTime";
import { Button } from "../ui/Button";
import { RunStatusBadge } from "../ui/RunStatusIndicator";
import { daemonLabel } from "../Automations/daemonChoices";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";
import { launchContextOf } from "./launchContext";
import { RunDuration } from "./RunRow";

export interface RunHeaderActions {
  onPause: () => void;
  onResume: () => void;
  onStop: () => void;
  /** Adopt the run into the chat list and open it there. */
  onOpenAsChat: () => void;
  /** Show or hide the workflow diagram; omitted where there is no diagram. */
  onToggleDiagram?: () => void;
  /**
   * Start a new attended run with this run's recorded inputs. Omitted while
   * they are unknown (still loading, or a run from before launch events).
   */
  onRerun?: () => void;
  /** Fire this run's automation now; omitted unless it is a live automation's run. */
  onRunAutomationNow?: () => void;
}

interface RunHeaderProps {
  chat: Chat;
  /** The automation's current name, when an automation fired the run and it still exists. */
  triggerName?: string;
  /** The launch event, for what the "Started by" line can say about the source. */
  event?: LaunchEvent | null;
  /** The chat whose agent started this run, when known. */
  parent?: { chatId: string; title: string };
  projectName?: string;
  actions: RunHeaderActions;
  /** An action is in flight; buttons disable rather than double-fire. */
  busy?: boolean;
}

export function RunHeader({ chat, triggerName, event, parent, projectName, actions, busy }: RunHeaderProps) {
  const status = runStatus({
    state: chat.workflowState,
    stopReason: chat.workflowStopReason,
    activity: chat.activity,
  });
  const live = isLiveRunStatus(status);
  const launch = launchKindDisplay(chat.launchKind, { ...launchContextOf(event), triggerName });
  const { daemons } = useDaemonStatus();
  const machine = chat.activeDaemonId
    ? daemonLabel(
        daemons.find((d) => d.daemonId === chat.activeDaemonId),
        chat.activeDaemonId,
      )
    : undefined;
  const startedAt = Date.parse(chat.createdAt);
  // Interactive chats and adopted runs are already in the chat list.
  const isChat = launch.kind === "chat.start" || Boolean(chat.adoptedAt);
  const location = [projectName, machine].filter(Boolean).join(" · ");

  return (
    <header className="space-y-2">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2.5">
          <h1 className="truncate text-xl font-semibold tracking-tight text-foreground">
            {chat.title || "Untitled run"}
          </h1>
          <span className="shrink-0">
            <RunStatusBadge status={status} />
          </span>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {status.key === "paused" ? (
            <Button
              size="sm"
              variant="primary"
              leftIcon={<Play className="h-4 w-4" />}
              onClick={actions.onResume}
              disabled={busy}
            >
              Resume
            </Button>
          ) : live ? (
            <Button
              size="sm"
              variant="outline"
              leftIcon={<Pause className="h-4 w-4" />}
              onClick={actions.onPause}
              disabled={busy}
            >
              Pause
            </Button>
          ) : null}
          {live && (
            <Button
              size="sm"
              variant="outline"
              leftIcon={<Square className="h-4 w-4" />}
              onClick={actions.onStop}
              disabled={busy}
            >
              Stop
            </Button>
          )}
          {!live && actions.onRerun && (
            <Button
              size="sm"
              variant={status.key === "failed" ? "primary" : "outline"}
              leftIcon={<RotateCcw className="h-4 w-4" />}
              onClick={actions.onRerun}
              disabled={busy}
            >
              Re-run (current definition)
            </Button>
          )}
          {actions.onRunAutomationNow && (
            <Button
              size="sm"
              variant="ghost"
              leftIcon={<CalendarClock className="h-4 w-4" />}
              onClick={actions.onRunAutomationNow}
              disabled={busy}
            >
              Run automation now
            </Button>
          )}
          {actions.onToggleDiagram && (
            <Button
              size="sm"
              variant="ghost"
              leftIcon={<Workflow className="h-4 w-4" />}
              onClick={actions.onToggleDiagram}
            >
              Diagram
            </Button>
          )}
          <Button
            size="sm"
            variant="ghost"
            leftIcon={<MessageSquarePlus className="h-4 w-4" />}
            onClick={actions.onOpenAsChat}
            disabled={busy}
          >
            {isChat ? "Open chat" : "Open as chat"}
          </Button>
        </div>
      </div>

      <p className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-sm text-muted-foreground">
        <span data-testid="run-started-by">
          <StartedBy chat={chat} triggerName={triggerName} parent={parent} launch={launch} />
        </span>
        {chat.workflowName && (
          <>
            <span aria-hidden="true">·</span>
            <span>{getWorkflowDisplayName(chat.workflowName, true)}</span>
          </>
        )}
        {location && (
          <>
            <span aria-hidden="true">·</span>
            <span>{location}</span>
          </>
        )}
        {!Number.isNaN(startedAt) && (
          <>
            <span aria-hidden="true">·</span>
            <span>
              Started{" "}
              <time dateTime={chat.createdAt}>{formatAbsoluteTime(chat.createdAt)}</time>
              {" · "}
              <span className="tabular-nums">
                <RunDuration startedAt={startedAt} live={live} />
              </span>
            </span>
          </>
        )}
      </p>
    </header>
  );
}

/**
 * The launch line, with what started the run linked when it can be: the
 * automation that fired it, whatever kind of automation, or the agent's chat.
 */
function StartedBy({
  chat,
  triggerName,
  parent,
  launch,
}: {
  chat: Chat;
  triggerName?: string;
  parent?: { chatId: string; title: string };
  launch: LaunchKindDisplay;
}) {
  const kind = launch.kind;
  const parts = launch.automationParts;
  if (parts && chat.triggerId) {
    return (
      <>
        {parts.lead}
        <Link
          to="/workflows/automations/$triggerId"
          params={{ triggerId: chat.triggerId }}
          className="font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {parts.automation}
        </Link>
        {parts.trail}
      </>
    );
  }
  if (kind === "schedule" && !triggerName) return <>Started by a schedule that has since been deleted</>;
  if (kind === "agent.start_run" && parent) {
    return (
      <>
        Started by an agent in{" "}
        <Link
          to="/workflows/runs/$runId"
          params={{ runId: parent.chatId }}
          className="font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {parent.title || "another run"}
        </Link>
      </>
    );
  }
  return <>{launch.startedByLine}</>;
}
