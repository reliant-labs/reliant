/**
/**
 * InterleavedTimeline - Single chronological timeline with workflow context
 *
 * Shows all messages in chronological order with workflow context via colored
 * borders. Fork/handoff points shown as dividers.
 *
 * Key design:
 * - WorkflowExecution is source of truth for structure (forks, names, status)
 * - Messages provide content and timeline order (sorted by timestamp)
 * - workflow_id on messages links to WorkflowExecution (must exist - no fallbacks)
 * - Handoffs detected from workflow_id changes in message stream
 */

import React, { useMemo, useCallback, useRef, useState, useEffect, memo } from "react";
import { MessageRole } from "../../../gen/reliant/v1/chat_pb";
import { Virtuoso, type VirtuosoHandle, type ListRange } from "react-virtuoso";
import { GitBranch, ArrowRightLeft, Plus, ArrowUp, Route } from "lucide-react";
import { Tooltip } from "../../ui/Tooltip";
import { ChatMessage } from "../ChatMessage";
import { CompactionMessage, isCompactionMessage } from "../CompactionMessage";
import { WorkflowErrorMessage } from "../WorkflowErrorMessage";
import { WorkflowInfoMessage } from "../WorkflowInfoMessage";
import { SkillInvocationMessage } from "../SkillInvocationMessage";
import { SystemNotificationMessage } from "../SystemNotificationMessage";
import { RunStepExecution } from "../RunStepExecution";
import type { Message, ToolApprovalRequest } from "../../../api/client";
import type {
  ErrorUpdate,
  InfoUpdate,
  RunOutputUpdate,
  SkillInvocationUpdate,
} from "../../../types/streaming";
import type { WorkflowExecution, StepExecution } from "../ExecutionSidebar/types";
import { cn } from "../../../lib/utils";
import { getActivitySteps } from "./activityIndicators";
import { ActivityIndicator } from "./ActivityIndicator";
import { getThreadColor, formatNodeId, resolveThreadNameFromActiveThreads, resolveRouterDecisionFromActiveThreads } from "./threadUtils";
import { useChatStore } from "../../../store/chatStore";
import { useActiveThreads } from "../../../store/threadActivityStore";

interface InterleavedTimelineProps {
  messages: Message[];
  approvals?: ToolApprovalRequest[];
  errorEvents?: ErrorUpdate[];
  infoEvents?: InfoUpdate[];
  skillInvocations?: SkillInvocationUpdate[];
  runOutputs?: RunOutputUpdate[];
  chatId: string;
  workflowExecution?: WorkflowExecution;
  /** Selected thread IDs to display. If null/undefined, shows all threads. */
  selectedThreads?: Set<string> | null;
  /** Whether the chat is currently streaming (used to hide branch icon on latest message) */
  isStreaming?: boolean;
  /** Ref to Virtuoso handle for external scroll control */
  virtuosoRef?: React.RefObject<VirtuosoHandle | null>;
  /** Callback when Virtuoso's at-bottom state changes */
  onAtBottomStateChange?: (atBottom: boolean) => void;
  /** Callback when Virtuoso detects scrolling state changes */
  onIsScrolling?: (isScrolling: boolean) => void;
  /** Footer element rendered at the bottom of the virtualized list (e.g. thinking indicator) */
  footer?: React.ReactNode;
}

/** Minimal info needed for rendering - derived from WorkflowExecution */
/** Thread creation mechanism */
type ThreadOrigin = "fork" | "new" | "main";

interface RouterDecision {
  workflow: string;
  preset: string;
}

interface WorkflowDisplay {
  id: string;
  thread: string;
  name: string;
  color: string;
  parentThread?: string;
  isMain: boolean;
  /** How this thread was created: fork (inherits context), new (fresh), or main */
  origin: ThreadOrigin;
  /** Routing decision metadata, set when thread was created by a router node */
  routerDecision?: RouterDecision;
}

