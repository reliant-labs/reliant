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
import { ContentBlockType, MessageRole, DisplayStyle } from "../../../gen/reliant/v1/chat_pb";
import { acknowledgeScrollToMessage } from "../../../lib/scrollToMessage";
import { useTimelineVirtualizer } from "./useTimelineVirtualizer";
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
import type { WorkflowExecution, ThreadOrigin } from "../ExecutionSidebar/types";
import { cn } from "../../../lib/utils";
import { sortMessagesForDisplay } from "../../../lib/messageOrder";
import { cleanTemporalErrorMessage } from "../../../lib/temporalErrors";
import { getThreadColor, formatNodeId, resolveThreadNameFromActiveThreads, resolveRouterDecisionFromActiveThreads, isSpawnOrigin } from "./threadUtils";
import { useActiveThreads } from "../../../store/threadActivityStore";
import { logger } from "../../../lib/logger";
import { settingsSync, SETTINGS_KEYS } from "../../../services/settingsSync";
import { getSpawnDisplayMode } from "../../Settings/SpawnDisplaySettings";
import {
  measureRows,
  resolvePinnedUserMessage,
  TIMELINE_ROW_INDEX_ATTR,
} from "./pinnedHeader";

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
  /** Callback when the transcript arrives at, or leaves, the bottom */
  onAtBottomStateChange?: (atBottom: boolean) => void;
  /** Footer element rendered at the bottom of the transcript (e.g. thinking indicator) */
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
  /**
   * `error` is the representative (earliest) failure and is what renders.
   * `errors` is every failure collapsed into this row — length 1 in the normal
   * case, N when several threads failed the same way at the same time. See
   * groupVisibleErrors.
   */
  | { type: "error"; error: ErrorUpdate; errors: ErrorUpdate[] }
  | { type: "info"; info: InfoUpdate }
  | { type: "run_output"; runOutput: RunOutputUpdate };

/**
 * Row key for a timeline item at a given index into timelineItems.
 *
 * Derived on demand rather than baked into the item, because a `key` field
 * would force a fresh wrapper object per row on every rebuild — and the
 * timeline rebuilds on every streamed delta, so that is thousands of
 * throwaway allocations a second during a long response.
 *
 * Only "handoff" needs the index: a handoff carries no id of its own, and two
 * handoffs to the same workflow are distinguishable only by position.
 */
function timelineItemKey(item: TimelineItem, index: number): string {
  switch (item.type) {
    case "message":
      return item.message.id;
    case "thread-start":
      return `thread-${item.workflow.thread}`;
    case "handoff":
      return `handoff-${index}`;
    case "error":
      // Keyed on the representative plus the group size: a row that absorbs a
      // late sibling must remount rather than reuse the ungrouped row's key.
      return `error-${item.error.id}-${item.errors.length}`;
    case "info":
      return `info-${item.info.id}`;
    case "run_output":
      return `run-${item.runOutput.id}`;
  }
}

/**
 * How far apart two identical failures may be and still read as ONE incident.
 *
 * When a provider goes down, every thread that happens to be mid-LLM-call
 * fails independently, and each failure is a genuinely separate row in the
 * database — different activity, different thread, different attempt number.
 * They are not duplicates and must never be merged in the store or the DB.
 * But to the user they are one outage, and N stacked identical banners read
 * as N problems.
 *
 * 60s is chosen from the observed data: a real chat produced three such rows
 * within 15 seconds and another produced five within the same minute, while
 * genuinely distinct incidents in those chats were minutes apart. The window
 * is anchored to the FIRST error in a group rather than sliding off the most
 * recent one, so a long stream of failures cannot chain into one unbounded
 * row that claims a five-minute outage was a single moment.
 */
export const CONCURRENT_ERROR_WINDOW_MS = 60_000;

/**
 * One rendered error row: the representative failure plus every failure
 * collapsed into it. `errors` always contains `error` and is length 1 unless
 * a collapse happened.
 */
export interface ErrorGroup {
  error: ErrorUpdate;
  errors: ErrorUpdate[];
}

/**
 * Whether a thread is currently on screen, given the thread filter.
 *
 * The main thread ships under three encodings — the chat id itself, "0", and
 * the empty string — and an absent thread means main. Extracted from the
 * timeline memo so error scoping and message scoping cannot drift, and so the
 * main-thread-id-equals-chat-id case can be pinned by a test.
 */
