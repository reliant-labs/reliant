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
import { MessageRole, DisplayStyle } from "../../../gen/reliant/v1/chat_pb";
import { Virtuoso, type VirtuosoHandle, type ListRange } from "react-virtuoso";
import { acknowledgeScrollToMessage } from "../../../lib/scrollToMessage";
import { createFollowState, type FollowState } from "./followState";
import { GitBranch, ArrowRightLeft, Plus, ArrowUp, Route, Loader2 } from "lucide-react";
import { Tooltip } from "../../ui/Tooltip";
import { ChatMessage, type ChatTimelineVariant } from "../ChatMessage";
import { CompactionMessage, isCompactionMessage } from "../CompactionMessage";
import { WorkflowErrorMessage } from "../WorkflowErrorMessage";
import { WorkflowInfoMessage } from "../WorkflowInfoMessage";
import { SystemNotificationMessage } from "../SystemNotificationMessage";
import { RunStepExecution } from "../RunStepExecution";
import type { Message, ToolApprovalRequest } from "../../../api/client";
import type {
  ErrorUpdate,
  InfoUpdate,
  RunOutputUpdate,
} from "../../../types/streaming";
import type { WorkflowExecution, StepExecution, ThreadOrigin } from "../ExecutionSidebar/types";
import { cn } from "../../../lib/utils";
import { sortMessagesForDisplay } from "../../../lib/messageOrder";
import { getActivitySteps } from "./activityIndicators";
import { ActivityIndicator } from "./ActivityIndicator";
import { getThreadColor, formatNodeId, resolveThreadNameFromActiveThreads, resolveRouterDecisionFromActiveThreads, isSpawnOrigin } from "./threadUtils";
import { useActiveThreads } from "../../../store/threadActivityStore";
import { logger } from "../../../lib/logger";
import { settingsSync, SETTINGS_KEYS } from "../../../services/settingsSync";
import { getSpawnDisplayMode } from "../../Settings/SpawnDisplaySettings";
import { RubberBandScroller } from "./RubberBandScroller";

interface InterleavedTimelineProps {
  messages: Message[];
  approvals?: ToolApprovalRequest[];
  errorEvents?: ErrorUpdate[];
  infoEvents?: InfoUpdate[];
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
  /** Callback to select/navigate to a thread (e.g. from spawn preview "Open Thread" button) */
  onSelectThread?: (threadId: string | null) => void;
  /** Callback exposed so external "scroll to bottom" buttons can resume follow mode */
  onResumeFollow?: (cb: () => void) => void;
  /**
   * Load the next page of older messages (scroll-back paging). Called when the
   * user reaches the top of the list. Omit to disable paging entirely.
   */
  onLoadOlderMessages?: () => void;
  /** Whether an older-message page is currently in flight (renders a spinner). */
  isLoadingOlderMessages?: boolean;
  /** Whether older messages remain to be loaded. Gates the startReached trigger. */
  hasOlderMessages?: boolean;
}

/** Minimal info needed for rendering - derived from WorkflowExecution */

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
  /** How this thread was created — read from threads.origin via the API. */
  origin: ThreadOrigin;
  /** Whether this thread was created by the spawn tool (not a workflow node) */
  isSpawn: boolean;
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
  | { type: "run_output"; runOutput: RunOutputUpdate };

const TIMELINE_VARIANTS: ChatTimelineVariant[] = ["compact", "card", "minimal"];