type TimelineItem =
  | { type: "message"; message: Message; workflow: WorkflowDisplay }
  | { type: "thread-start"; workflow: WorkflowDisplay; parentName: string }
  | { type: "handoff"; toName: string; color: string }
  | { type: "activity"; step: StepExecution; workflow: WorkflowDisplay; workflowName: string }
  | { type: "error"; error: ErrorUpdate }
  | { type: "info"; info: InfoUpdate }
  | { type: "skill_invocation"; invocation: SkillInvocationUpdate }
  | { type: "run_output"; runOutput: RunOutputUpdate };

/**
 * Build lookup maps from WorkflowExecution tree.
 * Returns lookup by workflow ID and by thread.
 */
function buildWorkflowLookups(
  root: WorkflowExecution | undefined,
  chatId: string
): {
  byId: Map<string, WorkflowExecution>;
  byThread: Map<string, WorkflowExecution>;
  displays: Map<string, WorkflowDisplay>;
} {
  const byId = new Map<string, WorkflowExecution>();
  const byThread = new Map<string, WorkflowExecution>();
  const displays = new Map<string, WorkflowDisplay>();

  function index(wf: WorkflowExecution, treeParentThread?: string) {
    byId.set(wf.id, wf);
    // Prefer workflows with spawnedByNodeId for thread lookup
    const existing = byThread.get(wf.thread);
    if (!existing || wf.spawnedByNodeId) {
      byThread.set(wf.thread, wf);
    }

    const isMain = wf.thread === chatId || wf.thread === "0";
    let name = "Main";
    if (!isMain) {
      if (wf.threadTitle) {
        name = formatNodeId(wf.threadTitle);
      } else if (wf.spawnedByNodeId) {
        name = formatNodeId(wf.spawnedByNodeId);
      } else {
        name = "Thread";
      }
    }

    // Determine thread origin: fork (has forkedFromThread), new (child but not fork), or main
    let origin: ThreadOrigin = "main";
    if (!isMain) {
      origin = wf.forkedFromThread ? "fork" : "new";
    }

    // Use authoritative parentThread from backend (thread table), fall back to tree-derived
    const parentThread = wf.parentThread || treeParentThread;

    displays.set(wf.thread, {
      id: wf.id,
      thread: wf.thread,
      name,
      color: getThreadColor(wf.thread, isMain),
      parentThread,
      isMain,
      origin,
    });

    for (const child of wf.children) {
      index(child, wf.thread);
    }
  }

  if (root) {
    index(root);
  }

  return { byId, byThread, displays };
}

/**
 * Get workflow display name from workflow_id
 */
function getWorkflowName(wf: WorkflowExecution | undefined): string {
  if (!wf) return "Agent";
  if (wf.threadTitle) return formatNodeId(wf.threadTitle);
  if (wf.spawnedByNodeId) return formatNodeId(wf.spawnedByNodeId);
  return "Agent";
}

/** Transition divider - used for thread starts, handoffs, and routing decisions */
interface TransitionDividerProps {
  icon: "fork" | "new" | "handoff";
  label: string;
  name: string;
  color: string;
  /** Routing decision details, shown before the thread mode icon */
  routingInfo?: { workflow: string; preset: string };
}

const TransitionDivider = memo(function TransitionDivider({
  icon,
  label,
  name,
  color,
  routingInfo,
}: TransitionDividerProps) {
  const Icon = icon === "fork" ? GitBranch : icon === "new" ? Plus : ArrowRightLeft;

  return (
    <div className="flex items-center gap-3 py-2 px-4">
      <div className="flex-1 h-px bg-border" />
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        {routingInfo ? (
          <>
            <Route className="h-3.5 w-3.5" style={{ color }} />
            <span className="text-muted-foreground">Routed to</span>
            <span
              className="font-medium px-1.5 py-0.5 rounded"
              style={{
                color,
                backgroundColor: `${color}15`,
              }}
            >
              {routingInfo.workflow}
            </span>
            {routingInfo.preset && (
              <span
                className="font-medium px-1.5 py-0.5 rounded"
                style={{
                  color: `${color}cc`,
                  backgroundColor: `${color}10`,
                }}
              >
                {routingInfo.preset}
              </span>
            )}
            <Icon className="h-3 w-3 opacity-60" />
            <span>{label}</span>
          </>
        ) : (
          <>
            <Icon className="h-3.5 w-3.5" style={{ color }} />
            <span
              className="font-medium px-1.5 py-0.5 rounded"
              style={{
                color,
                backgroundColor: `${color}15`,
              }}
            >
              {name}
            </span>
            <span>{label}</span>
          </>
        )}
      </div>
      <div className="flex-1 h-px bg-border" />
    </div>
  );
});

