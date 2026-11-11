import { Fragment, memo, useCallback, useEffect, useId, useMemo, useState } from "react";
import { AlertCircle, ChevronDown, ChevronRight } from "lucide-react";
import { cn } from "../../lib/utils";
import { useCapability, useSurface } from "../../lib/surfaceContext";
import { getProcessedMessage } from "../../lib/messageProcessor";
import { useToolCallStates, useToolResultsByCallId } from "../../store/chatStoreHooks";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { findWorkflowById } from "../../lib/workflowTree";
import { workflowLifecycleToDisplayStatus } from "./ToolExecution";
import { ChatMessage, type ChatTimelineVariant } from "./ChatMessage";
import {
  formatTurnWorkHeadline,
  formatTurnWorkElapsed,
  formatTurnWorkCounts,
  summarizeTurnWork,
  type TurnWorkExecution,
} from "./turnWorkSummary";
import { BranchFromMessageButton, MessageTimestamp } from "./MessageChrome";
import type { MessageSlice, TurnWork } from "./thread-views/simplifiedTurns";
import type { Approval } from "../../gen/reliant/v1/approval_pb";
import { useMinimumDwell } from "./useMinimumDwell";
import { elidedBlockIdsOf, prefetchContentBlockBodies } from "../../hooks/content-block-bodies";
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

/** A fold's execution: what the summary reads, plus the bodies its rows may fetch. */
type FoldExecution = TurnWorkExecution & {
  call: TurnWorkExecution["call"] & { inputElided?: boolean; content_block_id?: string };
  result?: TurnWorkExecution["result"] & { elided?: boolean; block_id?: string };
};

const workSlice = (part: TurnWork["parts"][number]): MessageSlice =>
  part.callIds
    ? { segmentIndexes: part.segmentIndexes, role: "work", callIds: part.callIds }
    : { segmentIndexes: part.segmentIndexes, role: "work" };

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
  const canBranch = useCapability("chatBranching");
  const panelId = useId();
  const live = isStreaming;

  const toolResultsByCallId = useToolResultsByCallId(chatId || "");
  const toolCallStates = useToolCallStates(chatId || "");
  const { allWorkflows } = useWorkflowExecutions(chatId || null);

  const executions = useMemo((): FoldExecution[] => {
    const approvalsByBlock = new Map<string, Approval>();
    for (const approval of approvals) {
      if (approval.entityId) approvalsByBlock.set(approval.entityId, approval);
    }
    const out: FoldExecution[] = [];
    for (const part of work.parts) {
      const { segments } = getProcessedMessage(part.message, toolResultsByCallId, approvals);
      const held = part.callIds ? new Set(part.callIds) : null;
      for (const index of part.segmentIndexes) {
        const segment = segments[index];
        if (segment?.kind !== "tools") continue;
        for (const execution of segment.executions) {
          if (held && !held.has(execution.call.id)) continue;
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

  // A run can open on mail as well as on a tool call: it starts at whichever
  // came first.
  const startMs = useMemo(() => {
    const times = [work.parts[0]?.message.createdAt, work.events?.[0]?.message.createdAt]
      .map(parseTime)
      .filter((time): time is number => time !== null);
    return times.length ? Math.min(...times) : null;
  }, [work.parts, work.events]);
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
  const counts = formatTurnWorkCounts(summary, { messages: work.events?.length ?? 0 });
  const failedLabel = summary.failed > 0
    ? `${summary.failed} ${summary.failed === 1 ? "failed step" : "failed steps"}`
    : "";

  // A row's chrome, like a message's: when the run ended, and branching from
  // its last assistant message. Not on the phone, which shows a message's
  // chrome in its actions sheet and cannot branch.
  const showChrome = surface !== "mobile";
  const endAt = !live && endMs !== null ? new Date(endMs).toISOString() : undefined;
  const branchFrom = canBranch ? work.parts[work.parts.length - 1]?.message : undefined;

  // Collapsed by default, even while live: the header carries the live state.
  const [expanded, setExpanded] = useState(false);
  const toggle = useCallback(() => setExpanded((open) => !open), []);

  // Opening the fold reads the bodies its rows were sent without (a stub
  // read) in one batch, so the rows that render expanded do not each start a
  // request and the ones the user opens next are already here.
  const elidedIds = useMemo(() => elidedBlockIdsOf(executions), [executions]);
  const elidedKey = elidedIds.join(",");
  useEffect(() => {
    if (!expanded || !chatId || elidedKey === "") return;
    prefetchContentBlockBodies(chatId, elidedKey.split(","));
  }, [expanded, chatId, elidedKey]);

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
      <div className="group/fold-header flex items-center gap-1">
        <button
          type="button"
          className={cn(
            "flex min-w-0 flex-1 items-center justify-between gap-2 rounded-md px-1.5 py-1 text-left text-xs text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:min-h-[44px]",
            surface !== "desktop" && "min-h-[44px]",
          )}
          onClick={toggle}
          aria-label={[headline, counts, failedLabel].filter(Boolean).join(", ")}
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
            {/* One truncating line: the headline wins, the counts give way. */}
            <span className="truncate" title={counts ? `${headline} · ${counts}` : undefined}>
              <span className="font-medium">{headline}</span>
              {counts && <span className="text-muted-foreground/70"> · {counts}</span>}
            </span>
            {failedLabel && (
              <span className="inline-flex shrink-0 items-center gap-1">
                <AlertCircle className="h-3.5 w-3.5" aria-hidden="true" />
                {failedLabel}
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
        {showChrome && endAt && <MessageTimestamp at={endAt} className="shrink-0 px-1" />}
        {showChrome && branchFrom && (
          // Revealed with the header, like a message's hover toolbar; inert
          // while hidden so it never swallows a click it does not show.
          <BranchFromMessageButton
            chatId={chatId}
            messageId={branchFrom.id}
            className="pointer-events-none shrink-0 opacity-0 transition-opacity group-hover/fold-header:pointer-events-auto group-hover/fold-header:opacity-100 group-focus-within/fold-header:pointer-events-auto group-focus-within/fold-header:opacity-100"
          />
        )}
      </div>

      {expanded && (
        <div id={panelId} className="mt-1 space-y-1 pl-3" data-testid="turn-work-body">
          {summary.failed > 0 && (
            <p className="px-1.5 py-1 text-xs text-muted-foreground">
              {summary.failed} {summary.failed === 1 ? "step failed" : "steps failed"}. Expand the steps below to inspect their errors.
            </p>
          )}
          {renderEvents(0)}
          {work.parts.map((part, index) => (
            <Fragment key={`${part.message.id}-work`}>
              <ChatMessage message={part.message} slice={workSlice(part)} {...common} />
              {renderEvents(index + 1)}
            </Fragment>
          ))}
        </div>
      )}
      {/* Nothing of the run renders outside the body: what must stay on
          screen (a question, an approval, a generated image, a deploy) is a
          timeline row of its own, at its place between folds. */}
    </div>
  );
}

export const TurnWorkFold = memo(TurnWorkFoldComponent);
