import { Fragment, memo, useCallback, useEffect, useId, useMemo, useState } from "react";
import { AlertCircle, ChevronDown, ChevronRight } from "lucide-react";
import { cn } from "../../lib/utils";
import { useSurface } from "../../lib/surfaceContext";
import { getProcessedMessage } from "../../lib/messageProcessor";
import { useToolCallStates, useToolResultsByCallId } from "../../store/chatStoreHooks";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { findWorkflowById } from "../../lib/workflowTree";
import { workflowLifecycleToDisplayStatus } from "./ToolExecution";
import { ChatMessage, type ChatTimelineVariant } from "./ChatMessage";
import {
  formatTurnWorkHeadline,
  formatTurnWorkElapsed,
  formatTurnWorkDetails,
  summarizeTurnWork,
  type TurnWorkExecution,
} from "./turnWorkSummary";
import type { TurnWork } from "./thread-views/simplifiedTurns";
import type { Approval } from "../../gen/reliant/v1/approval_pb";
import { useMinimumDwell } from "./useMinimumDwell";
import { DeployCardShownOutside } from "../Apps/DeployedAppCard";
import { AgentMessageCard, isAgentMessage } from "./AgentMessageCard";
import { SystemNotificationMessage } from "./SystemNotificationMessage";

interface TurnWorkFoldProps {
  work: TurnWork;
  chatId?: string;
  approvals?: Approval[];
  /** The turn is still being worked on: the header shows live text and ticks. */
  isStreaming?: boolean;
  onSelectThread?: (threadId: string | null) => void;
  timelineVariant?: ChatTimelineVariant;
}

const NO_APPROVALS: Approval[] = [];

/** How long each step stays on the live line before the next replaces it. */
const STEP_MIN_DWELL_MS = 1200;

function parseTime(value: string | undefined): number | null {
  if (!value) return null;
  const ms = Date.parse(value);
  return Number.isNaN(ms) ? null : ms;
}