export const InterleavedTimeline = memo(function InterleavedTimeline({
  messages,
  approvals = [],
  errorEvents = [],
  infoEvents = [],
  skillInvocations = [],
  runOutputs = [],
  chatId,
  workflowExecution,
  selectedThreads,
  isStreaming = false,
  virtuosoRef,
  onAtBottomStateChange,
  onIsScrolling,
  footer,
}: InterleavedTimelineProps) {
  const activeThreads = useActiveThreads(chatId);
  const timelineItems = useMemo(() => {
    // Build workflow lookups from execution tree
    const { byId, displays } = buildWorkflowLookups(workflowExecution, chatId);

    // Augment displays with router decision data from streaming updates
    for (const at of activeThreads) {
      if (at.router_decision) {
        const existing = displays.get(at.thread);
        if (existing && !existing.routerDecision) {
          existing.routerDecision = at.router_decision;
        }
      }
      // Also augment thread title if display fell back to "Thread"
      if (at.thread_title) {
        const existing = displays.get(at.thread);
        if (existing && existing.name === "Thread") {
          existing.name = formatNodeId(at.thread_title);
        }
      }
    }

    // Create default display for main thread if no workflow execution yet
    if (!displays.has(chatId)) {
      displays.set(chatId, {
        id: chatId,
        thread: chatId,
        name: "Main",
        color: getThreadColor(chatId, true),
        isMain: true,
        origin: "main",
      });
    }

    // Thread visibility check
    const showAll = !selectedThreads || selectedThreads.size === 0;
    const isVisible = (thread: string | undefined) => {
      // Treat undefined/empty thread as main thread
      const effectiveThread = thread || chatId;
      if (showAll) return true;
      if (selectedThreads?.has(effectiveThread)) return true;
      const isMain = effectiveThread === chatId || effectiveThread === "0" || effectiveThread === "";
      const selectedMain = selectedThreads?.has(chatId) || selectedThreads?.has("0") || selectedThreads?.has("");
      return isMain && selectedMain;
    };

    const items: TimelineItem[] = [];
    const seenThreads = new Set<string>();
    const lastWorkflowByThread = new Map<string, string>();
    const seenAssistantOnThread = new Set<string>();

    // Sort messages by timestamp (ordinal is per-thread, not global)
    const sorted = [...messages].sort(
      (a, b) => new Date(a.createdAt || "").getTime() - new Date(b.createdAt || "").getTime()
    );

    for (const msg of sorted) {
      // Thread defaults to chatId (main thread) if not set
      const thread = msg.thread || chatId;
      if (!isVisible(thread)) continue;

      // Get workflow display info for this thread
      let display = displays.get(thread);
      if (!display) {
        // Thread exists but wasn't in workflow tree - create minimal display
        // Resolve name from activeThreads streaming data (has thread_title/spawned_by_node_id)
        const isMain = thread === chatId || thread === "0";
        const resolvedName = !isMain ? resolveThreadNameFromActiveThreads(thread, activeThreads) : undefined;
        const routerDec = !isMain ? resolveRouterDecisionFromActiveThreads(thread, activeThreads) : undefined;
        display = {
          id: thread,
          thread,
          name: isMain ? "Main" : (resolvedName || "Thread"),
          color: getThreadColor(thread, isMain),
          isMain,
          origin: isMain ? "main" : "new",
          routerDecision: routerDec,
        };
        displays.set(thread, display);
      }

      // Thread start: first time seeing a non-main thread
      let justStarted = false;
      if (!display.isMain && !seenThreads.has(thread)) {
        seenThreads.add(thread);
        justStarted = true;

        const parentDisplay = display.parentThread
          ? displays.get(display.parentThread)
          : displays.get(chatId);

        items.push({
          type: "thread-start",
          workflow: display,
          parentName: parentDisplay?.name || "Main",
        });
      }

      // Handoff: workflow_id changed on same thread
      const currentWorkflowId = msg.workflowId;
      const lastWorkflowId = lastWorkflowByThread.get(thread);
      const hasSeenAssistant = seenAssistantOnThread.has(thread);

      // Show handoff if:
      // - workflow changed
      // - not just forked (fork already shows the workflow)
      // - for non-main: only after we've seen assistant responses (skip setup user messages)
      const isHandoff =
        currentWorkflowId &&
        lastWorkflowId &&
        currentWorkflowId !== lastWorkflowId &&
        !justStarted &&
        (display.isMain || hasSeenAssistant);

      if (isHandoff) {
        const newWorkflow = byId.get(currentWorkflowId);
        items.push({
          type: "handoff",
          toName: getWorkflowName(newWorkflow),
          color: display.color,
        });
      }

      // Track state
      if (currentWorkflowId) {
        lastWorkflowByThread.set(thread, currentWorkflowId);
      }
      if (msg.role === MessageRole.ASSISTANT) {
        seenAssistantOnThread.add(thread);
      }

      // Skip assistant messages with no content — they render as null in ChatMessage
      // and cause Virtuoso's "Zero-sized element" warning. Compaction and display_style
      // messages have their own renderers and are always visible.
      if (
        msg.role === MessageRole.ASSISTANT &&
        !msg.contentBlocks?.length &&
        !msg.displayStyle &&
        (!msg.attachments || msg.attachments.length === 0)
      ) {
        continue;
      }

      // Add message
      items.push({
        type: "message",
        message: msg,
        workflow: display,
      });
    }

    // Insert activities at correct positions
    if (workflowExecution) {
      const activities = getActivitySteps(workflowExecution);

      // Find first user message time to skip setup activities
      const firstUserItem = items.find(
        (i) => i.type === "message" && i.message.role === MessageRole.USER
      );
      const firstUserTime =
        firstUserItem?.type === "message"
          ? new Date(firstUserItem.message.createdAt || "").getTime()
          : 0;

      // Insert each activity at the right position
      for (const activity of activities) {
        if (!isVisible(activity.thread)) continue;
        if (activity.step.createdAt < firstUserTime) continue;

        const display = displays.get(activity.thread);
        if (!display) continue;

        // Find insertion point: after last message with timestamp <= activity time
        let insertIdx = items.length;
        for (let i = items.length - 1; i >= 0; i--) {
          const item = items[i];
          if (item.type === "message") {
            const msgTime = new Date(item.message.createdAt || "").getTime();
            if (msgTime <= activity.step.createdAt) {
              insertIdx = i + 1;
              break;
            }
          }
          if (i === 0) insertIdx = 0;
        }

        items.splice(insertIdx, 0, {
          type: "activity",
          step: activity.step,
          workflow: display,
          workflowName: activity.workflowName,
        });
      }
    }

    // Insert error events at correct positions based on timestamp
    for (const error of errorEvents) {
      const errorTime = new Date(error.timestamp).getTime();

      // Find insertion point: after last item with timestamp <= error time
      let insertIdx = items.length;
      for (let i = items.length - 1; i >= 0; i--) {
        const item = items[i];
        if (item.type === "message") {
          const msgTime = new Date(item.message.createdAt || "").getTime();
          if (msgTime <= errorTime) {
            insertIdx = i + 1;
            break;
          }
        }
        if (i === 0) insertIdx = 0;
      }

      items.splice(insertIdx, 0, {
        type: "error",
        error,
      });
    }

    // Insert info events at correct positions based on timestamp
    for (const info of infoEvents) {
      const infoTime = new Date(info.timestamp).getTime();

      // Find insertion point: after last item with timestamp <= info time
      let insertIdx = items.length;
      for (let i = items.length - 1; i >= 0; i--) {
        const item = items[i];
        if (item.type === "message") {
          const msgTime = new Date(item.message.createdAt || "").getTime();
          if (msgTime <= infoTime) {
            insertIdx = i + 1;
            break;
          }
        }
        if (i === 0) insertIdx = 0;
      }

      items.splice(insertIdx, 0, {
        type: "info",
        info,
      });
    }

    // Insert skill invocation events at correct positions based on timestamp
    for (const invocation of skillInvocations) {
      const invocationTime = new Date(invocation.timestamp).getTime();

      // Find insertion point: after last item with timestamp <= invocation time
      let insertIdx = items.length;
      for (let i = items.length - 1; i >= 0; i--) {
        const item = items[i];
        if (item.type === "message") {
          const msgTime = new Date(item.message.createdAt || "").getTime();
          if (msgTime <= invocationTime) {
            insertIdx = i + 1;
            break;
          }
        }
        if (i === 0) insertIdx = 0;
      }

      items.splice(insertIdx, 0, {
        type: "skill_invocation",
        invocation,
      });
    }

    // Insert run outputs at correct positions based on timestamp
    for (const runOutput of runOutputs) {
      const runOutputTime = new Date(runOutput.timestamp).getTime();

      // Find insertion point: after last item with timestamp <= runOutput time
      let insertIdx = items.length;
      for (let i = items.length - 1; i >= 0; i--) {
        const item = items[i];
        if (item.type === "message") {
          const msgTime = new Date(item.message.createdAt || "").getTime();
          if (msgTime <= runOutputTime) {
            insertIdx = i + 1;
            break;
          }
        }
        if (i === 0) insertIdx = 0;
      }

      items.splice(insertIdx, 0, {
        type: "run_output",
        runOutput,
      });
    }

    return items;
  }, [messages, chatId, workflowExecution, selectedThreads, errorEvents, infoEvents, skillInvocations, runOutputs, activeThreads]);

  // Flatten timelineItems into a renderable list with stable keys for Virtuoso
  const flatItems = useMemo(() => {
    return timelineItems.map((item, idx) => {
      let key: string;
      switch (item.type) {
        case "message":
          key = item.message.id;
          break;
        case "thread-start":
          key = `thread-${item.workflow.thread}`;
          break;
        case "handoff":
          key = `handoff-${idx}`;
          break;
        case "activity":
          key = `activity-${item.step.id}`;
          break;
        case "error":
          key = `error-${item.error.id}`;
          break;
        case "info":
          key = `info-${item.info.id}`;
          break;
        case "skill_invocation":
          key = `skill-invocation-${item.invocation.id}`;
          break;
        case "run_output":
          key = `run-${item.runOutput.id}`;
          break;
      }
      return { ...item, key, isLast: idx === timelineItems.length - 1 };
    });
  }, [timelineItems]);

  // Build user-message layer index: for each flatItem index, which user message index is its "layer header"
  const userMessageForItem = useMemo(() => {
    const mapping: (number | null)[] = [];
    let currentUserIdx: number | null = null;

    for (let i = 0; i < flatItems.length; i++) {
      const item = flatItems[i];
      if (item.type === "message" && item.message.role === MessageRole.USER) {
        currentUserIdx = i;
      }
      mapping.push(currentUserIdx);
    }
    return mapping;
  }, [flatItems]);

  // Track visible range for pinned user message
  const [pinnedUserMessageIdx, setPinnedUserMessageIdx] = useState<number | null>(null);
  const [isHoveringPinned, setIsHoveringPinned] = useState(false);

  // --- Per-thread scroll position memory ---
  // Derive a stable key for the current thread filter
  const threadKey = useMemo(() => {
    if (!selectedThreads || selectedThreads.size === 0) return "__all__";
    return Array.from(selectedThreads).sort().join(",");
  }, [selectedThreads]);

  // Store scroll positions per thread: { startIndex, atBottom }
  const scrollPositions = useRef<Map<string, { startIndex: number; atBottom: boolean }>>(new Map());
  const prevThreadKey = useRef<string>(threadKey);
  const lastRangeRef = useRef<ListRange | null>(null);

  // Save current scroll position for the active thread whenever range changes.
  // Use a ref to track the computed pinned index and only call setState when
  // the value actually changes to avoid unnecessary re-renders during scroll
  // that can trigger Virtuoso layout recalculations and cause jitter.
  const pinnedUserMessageIdxRef = useRef<number | null>(null);
  const handleRangeChanged = useCallback((range: ListRange) => {
    lastRangeRef.current = range;
    const firstVisible = range.startIndex;

    // Compute pinned user message
    const layerUserIdx = userMessageForItem[firstVisible] ?? null;
    const nextPinned = (layerUserIdx !== null && layerUserIdx < firstVisible) ? layerUserIdx : null;

    // Only trigger a React re-render when the pinned index actually changes
    if (nextPinned !== pinnedUserMessageIdxRef.current) {
      pinnedUserMessageIdxRef.current = nextPinned;
      setPinnedUserMessageIdx(nextPinned);
    }
  }, [userMessageForItem]);

  // Persist scroll position for current thread on every range/atBottom change
  useEffect(() => {
    const startIndex = lastRangeRef.current?.startIndex ?? 0;
    scrollPositions.current.set(threadKey, {
      startIndex,
      atBottom: atBottomRef.current,
    });
  });

  // On thread switch: restore saved position or scroll to bottom (instant)
  useEffect(() => {
    if (prevThreadKey.current === threadKey) return;
    prevThreadKey.current = threadKey;

    // Use requestAnimationFrame to let Virtuoso re-render with new data first
    const rafId = requestAnimationFrame(() => {
      const saved = scrollPositions.current.get(threadKey);
      if (saved && !saved.atBottom) {
        // Restore their previous position in this thread
        virtuosoRef?.current?.scrollToIndex({
          index: saved.startIndex,
          behavior: "auto",
          align: "start",
        });
      } else {
        // Default: scroll to bottom instantly (new thread or was at bottom)
        virtuosoRef?.current?.scrollToIndex({
          index: "LAST",
          behavior: "auto",
        });
      }
    });
    return () => cancelAnimationFrame(rafId);
  }, [threadKey, virtuosoRef]);

  const handleJumpToPinned = useCallback(() => {
    if (pinnedUserMessageIdx !== null && virtuosoRef?.current) {
      virtuosoRef.current.scrollToIndex({
        index: pinnedUserMessageIdx,
        behavior: "auto",
        align: "start",
      });
    }
  }, [pinnedUserMessageIdx, virtuosoRef]);

  // Force-yield: resolve spawn toolCallId → threadId, then call store method
  const forceYieldThread = useChatStore((s) => s.forceYieldThread);
  const handleForceYield = useCallback((toolCallId: string) => {
    if (!workflowExecution || !chatId) return;
    // Walk the workflow tree to find the child spawned by this tool call.
    // Backend creates spawn nodes as "spawn-" + toolCallID.
    const spawnNodeId = `spawn-${toolCallId}`;
    function findThread(wf: WorkflowExecution): string | undefined {
      if (wf.spawnedByNodeId === spawnNodeId) return wf.thread;
      for (const child of wf.children) {
        const found = findThread(child);
        if (found) return found;
      }
      return undefined;
    }
    const threadId = findThread(workflowExecution);
    if (threadId) {
      forceYieldThread(chatId, threadId);
    }
  }, [workflowExecution, chatId, forceYieldThread]);

  // Get the pinned user message data
  const pinnedMessage = pinnedUserMessageIdx !== null ? flatItems[pinnedUserMessageIdx] : null;
  const pinnedUserMsg = pinnedMessage?.type === "message" ? pinnedMessage.message : null;

  // Track atBottom for followOutput callback.
  // Stabilize the "at bottom" state: transitioning away from bottom is debounced
  // to prevent transient false reports (from footer re-layouts, smooth scroll
  // animations, or overscroll bounce) from interrupting followOutput and
  // causing visible jitter.
  const atBottomRef = useRef(true);
  const atBottomTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const handleAtBottomChange = useCallback((atBottom: boolean) => {
    if (atBottom) {
      // Immediately mark as at bottom (no delay)
      if (atBottomTimerRef.current) {
        clearTimeout(atBottomTimerRef.current);
        atBottomTimerRef.current = null;
      }
      atBottomRef.current = true;
      onAtBottomStateChange?.(true);
    } else {
      // Debounce leaving bottom: only commit after 150ms of sustained "not at bottom"
      if (!atBottomTimerRef.current) {
        atBottomTimerRef.current = setTimeout(() => {
          atBottomTimerRef.current = null;
          atBottomRef.current = false;
          onAtBottomStateChange?.(false);
        }, 150);
      }
    }
  }, [onAtBottomStateChange]);

  // Clean up timer on unmount
  useEffect(() => {
    return () => {
      if (atBottomTimerRef.current) {
        clearTimeout(atBottomTimerRef.current);
      }
    };
  }, []);

  // Only auto-scroll when user is at the bottom.
  // Use atBottomRef instead of Virtuoso's isAtBottom argument because
  // Virtuoso can transiently report isAtBottom=true during footer re-layouts.
  const handleFollowOutput = useCallback(() => {
    if (atBottomRef.current) return "smooth";
    return false;
  }, []);

  const computeItemKey = useCallback((_index: number, item: (typeof flatItems)[number]) => item.key, []);

  const renderItem = useCallback((_index: number, item: (typeof flatItems)[number]) => {
    if (item.type === "thread-start") {
      const isFork = item.workflow.origin === "fork";
      const label = isFork ? `forked from ${item.parentName}` : `from ${item.parentName}`;
      return (
        <TransitionDivider
          icon={isFork ? "fork" : "new"}
          label={label}
          name={item.workflow.name}
          color={item.workflow.color}
          routingInfo={item.workflow.routerDecision}
        />
      );
    }

    if (item.type === "handoff") {
      return (
        <TransitionDivider
          icon="handoff"
          label="handoff"
          name={item.toName}
          color={item.color}
        />
      );
    }

    if (item.type === "activity") {
      return (
        <div
          className={cn("transition-all", !item.workflow.isMain && "ml-1 pl-3")}
          style={
            !item.workflow.isMain
              ? {
                  borderLeftColor: item.workflow.color,
                  borderLeftWidth: 3,
                  borderLeftStyle: "solid",
                }
              : undefined
          }
        >
          <ActivityIndicator step={item.step} workflowName={item.workflowName} />
        </div>
      );
    }

    if (item.type === "error") {
      return (
        <div className="mb-2">
          <WorkflowErrorMessage error={item.error} />
        </div>
      );
    }

    if (item.type === "info") {
      return (
        <div className="message-content w-full px-2 mb-1">
          <WorkflowInfoMessage info={item.info} />
        </div>
      );
    }

    if (item.type === "skill_invocation") {
      return (
        <div className="message-content w-full px-2 mb-1">
          <SkillInvocationMessage invocation={item.invocation} />
        </div>
      );
    }

    if (item.type === "run_output") {
      return (
        <div className="mb-1">
          <RunStepExecution runOutput={item.runOutput} />
        </div>
      );
    }

    // Message item (both user and assistant)
    const msg = item.message;
    const isLastItem = item.isLast;

    if (msg.role === MessageRole.USER) {
      return (
        <ChatMessage
          message={msg}
          approvals={approvals}
          isLatestMessage={false}
          chatId={chatId}
          isStreaming={false}
          onForceYield={handleForceYield}
        />
      );
    }

    return (
      <div
        className={cn("transition-all", !item.workflow.isMain && "ml-1 pl-3")}
        style={
          !item.workflow.isMain
            ? {
                borderLeftColor: item.workflow.color,
                borderLeftWidth: 3,
                borderLeftStyle: "solid",
              }
            : undefined
        }
      >
        {isCompactionMessage(msg) ? (
          <CompactionMessage message={msg} chatId={chatId} />
        ) : msg.displayStyle ? (
          <SystemNotificationMessage message={msg} />
        ) : (
          <ChatMessage
            message={msg}
            approvals={approvals}
            isLatestMessage={isLastItem}
            chatId={chatId}
            isStreaming={isStreaming && isLastItem}
            onForceYield={handleForceYield}
          />
        )}
      </div>
    );
  }, [approvals, chatId, isStreaming, handleForceYield]);

  // Wrap each Virtuoso item in the padding/max-width container
  const wrappedRenderItem = useCallback((index: number, item: (typeof flatItems)[number]) => {
    return (
      <div className="px-4 sm:px-6 lg:px-8">
        <div className="max-w-[1200px] mx-auto">
          {renderItem(index, item)}
        </div>
      </div>
    );
  }, [renderItem]);

  // Use Virtuoso's context prop to pass footer content to the Footer component.
  // This keeps the Footer component identity stable (preventing Virtuoso re-mounts
  // that cause layout recalculations) while still re-rendering when footer changes.
  const footerContext = useMemo(() => ({ footer }), [footer]);

  const virtuosoComponents = useMemo(() => ({
    Footer: function VirtuosoFooter({ context }: { context?: { footer?: React.ReactNode } }) {
      if (!context?.footer) return null;
      return (
        <div className="px-4 sm:px-6 lg:px-8 pb-2">
          <div className="max-w-[1200px] mx-auto">
            {context.footer}
          </div>
        </div>
      );
    },
  }), []);

  if (flatItems.length === 0) {
    return (
      <div className="p-8 text-center text-muted-foreground">No messages yet</div>
    );
  }

  return (
    <div style={{ height: "100%", position: "relative" }}>
      {/* Pinned user message overlay */}
      {pinnedUserMsg && (
        <div
          className="absolute top-0 left-0 right-0 z-50"
          style={{ backgroundColor: "hsl(var(--background))" }}
          onMouseEnter={() => setIsHoveringPinned(true)}
          onMouseLeave={() => setIsHoveringPinned(false)}
        >
          {/* Jump to button */}
          <div
            className={`absolute right-6 top-2 z-10 transition-opacity duration-200 bg-background/95 backdrop-blur-sm rounded-full p-0.5 ${
              isHoveringPinned ? "opacity-100" : "opacity-0 pointer-events-none"
            }`}
          >
            <Tooltip content="Jump to" placement="left">
              <button
                onClick={handleJumpToPinned}
                className="flex items-center justify-center w-6 h-6 rounded-full bg-primary/90 text-primary-foreground hover:bg-primary transition-all duration-200 border border-primary-foreground/20 shadow-sm"
                aria-label="Jump to message"
              >
                <ArrowUp className="w-3 h-3" />
              </button>
            </Tooltip>
          </div>
          <div className="px-4 sm:px-6 lg:px-8">
            <div className="max-w-[1200px] mx-auto">
              <ChatMessage
                message={pinnedUserMsg}
                approvals={approvals}
                isLatestMessage={false}
                chatId={chatId}
                isStreaming={false}
                onForceYield={handleForceYield}
              />
            </div>
          </div>
        </div>
      )}
      <Virtuoso
        ref={virtuosoRef}
        data={flatItems}
        context={footerContext}
        computeItemKey={computeItemKey}
        initialTopMostItemIndex={flatItems.length - 1}
        followOutput={handleFollowOutput}
        atBottomThreshold={50}
        overscan={200}
        increaseViewportBy={200}
        itemContent={wrappedRenderItem}
        components={virtuosoComponents}
        atBottomStateChange={handleAtBottomChange}
        isScrolling={onIsScrolling}
        rangeChanged={handleRangeChanged}
        style={{ height: "100%" }}
      />
    </div>
  );
});