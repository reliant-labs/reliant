import { Fragment, memo, useCallback, useEffect, useMemo, useState } from "react";
import { ListTree, Loader2, XCircle } from "lucide-react";
import { useCapability, useSurface } from "../../lib/surfaceContext";
import { getProcessedMessage } from "../../lib/messageProcessor";
import { useChat, useToolCallStates, useToolResultsByCallId } from "../../store/chatStoreHooks";
import { viewerSafeMessage } from "../../lib/viewerResults";
import { useWorkflowExecutions } from "../../hooks/useWorkflowExecutions";
import { findWorkflowById } from "../../lib/workflowTree";
import { workflowLifecycleToDisplayStatus } from "./ToolExecution";
import { ChatMessage } from "./ChatMessage";
import {
  formatTurnWorkHeadline,
  formatTurnWorkElapsed,
  formatTurnWorkCounts,
  summarizeTurnWork,
  type TurnWorkExecution,
} from "./turnWorkSummary";
import { BranchFromMessageButton, MessageTimestamp } from "./MessageChrome";
import { QuietRow } from "./QuietRow";
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
  /** The most steps shown as rows of their own; more read as one summary row. */
  inlineStepsMax?: number;
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

/**
 * The most steps (tool calls and agent mail) a run shows as rows of their
 * own. A longer run reads as one summary row that opens onto them.
 */
export const INLINE_STEPS_MAX = 5;

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
  inlineStepsMax = INLINE_STEPS_MAX,
}: TurnWorkFoldProps) {
  const surface = useSurface();
  const canBranch = useCapability("chatBranching");
  const live = isStreaming;

  const toolResultsByCallId = useToolResultsByCallId(chatId || "");
  // A viewer of another member's shared chat never sees a tool's result
  // (viewerSafeMessage), however it reached this client.
  const isViewer = !!useChat(chatId || "")?.viewerAccess;
  const toolCallStates = useToolCallStates(chatId || "");
  const { allWorkflows } = useWorkflowExecutions(chatId || null);

  const executions = useMemo((): FoldExecution[] => {
    const approvalsByBlock = new Map<string, Approval>();
    for (const approval of approvals) {
      if (approval.entityId) approvalsByBlock.set(approval.entityId, approval);
    }
    const out: FoldExecution[] = [];
    for (const part of work.parts) {
      const { segments } = viewerSafeMessage(getProcessedMessage(part.message, toolResultsByCallId, approvals), isViewer);
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
  }, [work.parts, toolResultsByCallId, toolCallStates, approvals, isViewer]);

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

  // Branching from the run's last assistant message. Not on the phone, which
  // shows a message's chrome in its actions sheet and cannot branch.
  const showChrome = surface !== "mobile";
  const branchFrom = canBranch ? work.parts[work.parts.length - 1]?.message : undefined;

  // A short run is its steps, each a quiet row of its own, in place. A long
  // one is a single summary row ("Read 6 files · ran 4 commands") that opens
  // onto the same rows.
  const steps = executions.length + (work.events?.length ?? 0);
  const inline = steps <= inlineStepsMax;
  const endAt = !live && endMs !== null ? new Date(endMs).toISOString() : undefined;

  // Collapsed by default, even while live: the summary carries the live state.
  const [expanded, setExpanded] = useState(false);
  const toggle = useCallback(() => setExpanded((open) => !open), []);

  // Opening the summary reads the bodies its rows were sent without (a stub
  // read) in one batch, so the rows that render expanded do not each start a
  // request and the ones the user opens next are already here.
  const elidedIds = useMemo(() => elidedBlockIdsOf(executions), [executions]);
  const elidedKey = elidedIds.join(",");
  useEffect(() => {
    if (inline || !expanded || !chatId || elidedKey === "") return;
    prefetchContentBlockBodies(chatId, elidedKey.split(","));
  }, [inline, expanded, chatId, elidedKey]);

  const common = { chatId, approvals, isStreaming, onSelectThread };
  const renderEvents = (afterPart: number) => (work.events ?? [])
    .filter((event) => event.afterPart === afterPart)
    .map(({ message }) => isAgentMessage(message) ? (
      <AgentMessageCard key={message.id} message={message} chatId={chatId} onSelectThread={onSelectThread} />
    ) : (
      <SystemNotificationMessage key={message.id} message={message} />
    ));
  const rows = (
    <>
      {renderEvents(0)}
      {work.parts.map((part, index) => (
        <Fragment key={`${part.message.id}-work`}>
          <ChatMessage message={part.message} slice={workSlice(part)} {...common} />
          {renderEvents(index + 1)}
        </Fragment>
      ))}
    </>
  );
  const branchButton = showChrome && branchFrom ? (
    // Revealed with the run, like a message's actions; inert while hidden so
    // it never swallows a click it does not show.
    <BranchFromMessageButton
      chatId={chatId}
      messageId={branchFrom.id}
      className="pointer-events-none shrink-0 opacity-0 transition-opacity group-hover/fold:pointer-events-auto group-hover/fold:opacity-100 group-focus-within/fold:pointer-events-auto group-focus-within/fold:opacity-100"
    />
  ) : null;

  if (inline) {
    return (
      <div
        className="group/fold relative my-2 flex flex-col gap-0.5"
        data-testid="turn-work-fold"
        data-form="rows"
        data-live={live ? "true" : "false"}
      >
        {rows}
        {branchButton && <div className="absolute -right-7 top-0.5">{branchButton}</div>}
      </div>
    );
  }

  return (
    <div className="group/fold my-2" data-testid="turn-work-fold" data-form="summary" data-live={live ? "true" : "false"}>
      <QuietRow
        icon={
          live ? (
            <Loader2 className="size-3.5 animate-spin text-primary-ink" />
          ) : summary.failed > 0 ? (
            <XCircle className="size-4 text-destructive-ink" />
          ) : (
            <ListTree className="size-4" />
          )
        }
        expanded={expanded}
        onToggle={toggle}
        ariaLabel={[headline, counts, failedLabel].filter(Boolean).join(", ")}
        trailing={
          <>
            {elapsed !== null && (
              <span role="timer" aria-live="off" aria-label="Activity elapsed time" className="text-meta tabular-nums">
                {elapsed}
              </span>
            )}
            {showChrome && endAt && (
              <MessageTimestamp
                at={endAt}
                className="shrink-0 opacity-0 transition-opacity group-hover/fold:opacity-100 group-focus-within/fold:opacity-100"
              />
            )}
            {branchButton}
          </>
        }
        detailTestId="turn-work-body"
        detail={
          <div className="flex flex-col gap-0.5">
            {summary.failed > 0 && (
              <p className="pb-1 text-ui text-muted-foreground">
                {summary.failed} {summary.failed === 1 ? "step failed" : "steps failed"}. Expand the steps below to inspect their errors.
              </p>
            )}
            {rows}
          </div>
        }
      >
        {/* One truncating line: the headline wins, the counts give way. */}
        <span className="truncate" title={counts ? `${headline} · ${counts}` : undefined}>
          <span>{headline}</span>
          {counts && <span className="text-muted-foreground/70"> · {counts}</span>}
        </span>
        {failedLabel && <span className="shrink-0 text-destructive-ink">{failedLabel}</span>}
      </QuietRow>
      {/* Nothing of the run renders outside the body: what must stay on
          screen (a question, an approval, a generated image, a deploy, a
          sub-agent) is a timeline row of its own, at its place between runs. */}
    </div>
  );
}

export const TurnWorkFold = memo(TurnWorkFoldComponent);