export function createThreadVisibilityCheck(
  chatId: string,
  selectedThreads: Set<string> | null | undefined,
): (thread: string | undefined) => boolean {
  const showAll = !selectedThreads || selectedThreads.size === 0;
  return (thread: string | undefined) => {
    // Treat undefined/empty thread as main thread
    const effectiveThread = thread || chatId;
    if (showAll) return true;
    if (selectedThreads?.has(effectiveThread)) return true;
    const isMain =
      effectiveThread === chatId || effectiveThread === "0" || effectiveThread === "";
    const selectedMain =
      selectedThreads?.has(chatId) ||
      selectedThreads?.has("0") ||
      selectedThreads?.has("");
    return isMain && selectedMain;
  };
}

/**
 * The user-visible identity of a failure: what the row will actually say.
 *
 * Grouping on the DISPLAYED text is deliberate — two rows the user cannot tell
 * apart are what makes a stack of banners feel like a bug, and two rows that
 * read differently must stay separate however similar their internals are.
 * Prefers the backend's `error_summary`, falling back to the same cleaned
 * message the row renders when there is none.
 */
function errorDisplayIdentity(error: ErrorUpdate): string {
  return error.error_summary?.trim() || cleanTemporalErrorMessage(error.error_message);
}

/**
 * Filter errors to the visible threads and collapse concurrent identical
 * failures into single rows.
 *
 * Pure and derived: the input array and its entries are never mutated. Two
 * errors collapse only when ALL of these hold:
 *   - several threads are visible (`collapseAcrossThreads`). When the user has
 *     one thread selected they are looking AT that thread and want its own
 *     error, so nothing collapses.
 *   - same chat. Errors from different chats are never merged.
 *   - same displayed summary (see errorDisplayIdentity).
 *   - within CONCURRENT_ERROR_WINDOW_MS of the group's first error.
 *   - DIFFERENT threads. Two failures on one thread are two things that
 *     happened to that thread; a retry series of one failure is already folded
 *     store-side by id (see applyErrorUpdates).
 *
 * An error with NO thread predates thread scoping. It stays visible everywhere
 * rather than being guessed into a thread, and it is never absorbed into
 * another thread's group — a guess about which threads it covers would be the
 * same mistake in a new place.
 */