function getStoredTimelineVariant(): ChatTimelineVariant {
  const stored = settingsSync.getSetting(SETTINGS_KEYS.CHAT_TIMELINE_VARIANT, "compact");
  return TIMELINE_VARIANTS.includes(stored as ChatTimelineVariant)
    ? (stored as ChatTimelineVariant)
    : "compact";
}

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
    // First workflow on a thread wins. Thread-level facts (name, origin) come
    // from the threads table and are identical across every workflow sharing
    // the thread, so later rows have nothing to add.
    if (!byThread.has(wf.thread)) {
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

    // Origin comes from the threads table, which owns thread identity, and is
    // NOT NULL there — every one of the 703 threads in a real database has a
    // value. So an empty origin here is never "this thread predates the
    // column"; it means the value was lost somewhere between the row and this
    // component, and the only honest thing to do is say so.
    //
    // There used to be a fallback of `isMain ? "main" : forkedFromThread ?
    // "fork" : "node"`. It could not produce "spawn" at all, so a spawned
    // sub-agent whose origin went missing was silently relabelled a node
    // thread — which made isSpawn false and dumped the entire sub-agent
    // transcript inline into the parent chat. The guess looked like a safety
    // net and was actually the bug report.
    const origin = wf.origin as ThreadOrigin;
    if (!origin) {
      logger.error(
        "[InterleavedTimeline] Workflow arrived with no thread origin; spawned threads will render inline",
        { workflowId: wf.id, thread: wf.thread, chatId },
      );
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
      isSpawn: isSpawnOrigin(origin),
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
  runOutputs = [],
  chatId,
  workflowExecution,
  selectedThreads,
  isStreaming = false,
  virtuosoRef,
  onAtBottomStateChange,
  onIsScrolling,
  footer,
  onSelectThread,
  onResumeFollow,
  onLoadOlderMessages,
  isLoadingOlderMessages = false,
  hasOlderMessages = false,
}: InterleavedTimelineProps) {
  const activeThreads = useActiveThreads(chatId);
  const [timelineVariant, setTimelineVariant] = useState<ChatTimelineVariant>(() => getStoredTimelineVariant());

  useEffect(() => {
    const handleAppearanceUpdated = () => {
      setTimelineVariant(getStoredTimelineVariant());
    };

    window.addEventListener("appearance-updated", handleAppearanceUpdated);
    return () => window.removeEventListener("appearance-updated", handleAppearanceUpdated);
  }, []);

  const timelineShellClass = cn(
    "chat-timeline-shell h-full",
    timelineVariant === "card" && "chat-timeline-card",
    timelineVariant === "minimal" && "chat-timeline-minimal"
  );
  const contentMaxWidthClass = timelineVariant === "minimal" ? "max-w-[900px]" : "max-w-[1200px]";
  const timelineHorizontalPaddingClass = timelineVariant === "minimal" ? "px-4 sm:px-8" : "px-4 sm:px-6 lg:px-8";
  const timelineGapClass = timelineVariant === "card" ? "py-1" : timelineVariant === "minimal" ? "py-0.5" : "";
  const timelineItems = useMemo(() => {
    // Build workflow lookups from execution tree
    const { byId, displays } = buildWorkflowLookups(workflowExecution, chatId);

    // Augment displays with streaming data (router decisions, titles, spawn status)
    for (const at of activeThreads) {
      const existing = displays.get(at.thread);
      if (existing) {
        if (at.router_decision && !existing.routerDecision) {
          existing.routerDecision = at.router_decision;
        }
        if (at.thread_title && existing.name === "Thread") {
          existing.name = formatNodeId(at.thread_title);
        }
        // The stream carries origin only to fill a GAP — a thread whose
        // execution tree has not been fetched yet. It must never override a
        // value that came from the workflow tree, because the tree reads
        // threads.origin (the column that owns thread provenance) while the
        // stream replays historical events that may predate a correction.
        //
        // Concretely: thread announcements are persisted in chat_updates and
        // replayed on reconnect. A thread announced before the spawn-origin
        // overwrite was fixed still has a stale "node" event on disk forever.
        // Letting that event win re-poisoned already-correct threads on every
        // reload, which is why the bug appeared fixed for new chats and stuck
        // for old ones.
        if (at.origin && !existing.origin) {
          existing.origin = at.origin;
          existing.isSpawn = isSpawnOrigin(at.origin);
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
        isSpawn: false,
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

    // Canonical order: per-thread ordinal, threads interleaved by clamped
    // time (lib/messageOrder). Raw createdAt is not trustworthy for ordering.
    const sorted = sortMessagesForDisplay(messages, chatId);

    for (const msg of sorted) {
      // TOOL-role messages carry only tool_result blocks; their content is
      // joined into the assistant tool-call cards at read time (via the store's
      // normalized tool-result index). Rendering them standalone would
      // synthesize empty-input duplicate cards (see ChatContainer/ChatPresenter
      // which filter the same way).
      if (msg.role === MessageRole.TOOL) continue;

      // Thread defaults to chatId (main thread) if not set
      const thread = msg.thread || chatId;
      if (!isVisible(thread)) continue;

      // Get workflow display info for this thread
      let display = displays.get(thread);
      if (!display) {
        // Thread exists but wasn't in workflow tree - create minimal display
        // Resolve name and spawn status from activeThreads streaming data
        const isMain = thread === chatId || thread === "0";
        const activeThread = activeThreads.find(at => at.thread === thread);
        const resolvedName = !isMain ? resolveThreadNameFromActiveThreads(thread, activeThreads) : undefined;
        const routerDec = !isMain ? resolveRouterDecisionFromActiveThreads(thread, activeThreads) : undefined;
        // Same rule as the workflow-tree path above: do not invent an origin.
        // `activeThreads` is LIVE streaming state, so it is empty for every
        // historical thread — defaulting to "node" here meant any spawned
        // thread not currently running was reported as a node thread and
        // rendered inline. Main is the one case we can assert from identity
        // rather than guess, because the main thread IS the chat.
        const streamedOrigin = isMain
          ? ("main" as ThreadOrigin)
          : (activeThread?.origin as ThreadOrigin | undefined);
        if (!streamedOrigin) {
          logger.error(
            "[InterleavedTimeline] Thread has messages but no workflow row and no live origin; cannot classify it",
            { thread, chatId },
          );
        }
        display = {
          id: thread,
          thread,
          name: isMain ? "Main" : (resolvedName || "Thread"),
          color: getThreadColor(thread, isMain),
          isMain,
          origin: streamedOrigin,
          isSpawn: isSpawnOrigin(streamedOrigin),
          routerDecision: routerDec,
        };
        displays.set(thread, display);
      }

      // In "preview" mode, spawn thread messages render inside the tool call instead
      // Only skip when viewing all threads — if a spawn thread is explicitly selected, show its messages
      const shouldCollapseSpawn =
        display.isSpawn && getSpawnDisplayMode() === "preview" && showAll;

      if (shouldCollapseSpawn) continue;

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

      // Skip hidden messages — these are for LLM context only, not shown to users.
      if (msg.displayStyle === DisplayStyle.HIDDEN) continue;

      // Skip assistant messages with no visible content — they render as zero-height
      // elements and cause Virtuoso's "Zero-sized element" warning + layout thrashing.
      // Compaction and display_style messages have their own renderers and are always visible.
      if (
        msg.role === MessageRole.ASSISTANT &&
        !msg.displayStyle &&
        (!msg.attachments || msg.attachments.length === 0)
      ) {
        if (!msg.contentBlocks?.length) continue;
        // Check if any block has actual visible content.
        // ask_user tool calls are filtered out by ChatMessage (they render
        // via QuestionPrompt instead), so they don't count as visible.
        const hasVisibleContent = msg.contentBlocks.some(
          (b) => {
            if (!b.content && !b.toolName && !b.toolCallId && !b.input) return false;
            if (b.toolName === "ask_user") return false;
            return true;
          }
        );
        if (!hasVisibleContent) continue;
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
        if (!display || (display.isSpawn && getSpawnDisplayMode() === "preview" && showAll)) continue;

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

    // Insert error events at correct positions based on timestamp.
    //
    // Scoped to the visible thread, exactly like messages and activity above.
    // Without this an error was chat-global: a single "Paused: no machine is
    // connected" from the main thread rendered inside EVERY thread of the chat,
    // including spawns that started 12h later and never saw the outage.
    //
    // An error with no thread predates thread scoping. It stays visible
    // everywhere rather than being assigned to a thread we'd have to guess —
    // the guess is what produced the wrong-thread render in the first place.
    for (const error of errorEvents) {
      if (error.thread && !isVisible(error.thread)) continue;
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
  }, [messages, chatId, workflowExecution, selectedThreads, errorEvents, infoEvents, runOutputs, activeThreads]);

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

  // --- Virtuoso prepend protocol (firstItemIndex) ---
  //
  // Virtuoso's "inverse infinite scroll" contract: when you PREPEND N items to
  // `data`, you must simultaneously DECREASE `firstItemIndex` by N. That delta
  // is how Virtuoso knows the items shifted down rather than the content having
  // changed, and it is what lets it hold the user's scroll anchored to the
  // message they were reading. Prepending without it makes the viewport jump on
  // every page load, so this is mandatory, not an optimization.
  //
  // It starts at a large constant because it must never go negative (Virtuoso
  // logs an error and misbehaves): 100_000 allows ~1000 pages of scroll-back.
  //
  // ⚠️ INDEX SPACES. Setting firstItemIndex splits Virtuoso's callbacks into two
  // different index spaces, and mixing them up silently corrupts scrolling:
  //   - SHIFTED (data index + firstItemIndex): `rangeChanged`, `startReached`,
  //     and the index passed to `itemContent` / `computeItemKey`.
  //   - DATA (a plain index into `flatItems`): `scrollToIndex` and
  //     `initialTopMostItemIndex`.
  // Everything below this component's boundary works in DATA space; the single
  // conversion happens in handleRangeChanged, which is the only place a SHIFTED
  // value enters logic that later indexes flatItems or calls scrollToIndex.
  const FIRST_ITEM_INDEX_BASE = 100_000;
  const [firstItemIndex, setFirstItemIndex] = useState(FIRST_ITEM_INDEX_BASE);
  // Mirrored in a ref so the shifted→data conversion can read the current value
  // without making every scroll callback depend on it.
  const firstItemIndexRef = useRef(FIRST_ITEM_INDEX_BASE);
  firstItemIndexRef.current = firstItemIndex;

  // Detect prepends by watching the identity of the FIRST rendered item. Derived
  // during render (React's supported "adjust state when props change" pattern)
  // rather than in an effect: firstItemIndex must land in the same commit as the
  // grown `data`, otherwise Virtuoso sees the prepend for one frame without the
  // delta and jumps before the effect can correct it.
  const [prependAnchor, setPrependAnchor] = useState<{
    firstKey: string | null;
    threadKey: string;
    chatId: string;
  }>({ firstKey: flatItems[0]?.key ?? null, threadKey, chatId });

  const currentFirstKey = flatItems[0]?.key ?? null;
  if (prependAnchor.chatId !== chatId || prependAnchor.threadKey !== threadKey) {
    // Different chat or thread filter — a different list entirely, not a
    // prepend. Reset the protocol rather than trying to diff across it.
    setFirstItemIndex(FIRST_ITEM_INDEX_BASE);
    setPrependAnchor({ firstKey: currentFirstKey, threadKey, chatId });
  } else if (currentFirstKey !== prependAnchor.firstKey) {
    // The head of the list changed. If the previous head is still present, the
    // items ahead of it are exactly what was prepended. If it is gone (a
    // replacing snapshot, a filter change), there is no meaningful delta — just
    // re-anchor, leaving firstItemIndex where it is.
    const prependedCount =
      prependAnchor.firstKey === null
        ? 0
        : flatItems.findIndex((item) => item.key === prependAnchor.firstKey);
    if (prependedCount > 0) {
      setFirstItemIndex((prev) => prev - prependedCount);
    }
    setPrependAnchor({ firstKey: currentFirstKey, threadKey, chatId });
  }

  // Store scroll positions per thread: { startIndex, atBottom }
  // startIndex is DATA space (see the index-space note above) — it is fed back
  // to scrollToIndex on thread switch.
  const scrollPositions = useRef<Map<string, { startIndex: number; atBottom: boolean }>>(new Map());
  const prevThreadKey = useRef<string>(threadKey);
  const lastRangeRef = useRef<ListRange | null>(null);

  // --- Scroll-follow state ---
  // Whether new content pulls the viewport down, and how a user scroll is told
  // apart from one of our own. See followState.ts.
  const followRef = useRef<FollowState | null>(null);
  if (followRef.current === null) {
    followRef.current = createFollowState();
  }
  const follow = followRef.current;

  // Save current scroll position for the active thread whenever range changes.
  // Use a ref to track the computed pinned index and only call setState when
  // the value actually changes to avoid unnecessary re-renders during scroll
  // that can trigger Virtuoso layout recalculations and cause jitter.
  const pinnedUserMessageIdxRef = useRef<number | null>(null);

  // Resolve the pinned header for a given first-visible row.
  //
  // userMessageForItem is POSITIONAL: it maps a flatItems index to the index of
  // the user message that heads its section. Every insertion above the viewport
  // — a streamed reply landing, a tool card expanding, an older page prepending
  // — shifts those indices, so a previously-correct pinned index silently comes
  // to point at a different message.
  const applyPinnedUserMessage = useCallback((firstVisible: number) => {
    const layerUserIdx = userMessageForItem[firstVisible] ?? null;
    const nextPinned =
      layerUserIdx !== null && layerUserIdx < firstVisible ? layerUserIdx : null;

    // Only re-render when the pinned index actually changes; Virtuoso fires
    // rangeChanged continuously during a scroll and extra setState calls there
    // cause layout recalculation and visible jitter.
    if (nextPinned !== pinnedUserMessageIdxRef.current) {
      pinnedUserMessageIdxRef.current = nextPinned;
      setPinnedUserMessageIdx(nextPinned);
    }
  }, [userMessageForItem]);

  // Recompute the pin whenever the mapping changes, not only when the user
  // scrolls. Virtuoso fires rangeChanged on scroll, so without this the pinned
  // header keeps whatever index it had when the last scroll ended — and after
  // rows are inserted above the viewport that index now names the wrong
  // message. That is the "wrong chat pinned at the header" report, and it needs
  // no branching or thread switching to reproduce: a reply arriving while you
  // sit still is enough.
  useEffect(() => {
    const startIndex = lastRangeRef.current?.startIndex;
    if (startIndex === undefined) return;
    applyPinnedUserMessage(startIndex);
  }, [applyPinnedUserMessage]);

  const handleRangeChanged = useCallback((range: ListRange) => {
    // `range` arrives in SHIFTED space; everything downstream (userMessageForItem
    // lookups, pinnedUserMessageIdx → flatItems[...], the saved scroll position
    // → scrollToIndex) indexes flatItems directly. Convert once, here.
    const offset = firstItemIndexRef.current;
    const dataRange: ListRange = {
      startIndex: range.startIndex - offset,
      endIndex: range.endIndex - offset,
    };
    lastRangeRef.current = dataRange;
    applyPinnedUserMessage(dataRange.startIndex);
  }, [applyPinnedUserMessage]);

  // Persist scroll position for current thread on every range/atBottom change
  useEffect(() => {
    const startIndex = lastRangeRef.current?.startIndex ?? 0;
    scrollPositions.current.set(threadKey, {
      startIndex,
      atBottom: follow.atBottom,
    });
  });

  // On thread switch: restore saved position or scroll to bottom (instant)
  useEffect(() => {
    if (prevThreadKey.current === threadKey) return;
    prevThreadKey.current = threadKey;

    // Drop the previous thread's viewport and pin.
    //
    // Both are POSITIONAL indices into flatItems, and flatItems is rebuilt
    // wholesale when the thread changes — so index N in the old thread names a
    // completely unrelated row in the new one. Carrying them across meant the
    // pinned header rendered a message from the thread you just left (and, when
    // the old index was past the end of a shorter thread, nothing at all).
    //
    // Clearing rather than recomputing: there is no correct pin until Virtuoso
    // reports a real range for the new content. Showing no header briefly is
    // honest; showing another thread's message is not.
    lastRangeRef.current = null;
    pinnedUserMessageIdxRef.current = null;
    setPinnedUserMessageIdx(null);

    // Reset follow state for the new thread
    follow.resumeFollow();

    // Use requestAnimationFrame to let Virtuoso re-render with new data first
    const rafId = requestAnimationFrame(() => {
      const saved = scrollPositions.current.get(threadKey);
      if (saved && !saved.atBottom) {
        // Restore their previous position in this thread
        follow.releaseFollow();
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
  }, [follow, threadKey, virtuosoRef]);

  const handleJumpToPinned = useCallback(() => {
    if (pinnedUserMessageIdx !== null && virtuosoRef?.current) {
      virtuosoRef.current.scrollToIndex({
        index: pinnedUserMessageIdx,
        behavior: "auto",
        align: "start",
      });
    }
  }, [pinnedUserMessageIdx, virtuosoRef]);



  // Get the pinned user message data
  const pinnedMessage = pinnedUserMessageIdx !== null ? flatItems[pinnedUserMessageIdx] : null;
  const pinnedUserMsg = pinnedMessage?.type === "message" ? pinnedMessage.message : null;

  const handleAtBottomChange = useCallback((atBottom: boolean) => {
    follow.setAtBottom(atBottom);
    onAtBottomStateChange?.(atBottom);
  }, [follow, onAtBottomStateChange]);

  const handleIsScrolling = useCallback((scrolling: boolean) => {
    follow.noteScrolling(scrolling);
    onIsScrolling?.(scrolling);
  }, [follow, onIsScrolling]);

  // Expose a "resume follow" callback so external scroll-to-bottom buttons
  // can re-enable following and trigger a programmatic scroll.
  const resumeFollow = useCallback(() => {
    follow.resumeFollow();
    virtuosoRef?.current?.scrollToIndex({
      index: "LAST",
      align: "end",
      behavior: "auto",
    });
  }, [follow, virtuosoRef]);

  // Register the resumeFollow callback with the parent
  useEffect(() => {
    onResumeFollow?.(resumeFollow);
  }, [onResumeFollow, resumeFollow]);

  // Jump to a specific message, e.g. from a chat-search hit.
  //
  // The list is virtualized, so the target usually has no DOM node yet and we
  // cannot scroll to an element — we resolve the message id to its index in
  // `flatItems` and let Virtuoso render it. `scrollToIndex` takes DATA space,
  // which is what flatItems is indexed in, so no firstItemIndex shift applies
  // here (see the index-space note above).
  const [highlightedMessageId, setHighlightedMessageId] = useState<
    string | null
  >(null);

  useEffect(() => {
    const handleScrollToMessage = (event: Event) => {
      const messageId = (event as CustomEvent<{ messageId?: string }>).detail
        ?.messageId;
      if (!messageId || !virtuosoRef?.current) return;

      const index = flatItems.findIndex(
        (item) => item.type === "message" && item.message.id === messageId,
      );
      // Not in the loaded window — the message lives in a page we have not
      // fetched yet, so there is nothing to scroll to.
      if (index === -1) return;

      // Mark as programmatic so follow-mode does not read this as the user
      // scrolling away from the bottom — but do hold position at the target
      // rather than snapping back down on the next streamed chunk.
      follow.beginProgrammaticScroll();
      follow.releaseFollow();
      virtuosoRef.current.scrollToIndex({
        index,
        align: "center",
        behavior: "auto",
      });
      setHighlightedMessageId(messageId);
      // Tell the requester to stop retrying; it cannot observe this otherwise.
      acknowledgeScrollToMessage(messageId);
    };

    window.addEventListener("scroll-to-message", handleScrollToMessage);
    return () =>
      window.removeEventListener("scroll-to-message", handleScrollToMessage);
  }, [follow, flatItems, virtuosoRef]);

  // Clear the highlight once it has had time to register visually.
  useEffect(() => {
    if (!highlightedMessageId) return;
    const timer = setTimeout(() => setHighlightedMessageId(null), 2000);
    return () => clearTimeout(timer);
  }, [highlightedMessageId]);

  // "Focus the conversation" — hands the transcript keyboard focus so it can be
  // scrolled and read without the mouse. Virtuoso renders into a scrollable
  // child, so focus goes to that rather than the outer shell.
  useEffect(() => {
    const handleFocusTranscript = () => {
      const shell = timelineContainerRef.current;
      if (!shell) return;

      // Virtuoso owns the scroll container and does not expose a stable hook
      // for it, so find the element that actually scrolls rather than matching
      // on a class name that could change with a library upgrade.
      const scroller =
        Array.from(shell.querySelectorAll<HTMLElement>("*")).find(
          (el) =>
            el.scrollHeight > el.clientHeight &&
            /auto|scroll/.test(getComputedStyle(el).overflowY),
        ) ?? shell;

      // Focusable only as a keyboard target; it is not in the tab order.
      if (!scroller.hasAttribute("tabindex")) {
        scroller.setAttribute("tabindex", "-1");
      }
      scroller.focus({ preventScroll: true });
    };

    window.addEventListener("focus-transcript", handleFocusTranscript);
    return () =>
      window.removeEventListener("focus-transcript", handleFocusTranscript);
  }, []);

  const isStreamingRef = useRef(isStreaming);
  isStreamingRef.current = isStreaming;
  follow.setStreaming(isStreaming);
  const timelineContainerRef = useRef<HTMLDivElement>(null);

  // Publish the pinned header's height so per-message hover toolbars can stick
  // below it instead of underneath it. Measured rather than hard-coded: the
  // header wraps a real message bubble whose height moves with the font size
  // and the timeline variant.
  const pinnedHeaderRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const shell = timelineContainerRef.current;
    if (!shell) return;

    const header = pinnedHeaderRef.current;
    if (!header) {
      shell.style.removeProperty("--chat-pinned-header-h");
      return;
    }

    const publish = () => {
      shell.style.setProperty("--chat-pinned-header-h", `${header.offsetHeight}px`);
    };
    publish();

    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(publish);
    observer.observe(header);
    return () => observer.disconnect();
  }, [pinnedUserMsg]);
  // Detect user-initiated scroll-up during streaming.
  //
  // While streaming, followState treats an unexplained scroll as one of ours
  // (Virtuoso's SIZE_INCREASED correction cannot be announced), so intent has
  // to come from the input device instead. Every device that can scroll this
  // list therefore needs a listener here — a gap means that device silently
  // cannot escape follow mode, which reads as the timeline yanking you back.
  useEffect(() => {
    const el = timelineContainerRef.current;
    if (!el) return;

    const onWheel = (e: WheelEvent) => {
      if (!isStreamingRef.current) return;
      follow.noteWheel(e.deltaY, e.timeStamp);
    };

    // Touch reports absolute positions, not deltas, and its sign is inverted
    // relative to the wheel: dragging a finger DOWN reveals EARLIER content,
    // which is the wheel's negative direction.
    let lastTouchY: number | null = null;
    const onTouchStart = (e: TouchEvent) => {
      lastTouchY = e.touches[0]?.clientY ?? null;
    };
    const onTouchMove = (e: TouchEvent) => {
      const y = e.touches[0]?.clientY;
      if (y === undefined) return;
      if (lastTouchY !== null && isStreamingRef.current) {
        follow.noteTouchMove(lastTouchY - y, e.timeStamp);
      }
      lastTouchY = y;
    };
    const onTouchEnd = () => {
      lastTouchY = null;
    };

    const onKeyDown = (e: KeyboardEvent) => {
      if (!isStreamingRef.current) return;
      if (e.key === "ArrowUp" || e.key === "PageUp" || e.key === "Home") {
        follow.noteKeyScrollUp();
      }
    };

    el.addEventListener('wheel', onWheel, { passive: true });
    el.addEventListener('touchstart', onTouchStart, { passive: true });
    el.addEventListener('touchmove', onTouchMove, { passive: true });
    el.addEventListener('touchend', onTouchEnd, { passive: true });
    el.addEventListener('touchcancel', onTouchEnd, { passive: true });
    el.addEventListener('keydown', onKeyDown);
    return () => {
      el.removeEventListener('wheel', onWheel);
      el.removeEventListener('touchstart', onTouchStart);
      el.removeEventListener('touchmove', onTouchMove);
      el.removeEventListener('touchend', onTouchEnd);
      el.removeEventListener('touchcancel', onTouchEnd);
      el.removeEventListener('keydown', onKeyDown);
    };
  }, [follow]);

  // On mount we start at the bottom, and followOutput would return "smooth"
  // — causing Virtuoso to slowly smooth-scroll through the entire conversation.
  // Use "auto" (instant jump) until the first frame settles.
  const initialScrollDoneRef = useRef(false);
  useEffect(() => {
    const raf = requestAnimationFrame(() => { initialScrollDoneRef.current = true; });
    return () => cancelAnimationFrame(raf);
  }, []);

  const handleFollowOutput = useCallback(() => {
    if (!follow.shouldFollow()) {
      return false;
    }
    // Returning a behavior means Virtuoso is about to scroll on its own.
    // Claim it, so the resulting isScrolling(true) is not read as the user
    // scrolling away — see the note in followState.noteScrolling.
    follow.beginProgrammaticScroll();
    // During streaming, always use "auto" (instant jump). "smooth" causes
    // visible jitter because each new content update fires a new
    // scrollTo({behavior:"smooth"}) that competes with Virtuoso's internal
    // SIZE_INCREASED auto-scroll (which uses "auto"), creating
    // discontinuities as smooth animations are interrupted mid-flight.
    return initialScrollDoneRef.current && !isStreaming ? "smooth" : "auto";
  }, [follow, isStreaming]);

  // Scroll-back trigger. Virtuoso fires startReached whenever the first item is
  // rendered — including on initial mount for a short chat, and repeatedly while
  // the user sits at the top. The store's own in-flight guard makes duplicate
  // calls harmless; these checks just avoid the pointless round-trips.
  const handleStartReached = useCallback(() => {
    if (!onLoadOlderMessages) return;
    if (!hasOlderMessages || isLoadingOlderMessages) return;
    onLoadOlderMessages();
  }, [hasOlderMessages, isLoadingOlderMessages, onLoadOlderMessages]);

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
          className={cn(
            // Colors only — never transition-all. Virtuoso measures row heights
            // with a ResizeObserver, so an animated height reports a new value
            // every frame of the animation and each one triggers a re-measure
            // and a follow-scroll correction.
            "transition-colors",
            !item.workflow.isMain && "pl-3",
            !item.workflow.isMain && timelineVariant !== "minimal" && "ml-1",
            timelineVariant === "card" && "rounded-xl border border-border/50 bg-card/60 p-2 shadow-sm"
          )}
          style={
            !item.workflow.isMain
              ? {
                  borderLeftColor: item.workflow.color,
                  borderLeftWidth: timelineVariant === "minimal" ? 2 : 3,
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
          isLatestMessage={isLastItem}
          chatId={chatId}
          isStreaming={false}
          onSelectThread={onSelectThread}
          timelineVariant={timelineVariant}
        />
      );
    }

    return (
      <div
        className={cn(
          // See the note on the activity row above: animated heights inside a
          // virtualized list re-trigger measurement on every frame.
          "transition-colors",
          !item.workflow.isMain && "pl-3",
          !item.workflow.isMain && timelineVariant !== "minimal" && "ml-1",
          timelineVariant === "card" && !item.workflow.isMain && "rounded-xl bg-background/30"
        )}
        style={
          !item.workflow.isMain
            ? {
                borderLeftColor: item.workflow.color,
                borderLeftWidth: timelineVariant === "minimal" ? 2 : 3,
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
            onSelectThread={onSelectThread}
            timelineVariant={timelineVariant}
          />
        )}
      </div>
    );
  }, [approvals, chatId, isStreaming, onSelectThread, timelineVariant]);

  // Wrap each Virtuoso item in the padding/max-width container
  const wrappedRenderItem = useCallback((index: number, item: (typeof flatItems)[number]) => {
    // After a search jump, briefly ring the target so the eye can find it —
    // landing mid-conversation with no cue makes the jump feel like it failed.
    const isHighlighted =
      highlightedMessageId !== null &&
      item.type === "message" &&
      item.message.id === highlightedMessageId;

    return (
      <div className={cn(timelineHorizontalPaddingClass, timelineGapClass)}>
        <div
          className={cn(
            contentMaxWidthClass,
            "mx-auto",
            isHighlighted &&
              "rounded-lg ring-2 ring-primary/60 transition-shadow duration-500",
          )}
        >
          {renderItem(index, item)}
        </div>
      </div>
    );
  }, [contentMaxWidthClass, highlightedMessageId, renderItem, timelineGapClass, timelineHorizontalPaddingClass]);

  // Use Virtuoso's context prop to pass footer content to the Footer component.
  // This keeps the Footer component identity stable (preventing Virtuoso re-mounts
  // that cause layout recalculations) while still re-rendering when footer changes.
  const footerContext = useMemo(
    () => ({
      footer,
      isStreaming,
      contentMaxWidthClass,
      timelineHorizontalPaddingClass,
      isLoadingOlderMessages,
    }),
    [
      contentMaxWidthClass,
      footer,
      isLoadingOlderMessages,
      isStreaming,
      timelineHorizontalPaddingClass,
    ]
  );

  const virtuosoComponents = useMemo(() => ({
    Scroller: RubberBandScroller,
    // The header doubles as the scroll-back loading indicator. It must keep a
    // non-zero height in both states so the prepend does not also change the
    // header's size while firstItemIndex is compensating for the new items.
    Header: function VirtuosoHeader({
      context,
    }: {
      context?: { isLoadingOlderMessages?: boolean };
    }) {
      if (!context?.isLoadingOlderMessages) {
        return <div className="pt-2" />;
      }
      return (
        <div className="flex items-center justify-center gap-2 py-2 text-xs text-muted-foreground">
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
          <span>Loading earlier messages…</span>
        </div>
      );
    },
    Footer: function VirtuosoFooter({
      context,
    }: {
      context?: {
        footer?: React.ReactNode;
        contentMaxWidthClass?: string;
        timelineHorizontalPaddingClass?: string;
      };
    }) {
      // Always render bottom padding that stays within atBottomThreshold (80px)
      // so overscroll bounces don't cause atBottom to flap.
      if (!context?.footer) {
        return <div className="pb-10" />;
      }
      return (
        <div className={cn(context.timelineHorizontalPaddingClass || "px-4 sm:px-6 lg:px-8", "pb-3")}>
          <div className={cn(context.contentMaxWidthClass || "max-w-[1200px]", "mx-auto")}>
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
    <div
      ref={timelineContainerRef}
      className={timelineShellClass}
      style={{ position: "relative" }}
      // Focus target for "focus the conversation" — makes the transcript
      // keyboard-scrollable (arrows, PageUp/Down, Home/End) without stealing
      // those keys from anywhere else, since they only apply while focused.
      data-context="transcript"
      tabIndex={-1}
    >
      {/* Pinned user message overlay */}
      {pinnedUserMsg && (
        <div
          ref={pinnedHeaderRef}
          // Fully opaque and elevated: the timeline scrolls underneath, so any
          // translucency here would let message text show through the gaps
          // around the floating bubble.
          className="pointer-events-auto absolute inset-x-0 top-0 z-50 border-b border-border/60 bg-background pt-1 pb-1.5 shadow-md"
          onMouseEnter={() => setIsHoveringPinned(true)}
          onMouseLeave={() => setIsHoveringPinned(false)}
        >
          <div className={timelineHorizontalPaddingClass}>
            <div className={cn(contentMaxWidthClass, "mx-auto flex items-center gap-2")}>
              <div className="min-w-0 flex-1">
                <ChatMessage
                  message={pinnedUserMsg}
                  approvals={approvals}
                  isLatestMessage={false}
                  chatId={chatId}
                  isStreaming={false}
                  onSelectThread={onSelectThread}
                  timelineVariant={timelineVariant}
                  pinned
                />
              </div>
              <Tooltip content="Jump to" placement="left">
                <button
                  onClick={handleJumpToPinned}
                  className={cn(
                    "flex h-6 w-6 shrink-0 items-center justify-center rounded-full border border-primary-foreground/20 bg-primary/90 text-primary-foreground shadow-sm transition-all duration-200 hover:bg-primary",
                    isHoveringPinned ? "opacity-100" : "opacity-0"
                  )}
                  aria-label="Jump to message"
                >
                  <ArrowUp className="w-3 h-3" />
                </button>
              </Tooltip>
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
        // Prepend protocol — see the FIRST_ITEM_INDEX_BASE block above. Must be
        // decremented by exactly the number of items prepended, in the same
        // commit as the grown `data`, or the scroll position jumps.
        firstItemIndex={firstItemIndex}
        startReached={handleStartReached}
        followOutput={handleFollowOutput}
        atBottomThreshold={80}
        overscan={200}
        increaseViewportBy={200}
        itemContent={wrappedRenderItem}
        components={virtuosoComponents}
        atBottomStateChange={handleAtBottomChange}
        isScrolling={handleIsScrolling}
        rangeChanged={handleRangeChanged}
        style={{ height: "100%" }}
      />
    </div>
  );
});