function TurnWorkFoldComponent({
  work,
  chatId,
  approvals = NO_APPROVALS,
  isStreaming = false,
  onSelectThread,
  timelineVariant,
}: TurnWorkFoldProps) {
  const surface = useSurface();
  const panelId = useId();
  const live = isStreaming;

  const toolResultsByCallId = useToolResultsByCallId(chatId || "");
  const toolCallStates = useToolCallStates(chatId || "");
  const { allWorkflows } = useWorkflowExecutions(chatId || null);

  const executions = useMemo((): TurnWorkExecution[] => {
    const approvalsByBlock = new Map<string, Approval>();
    for (const approval of approvals) {
      if (approval.entityId) approvalsByBlock.set(approval.entityId, approval);
    }
    const out: TurnWorkExecution[] = [];
    for (const part of work.parts) {
      const { segments } = getProcessedMessage(part.message, toolResultsByCallId, approvals);
      for (const index of part.segmentIndexes) {
        const segment = segments[index];
        if (segment?.kind !== "tools") continue;
        for (const execution of segment.executions) {
          const state = toolCallStates.get(execution.call.id);
          const childWorkflowId = execution.call.childWorkflowId ?? state?.childWorkflowId;
          out.push({
            call:
              childWorkflowId === execution.call.childWorkflowId
                ? execution.call
                : { ...execution.call, childWorkflowId },
            result: execution.result,
            approval:
              execution.approval ??
              (execution.call.content_block_id
                ? approvalsByBlock.get(execution.call.content_block_id)
                : undefined),
            status: state?.status,
          });
        }
      }
    }
    return out;
  }, [work.parts, toolResultsByCallId, toolCallStates, approvals]);

  const summary = useMemo(
    () =>
      summarizeTurnWork(executions, {
        spawnStatus: (call) => {
          if (!call.childWorkflowId) return undefined;
          const workflow = findWorkflowById(allWorkflows, call.childWorkflowId);
          return workflowLifecycleToDisplayStatus(workflow?.state, workflow?.stopReason);
        },
      }),
    [executions, allWorkflows],
  );

  // One tick per second, only while live: a finished turn never re-renders.
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!live) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [live]);

  const startMs = parseTime(work.parts[0]?.message.createdAt);
  const endMs = useMemo(() => {
    let latest: number | null = null;
    for (const { call } of executions) {
      const done = parseTime(call.durableCompletedAt);
      if (done !== null && (latest === null || done > latest)) latest = done;
    }
    for (const event of work.events ?? []) {
      const time = parseTime(event.message.createdAt);
      if (time !== null && (latest === null || time > latest)) latest = time;
    }
    if (latest !== null) return latest;
    return parseTime(work.parts[work.parts.length - 1]?.message.createdAt);
  }, [executions, work.parts, work.events]);

  const elapsedMs =
    startMs === null
      ? null
      : live
        ? Math.max(0, now - startMs)
        : endMs !== null && endMs >= startMs
          ? endMs - startMs
          : null;

  // Hold each step on the live line long enough to read: a fast agent moves
  // through several reads a second, which otherwise flashes past unreadable.
  const heldStep = useMinimumDwell(summary.current, STEP_MIN_DWELL_MS);
  const headline = formatTurnWorkHeadline(
    live ? { ...summary, current: heldStep } : summary,
    { live },
  );
  const elapsed = formatTurnWorkElapsed(elapsedMs);
  const details = formatTurnWorkDetails(summary);

  // Collapsed by default, even while live: the header carries the live state.
  const [expanded, setExpanded] = useState(false);
  const toggle = useCallback(() => setExpanded((open) => !open), []);

  const common = { chatId, approvals, isStreaming, onSelectThread, timelineVariant };
  const renderEvents = (afterPart: number) => (work.events ?? [])
    .filter((event) => event.afterPart === afterPart)
    .map(({ message }) => isAgentMessage(message) ? (
      <AgentMessageCard key={message.id} message={message} chatId={chatId} onSelectThread={onSelectThread} />
    ) : (
      <SystemNotificationMessage key={message.id} message={message} />
    ));

  return (
    <div className="my-1" data-testid="turn-work-fold" data-live={live ? "true" : "false"}>
      <button
        type="button"
        className={cn(
          "flex w-full items-center justify-between gap-2 rounded-md px-1.5 py-1 text-left text-xs text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:min-h-[44px]",
          surface !== "desktop" && "min-h-[44px]",
        )}
        onClick={toggle}
        aria-label={summary.failed > 0
          ? `${headline}, ${summary.failed} ${summary.failed === 1 ? "failed step" : "failed steps"}`
          : headline}
        aria-expanded={expanded}
        aria-controls={panelId}
      >
        <span className="flex min-w-0 items-center gap-1.5">
          {expanded ? (
            <ChevronDown className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          )}
          {live && (
            <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-primary motion-safe:animate-pulse" aria-hidden="true" />
          )}
          <span className="truncate font-medium">{headline}</span>
          {summary.failed > 0 && (
            <span className="inline-flex shrink-0 items-center gap-1">
              <AlertCircle className="h-3.5 w-3.5" aria-hidden="true" />
              {summary.failed} {summary.failed === 1 ? "failed step" : "failed steps"}
            </span>
          )}
        </span>
        {elapsed !== null && (
          <span
            role="timer"
            aria-live="off"
            aria-label="Activity elapsed time"
            className="ml-auto shrink-0 tabular-nums"
          >
            {elapsed}
          </span>
        )}
      </button>

      {expanded && (
        <div id={panelId} className="mt-1 space-y-1 pl-3" data-testid="turn-work-body">
          {details && <p className="px-1.5 py-1 text-xs text-muted-foreground">{details}</p>}
          {summary.failed > 0 && (
            <p className="px-1.5 py-1 text-xs text-muted-foreground">
              {summary.failed} {summary.failed === 1 ? "step failed" : "steps failed"}. Expand the steps below to inspect their errors.
            </p>
          )}
          {/* A deploy's card is the surfaced row's, on screen open or closed. */}
          <DeployCardShownOutside.Provider value={true}>
            {renderEvents(0)}
            {work.parts.map((part, index) => (
              <Fragment key={`${part.message.id}-work`}>
                <ChatMessage
                  message={part.message}
                  slice={{ segmentIndexes: part.segmentIndexes, role: "work" }}
                  {...common}
                />
                {renderEvents(index + 1)}
              </Fragment>
            ))}
          </DeployCardShownOutside.Provider>
        </div>
      )}

      {work.parts.map((part) => (
        <ChatMessage
          key={`${part.message.id}-surfaced`}
          message={part.message}
          slice={{ segmentIndexes: part.segmentIndexes, role: "work-surfaced" }}
          {...common}
        />
      ))}
    </div>
  );
}

export const TurnWorkFold = memo(TurnWorkFoldComponent);