export function groupVisibleErrors(
  errors: readonly ErrorUpdate[],
  opts: {
    isVisible: (thread: string | undefined) => boolean;
    collapseAcrossThreads: boolean;
  },
): ErrorGroup[] {
  const visible = errors.filter((error) => !error.thread || opts.isVisible(error.thread));

  if (!opts.collapseAcrossThreads) {
    return visible.map((error) => ({ error, errors: [error] }));
  }

  // Ascending by time so a group's first entry is its earliest, which is what
  // anchors the window and what represents the row on the timeline. Sorted on
  // a copy — `errorEvents` belongs to the store.
  const ordered = [...visible].sort(
    (a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime(),
  );

  const groups: Array<{
    group: ErrorGroup;
    anchorTime: number;
    identity: string;
    threads: Set<string>;
  }> = [];

  for (const error of ordered) {
    if (!error.thread) {
      // Legacy, chat-wide: stands alone.
      groups.push({
        group: { error, errors: [error] },
        anchorTime: Number.NaN,
        identity: "",
        threads: new Set(),
      });
      continue;
    }

    const identity = errorDisplayIdentity(error);
    const time = new Date(error.timestamp).getTime();
    const open = groups.find(
      (candidate) =>
        candidate.identity === identity &&
        candidate.group.error.chat_id === error.chat_id &&
        !candidate.threads.has(error.thread as string) &&
        time - candidate.anchorTime <= CONCURRENT_ERROR_WINDOW_MS,
    );

    if (open) {
      open.group.errors.push(error);
      open.threads.add(error.thread);
      continue;
    }

    groups.push({
      group: { error, errors: [error] },
      anchorTime: time,
      identity,
      threads: new Set([error.thread]),
    });
  }

  return groups.map((entry) => entry.group);
}

/**
 * An error row, with the count when several threads failed the same way at the
 * same time. A thin wrapper rather than a prop on WorkflowErrorMessage: the
 * count is a fact about this timeline's grouping, not about the error itself,
 * and the presentational component stays unaware of it.
 */
const GroupedErrorRow = memo(function GroupedErrorRow({
  error,
  errors,
}: ErrorGroup) {
  const affectedThreads = errors.length;
  return (
    <div className="mb-2">
      {affectedThreads > 1 && (
        <div className="px-2 pb-1 text-xs text-muted-foreground">
          ×{affectedThreads} — the same failure hit {affectedThreads} threads at once
        </div>
      )}
      <WorkflowErrorMessage error={error} />
    </div>
  );
});

const TIMELINE_VARIANTS: ChatTimelineVariant[] = ["compact", "card", "minimal"];

/**
 * How far past the header's bottom edge an already-pinned user message must
 * travel before it gives the header up. Applied to release only — see the
 * reasoning on PinnedHeaderInput.releaseHysteresisPx. Large enough to swallow
 * sub-pixel layout corrections, and small on purpose: the first message's
 * bottom edge has to clear `header + this` for the header to leave at the top
 * of the transcript, and a full line of slack made that unreachable.
 */
const PINNED_HEADER_RELEASE_HYSTERESIS_PX = 8;

/**
 * The pinned header's height before it has ever been measured: one clamped
 * line of user text plus the header's padding. Only the first resolve uses
 * it; every later one reads the last measured height.
 */
const PINNED_HEADER_ESTIMATED_PX = 48;

/**
 * Height reserved above the first row for the scroll-back loading indicator.
 * Fixed in both states, so the indicator toggling never moves the rows.
 */
const TIMELINE_HEADER_PX = 32;

function getStoredTimelineVariant(): ChatTimelineVariant {
  const stored = settingsSync.getSetting(SETTINGS_KEYS.CHAT_TIMELINE_VARIANT, "compact");
  return TIMELINE_VARIANTS.includes(stored as ChatTimelineVariant)
    ? (stored as ChatTimelineVariant)
    : "compact";
}

export function isToolOnlyAssistantMessage(message: Message): boolean {
  if (message.role !== MessageRole.ASSISTANT) return false;
  if (message.displayStyle) return false;
  if (message.attachments?.length) return false;

  const blocks = message.contentBlocks || [];
  let hasToolCall = false;

  for (const block of blocks) {
    if (block.type === ContentBlockType.TOOL_CALL) {
      if (block.toolName === "ask_user") return false;
      if (block.toolName || block.toolCallId || block.input) {
        hasToolCall = true;
      }
      continue;
    }

    if (block.type === ContentBlockType.TEXT && block.content?.trim()) {
      return false;
    }
  }

  return hasToolCall;
}

type ToolRowSpacingItem = { type: string; message?: Message };

export interface ToolRowSpacing {
  compact: boolean;
  compactBefore: boolean;
  compactAfter: boolean;
}

export function getToolRowSpacing(
  items: readonly ToolRowSpacingItem[],
  index: number,
): ToolRowSpacing {
  const item = items[index];
  const previousItem = index > 0 ? items[index - 1] : undefined;
  const nextItem = index < items.length - 1 ? items[index + 1] : undefined;
  const isToolOnlyRow = item?.type === "message" && item.message && isToolOnlyAssistantMessage(item.message);
  const compactBefore = Boolean(
    isToolOnlyRow &&
      previousItem?.type === "message" &&
      previousItem.message &&
      isToolOnlyAssistantMessage(previousItem.message),
  );
  const compactAfter = Boolean(
    isToolOnlyRow &&
      nextItem?.type === "message" &&
      nextItem.message &&
      isToolOnlyAssistantMessage(nextItem.message),
  );

  return {
    compact: compactBefore || compactAfter,
    compactBefore,
    compactAfter,
  };
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
  onAtBottomStateChange,
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
    const isVisible = createThreadVisibilityCheck(chatId, selectedThreads);

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
          ? "main"
          : activeThread?.origin;
        if (!streamedOrigin) {
          logger.error(
            "[InterleavedTimeline] Thread has messages but no workflow row and no live origin; cannot classify it",
            { thread, chatId },
          );
          // SKIP the thread rather than fabricating a display for it. The log
          // above says the thread cannot be classified; continuing anyway
          // built a WorkflowDisplay with `origin: undefined`, which violates
          // the type and — worse — silently rendered an unclassifiable thread
          // as though it were a node thread. Every field below depends on an
          // origin we do not have.
          continue;
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
      // rows, which the virtualizer measures and positions for nothing.
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

    // Insert error events at correct positions based on timestamp.
    //
    // Scoped to the visible thread, exactly like messages above.
    // Without this an error was chat-global: a single "Paused: no machine is
    // connected" from the main thread rendered inside EVERY thread of the chat,
    // including spawns that started 12h later and never saw the outage.
    //
    // An error with no thread predates thread scoping. It stays visible
    // everywhere rather than being assigned to a thread we'd have to guess —
    // the guess is what produced the wrong-thread render in the first place.
    //
    // Concurrent identical failures across SEVERAL visible threads collapse to
    // one row carrying a count. They stay separate rows in the store and the
    // database, where they really are separate events; this is derived render
    // state only. See groupVisibleErrors.
    const errorGroups = groupVisibleErrors(errorEvents, {
      isVisible,
      collapseAcrossThreads: showAll || (selectedThreads?.size ?? 0) > 1,
    });

    for (const group of errorGroups) {
      const error = group.error;
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
        errors: group.errors,
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

  // `timelineItems` IS the list the virtualizer renders. There is deliberately
  // no per-row wrapper carrying `key`/`isLast`: both are derivable from the
  // item itself, and materializing them meant spreading a fresh object for
  // EVERY row in the conversation — not just the visible ones — on every
  // rebuild. The timeline rebuilds on every streamed delta, so that was the
  // whole transcript re-allocated 10-50 times a second, and it handed every
  // row a new `item` prop identity even when nothing about that row changed.
  //
  // `isLast` is decided at render time by comparing the row's index against
  // this one.
  const lastItemIndex = timelineItems.length - 1;

  // Build user-message layer index: for each item index, which user message index is its "layer header"
  const userMessageForItem = useMemo(() => {
    const mapping: (number | null)[] = [];
    let currentUserIdx: number | null = null;

    for (let i = 0; i < timelineItems.length; i++) {
      const item = timelineItems[i];
      if (item.type === "message" && item.message.role === MessageRole.USER) {
        currentUserIdx = i;
      }
      mapping.push(currentUserIdx);
    }
    return mapping;
  }, [timelineItems]);

  // Track visible range for pinned user message
  const [pinnedUserMessageIdx, setPinnedUserMessageIdx] = useState<number | null>(null);

  // The pinned header's height: the line a user message has to slide under
  // to be pinned, and the clearance a jump leaves above its target.
  //
  // Held while the header is hidden, and only ever raised. The decision must
  // not read a geometry the decision itself produces: a line that fell to 0
  // with the header gone made the first message look scrolled away the moment
  // the header left, and a shorter header (an attachment-only message) would
  // otherwise move the line and flip the very swap that showed it.
  const [measuredPinnedHeaderPx, setMeasuredPinnedHeaderPx] = useState<number | null>(null);
  const pinnedHeaderLine = measuredPinnedHeaderPx ?? PINNED_HEADER_ESTIMATED_PX;

  // --- Per-thread scroll position memory ---
  // Derive a stable key for the current thread filter
  const threadKey = useMemo(() => {
    if (!selectedThreads || selectedThreads.size === 0) return "__all__";
    return Array.from(selectedThreads).sort().join(",");
  }, [selectedThreads]);

  // --- Scrolling ---
  //
  // A native scroller, virtualized by @tanstack/react-virtual and anchored to
  // the end. The virtualizer is the ONLY writer of scrollTop while rows
  // change, and it corrects before paint — see useTimelineVirtualizer.ts for
  // why that replaced react-virtuoso.
  const getTimelineItemKey = useCallback(
    (index: number) => timelineItemKey(timelineItems[index], index),
    [timelineItems],
  );
  const timeline = useTimelineVirtualizer({
    count: timelineItems.length,
    getItemKey: getTimelineItemKey,
    paddingStart: TIMELINE_HEADER_PX,
    scrollPaddingStart: pinnedHeaderLine,
  });
  const { virtualizer, scrollToBottom, atBottom, scroller: scrollerEl } = timeline;
  const scrollerElRef = useRef<HTMLElement | null>(null);
  scrollerElRef.current = scrollerEl;
  // The pinned index the scroll handlers below read. Mirrored in a ref so the
  // per-scroll resolver does not re-subscribe on every change.
  const pinnedUserMessageIdxRef = useRef<number | null>(null);
  const setScrollerEl = timeline.scrollerRef;

  // Report at-bottom upward for the scroll-to-bottom button.
  useEffect(() => {
    onAtBottomStateChange?.(atBottom);
  }, [atBottom, onAtBottomStateChange]);

  // Expose "scroll to bottom and resume following" to the parent's button and
  // to the composer's send.
  useEffect(() => {
    onResumeFollow?.(scrollToBottom);
  }, [onResumeFollow, scrollToBottom]);

  // A thread switch rebuilds the list from different rows: start it at the
  // bottom, following, and drop the pin (its index named a row in the old
  // list, which is a different row — or no row — in this one).
  const previousThreadKeyRef = useRef(threadKey);
  useEffect(() => {
    if (previousThreadKeyRef.current === threadKey) return;
    previousThreadKeyRef.current = threadKey;
    pinnedUserMessageIdxRef.current = null;
    setPinnedUserMessageIdx(null);
    scrollToBottom();
  }, [threadKey, scrollToBottom]);

  // --- Pinned user-message header ---
  //
  // Resolved from measured row geometry, re-run on every scroll and whenever
  // the row-to-section mapping changes (rows inserted above shift indices) or
  // the header's measured height moves the line.
  const applyPinnedUserMessage = useCallback(() => {
    const scroller = scrollerElRef.current;
    if (!scroller) return;
    const nextPinned = resolvePinnedUserMessage({
      rows: measureRows(scroller),
      userMessageForItem,
      line: pinnedHeaderLine,
      previousPinned: pinnedUserMessageIdxRef.current,
      releaseHysteresisPx: PINNED_HEADER_RELEASE_HYSTERESIS_PX,
    });
    if (nextPinned !== pinnedUserMessageIdxRef.current) {
      pinnedUserMessageIdxRef.current = nextPinned;
      setPinnedUserMessageIdx(nextPinned);
    }
  }, [userMessageForItem, pinnedHeaderLine]);

  // Scroll-back paging: load the next page when the user nears the top.
  // Prepending needs no compensation here — the virtualizer is anchored to
  // the end, so rows prepended above hold the row being read in place.
  const LOAD_OLDER_WITHIN_PX = 400;
  const loadOlderStateRef = useRef({ hasOlderMessages, isLoadingOlderMessages, onLoadOlderMessages });
  loadOlderStateRef.current = { hasOlderMessages, isLoadingOlderMessages, onLoadOlderMessages };

  useEffect(() => {
    const scroller = scrollerEl;
    if (!scroller) return;
    let frame: number | null = null;
    const onScroll = () => {
      if (frame !== null) return;
      // One measurement per frame however many scroll events arrive.
      frame = requestAnimationFrame(() => {
        frame = null;
        applyPinnedUserMessage();
        const older = loadOlderStateRef.current;
        if (
          scroller.scrollTop < LOAD_OLDER_WITHIN_PX &&
          older.onLoadOlderMessages &&
          older.hasOlderMessages &&
          !older.isLoadingOlderMessages
        ) {
          older.onLoadOlderMessages();
        }
      });
    };
    scroller.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      scroller.removeEventListener("scroll", onScroll);
      if (frame !== null) cancelAnimationFrame(frame);
    };
  }, [applyPinnedUserMessage, scrollerEl]);

  // Re-resolve the pin when the mapping or the line changes, not only on
  // scroll: a reply arriving while you sit still inserts rows and shifts every
  // index.
  useEffect(() => {
    applyPinnedUserMessage();
  }, [applyPinnedUserMessage]);

  // Jumps go through the virtualizer, not the DOM: the target row is usually
  // not rendered. Moving off the end is itself what stops following.
  const handleJumpToPinned = useCallback(() => {
    if (pinnedUserMessageIdx === null) return;
    virtualizer.scrollToIndex(pinnedUserMessageIdx, { align: "start", behavior: "auto" });
  }, [pinnedUserMessageIdx, virtualizer]);

  // Get the pinned user message data
  const pinnedMessage = pinnedUserMessageIdx !== null ? timelineItems[pinnedUserMessageIdx] : null;
  const pinnedUserMsg = pinnedMessage?.type === "message" ? pinnedMessage.message : null;

  // Jump to a specific message, e.g. from a chat-search hit. The target is
  // usually not rendered, so the jump is by index through the virtualizer.
  const [highlightedMessageId, setHighlightedMessageId] = useState<string | null>(null);

  useEffect(() => {
    const handleScrollToMessage = (event: Event) => {
      const messageId = (event as CustomEvent<{ messageId?: string }>).detail?.messageId;
      if (!messageId) return;
      const index = timelineItems.findIndex(
        (item) => item.type === "message" && item.message.id === messageId,
      );
      // Not in the loaded window — the message lives in a page we have not
      // fetched yet, so there is nothing to scroll to.
      if (index === -1) return;
      virtualizer.scrollToIndex(index, { align: "center", behavior: "auto" });
      setHighlightedMessageId(messageId);
      // Tell the requester to stop retrying; it cannot observe this otherwise.
      acknowledgeScrollToMessage(messageId);
    };

    window.addEventListener("scroll-to-message", handleScrollToMessage);
    return () => window.removeEventListener("scroll-to-message", handleScrollToMessage);
  }, [timelineItems, virtualizer]);

  // Clear the highlight once it has had time to register visually.
  useEffect(() => {
    if (!highlightedMessageId) return;
    const timer = setTimeout(() => setHighlightedMessageId(null), 2000);
    return () => clearTimeout(timer);
  }, [highlightedMessageId]);

  // "Focus the conversation" — hands the transcript keyboard focus so it can
  // be scrolled and read without the mouse.
  useEffect(() => {
    const handleFocusTranscript = () => {
      const scroller = scrollerElRef.current;
      if (!scroller) return;
      scroller.focus({ preventScroll: true });
    };
    window.addEventListener("focus-transcript", handleFocusTranscript);
    return () => window.removeEventListener("focus-transcript", handleFocusTranscript);
  }, []);

  const timelineContainerRef = useRef<HTMLDivElement>(null);

  // Publish the pinned header's height: per-message hover toolbars stick below
  // it, and it is the crossing line the pin resolver measures against.
  //
  // The CSS variable tracks the header as it is — toolbars stick at the very
  // top when there is no header. The resolver's line keeps the tallest height
  // seen (see measuredPinnedHeaderPx). A 0 is a pane not laid out, not a
  // header with no height, so it never lowers anything.
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
      const height = header.offsetHeight;
      shell.style.setProperty("--chat-pinned-header-h", `${height}px`);
      if (height > 0) {
        setMeasuredPinnedHeaderPx((tallest) =>
          tallest === null || height > tallest ? height : tallest,
        );
      }
    };
    publish();

    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(publish);
    observer.observe(header);
    return () => observer.disconnect();
  }, [pinnedUserMsg]);

  // `index` is a plain index into timelineItems.
  const renderItem = useCallback((index: number, item: TimelineItem, compactToolSpacing = false) => {
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

    if (item.type === "error") {
      return <GroupedErrorRow error={item.error} errors={item.errors} />;
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
    const isLastItem = index === lastItemIndex;

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
          // Colors only — never transition-all. The virtualizer measures row
          // heights with a ResizeObserver, so an animated height reports a new value
          // every frame of the animation and each one triggers a re-measure
          // and a follow-scroll correction.
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
            compactToolSpacing={compactToolSpacing}
          />
        )}
      </div>
    );
  }, [approvals, chatId, isStreaming, lastItemIndex, onSelectThread, timelineVariant]);

  // Wrap each row in the padding/max-width container.
  // One virtual row: absolutely positioned at its measured offset, and handed
  // to the virtualizer to measure. The ref is called for every rendered row;
  // the virtualizer watches it with a ResizeObserver from then on.
  const measureRow = virtualizer.measureElement;
  const renderRow = useCallback((index: number, item: TimelineItem) => {
    // After a search jump, briefly ring the target so the eye can find it —
    // landing mid-conversation with no cue makes the jump feel like it failed.
    const isHighlighted =
      highlightedMessageId !== null &&
      item.type === "message" &&
      item.message.id === highlightedMessageId;

    const rowKey = timelineItemKey(item, index);
    const toolRowSpacing = getToolRowSpacing(timelineItems, index);

    return (
      <div
        key={rowKey}
        className={cn(
          "absolute inset-x-0 top-0",
          timelineHorizontalPaddingClass,
          timelineGapClass,
          toolRowSpacing.compactBefore && "pt-0.5",
          toolRowSpacing.compactAfter && "pb-0.5",
        )}
        // No `transform` in JSX: with directDomUpdates the virtualizer writes
        // each row's position in a layout effect after every render (before
        // paint), and caches what it wrote. A JSX transform would be re-applied
        // by React on re-render while that cache believes the DOM is current,
        // leaving the row at a stale offset until the next range change.
        // The virtualizer reads this to map a measured element to its index.
        data-index={index}
        // The pinned header resolves from measured row geometry and needs each
        // row's index into timelineItems.
        {...{ [TIMELINE_ROW_INDEX_ATTR]: index }}
        ref={measureRow}
      >
        <div
          className={cn(
            contentMaxWidthClass,
            "mx-auto",
            isHighlighted &&
              "rounded-lg ring-2 ring-primary/60 transition-shadow duration-500",
          )}
        >
          {renderItem(index, item, toolRowSpacing.compact)}
        </div>
      </div>
    );
  }, [contentMaxWidthClass, highlightedMessageId, measureRow, renderItem, timelineGapClass, timelineHorizontalPaddingClass, timelineItems]);

  if (timelineItems.length === 0) {
    return (
      <div className="p-8 text-center text-muted-foreground">No messages yet</div>
    );
  }

  return (
    <div
      ref={timelineContainerRef}
      className={timelineShellClass}
      style={{ position: "relative" }}
    >
      {/* Pinned user message overlay.

          pointer-events-none on the overlay is what keeps text selection
          sane. It is absolutely positioned over the transcript and comes
          EARLIER in the DOM than every row, so a drag-select that crossed it
          used to resolve its focus to a point before row 0 — selecting the
          entire conversation above the anchor, including rows scrolled out of
          view. With hit-testing passing through, a drag over it extends the
          selection to the text underneath instead. The Jump-to button opts
          back in, so it stays clickable. */}
      {pinnedUserMsg && (
        <div
          ref={pinnedHeaderRef}
          data-testid="pinned-user-message-header"
          // Fully opaque and elevated: the timeline scrolls underneath, so any
          // translucency here would let message text show through the gaps
          // around the floating bubble.
          className="group/pinned pointer-events-none absolute inset-x-0 top-0 z-50 border-b border-border/60 bg-background pt-1 pb-1.5 shadow-md"
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
                    "pointer-events-auto flex h-6 w-6 shrink-0 items-center justify-center rounded-full border border-primary-foreground/20 bg-primary/90 text-primary-foreground shadow-sm transition-[color,background-color,opacity] duration-200 hover:bg-primary",
                    // The overlay no longer receives hover, so the button
                    // reveals on its own hover and on keyboard focus rather
                    // than on hovering the header.
                    "opacity-40 hover:opacity-100 focus-visible:opacity-100"
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
      <div
        ref={setScrollerEl}
        className="h-full overflow-y-auto"
        // "contain": the end-of-list bounce stays (it is macOS's native
        // rubber-band; its length is set by the OS, not here), but a scroll
        // past either end never chains to the page behind the transcript.
        style={{ overscrollBehavior: "contain" }}
        // Focus target for "focus the conversation" — makes the transcript
        // keyboard-scrollable (arrows, PageUp/Down, Home/End) without stealing
        // those keys from anywhere else, since they only apply while focused.
        data-context="transcript"
        tabIndex={-1}
      >
        {/* The virtual space: as tall as every row measured or estimated,
            with only the rows near the viewport actually rendered. */}
        {/* Height is written by the virtualizer (directDomUpdates), not JSX. */}
        <div ref={virtualizer.containerRef} className="relative w-full">
          {/* Top of the transcript doubles as the scroll-back loading
              indicator, in the space paddingStart reserves above row 0.
              Fixed height in both states, so the indicator toggling never
              moves the rows below it. */}
          <div
            className="absolute inset-x-0 top-0 flex items-center justify-center gap-2 text-xs text-muted-foreground"
            style={{ height: TIMELINE_HEADER_PX }}
          >
            {isLoadingOlderMessages && (
              <>
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                <span>Loading earlier messages…</span>
              </>
            )}
          </div>
          {virtualizer
            .getVirtualItems()
            .map((virtualRow) => renderRow(virtualRow.index, timelineItems[virtualRow.index]))}
          {/* The footer sits in the paddingEnd the virtualizer reserves for
              it, so "the end" it pins to includes the footer. Anchored to the
              container's bottom edge, which the virtualizer sizes directly,
              so it moves with the total without a re-render. */}
          <div ref={timeline.footerRef} className="absolute inset-x-0 bottom-0">
            {footer ? (
              <div className={cn(timelineHorizontalPaddingClass, "pb-3")}>
                <div className={cn(contentMaxWidthClass, "mx-auto")}>{footer}</div>
              </div>
            ) : (
              <div className="pb-10" />
            )}
          </div>
        </div>
      </div>
    </div>
  );
});
