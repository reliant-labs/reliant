/**
 * Simplified chat thread: the presentation-only pass that folds runs of
 * non-text work into single rows.
 *
 * The simplified thread is the detailed transcript with every maximal run of
 * work — tool calls, and the routine agent mail that arrives between them —
 * folded into one "Activity" row. Assistant prose is never folded: it is the
 * agent talking to the user. So a message [text, tools, text] renders as
 * prose / fold / prose, and a turn of many LLM calls reads as its narration
 * with a fold wherever the agent went quiet and worked.
 *
 * A tool call the user must see — a question, an approval, a generated image,
 * a deploy — is a boundary just like prose: [tool, ask_user, tool] renders as
 * fold / card / fold. Nothing that stays on screen is ever collected under a
 * fold's header, away from where it happened.
 *
 * Presentation only, like transcriptPresentation.ts: it never clones or
 * mutates a message. A row names a message plus the indexes of the
 * `ProcessedMessage.segments` it renders (a MessageSlice).
 */

import type { Message } from "../../../gen/reliant/v1/chat_pb";
import { AgentMessageKind, ContentBlockType, DisplayStyle, MessageRole } from "../../../gen/reliant/v1/chat_pb";
import { getProcessedMessage } from "../../../lib/messageProcessor";
import type { MessageSegment, ToolExecution, ToolResultsByCallId } from "../../../lib/messageProcessor";
import { toolBaseName } from "../../../lib/toolAction";
import type { Approval } from "../../../gen/reliant/v1/approval_pb";

/**
 * How a sliced ChatMessage renders the segments it was given.
 *
 * - `prose`: assistant text, as a message row. With `chrome` it also carries
 *   the message's timestamp, actions and attachments — set on a message's LAST
 *   prose slice only, so a message cut in three shows them once.
 * - `work`: a part of a fold's expanded body — tool rows and their media,
 *   without the message chrome.
 * - `surfaced`: tool calls that stay on screen as a row of their own (see
 *   SurfacedExecutionPredicate), rendered as the detailed transcript renders
 *   them — card, approval buttons, image — without the message chrome.
 */
export type MessageSliceRole = "prose" | "work" | "surfaced";

/** Which of a message's segments a row renders, and how. */
export interface MessageSlice {
  /** Indexes into `getProcessedMessage(message).segments`, ascending. */
  segmentIndexes: readonly number[];
  role: MessageSliceRole;
  /** `prose` only: this row carries the message's chrome. */
  chrome?: boolean;
  /**
   * The tool calls (by call id) the row renders of its tools segments, when
   * not all of them: a surfaced call cuts its segment into fold / call / fold.
   * Absent: every call of every listed segment.
   */
  callIds?: readonly string[];
}

/**
 * Whether a tool call stays on screen as its own row, at its place, rather
 * than inside a fold. Given the call as the segments carry it; a caller that
 * judges by result or approval joins those in itself.
 */
export type SurfacedExecutionPredicate = (execution: ToolExecution) => boolean;

const NEVER_SURFACED: SurfacedExecutionPredicate = () => false;

/** One folded run of work: tool segments and the agent mail between them. */
export interface TurnWork {
  /**
   * `work-<message id>-<segment index>` of the run's first tool segment
   * (`…:<call id>` when the run opens after a surfaced call of that segment),
   * or `work-<message id>` when the run opens on mail. Stable while the run grows
   * at its end — the virtualizer re-finds rows by key, so a key that changed
   * as messages arrived would remount the fold and jump the scroll.
   */
  key: string;
  thread: string;
  /**
   * The run's tool work, in render order; each entry is one message's slice.
   * `callIds` as in MessageSlice: set when a surfaced call cut a segment.
   */
  parts: ReadonlyArray<{ message: Message; segmentIndexes: readonly number[]; callIds?: readonly string[] }>;
  /** Agent mail and routine notices, retained in full in the expanded body.
   * afterPart is the number of parts preceding the message (0 means before all).
   * The original message supplies its timestamp and navigation metadata.
   */
  events?: ReadonlyArray<{ type: "message"; message: Message; afterPart: number }>;
}

/** The subset of an InterleavedTimeline item this pass reads. */
export type SimplifiableItem =
  | { type: "message"; message: Message; workflow: { thread: string } }
  | { type: Exclude<string, "message"> };

/**
 * A message row, annotated with the slice it renders. `key` is set when the
 * row is not the message's first: a message can now yield several rows, and
 * each needs a row key of its own.
 */
export type SlicedMessageItem<T> = T & { slice?: MessageSlice; key?: string };

/** The fold row inserted for a run of work. */
export interface WorkItem<W> {
  type: "work";
  work: TurnWork;
  workflow: W;
}

/** Reads a message's ordered segments (callers pass a memoized reader). */
export type SegmentReader = (message: Message) => readonly MessageSegment[];

// Segment kinds depend only on the message, never on results/approvals, so the
// reader passes the same constant empties every time: getProcessedMessage's
// WeakMap memo is keyed on those refs, and fresh `{}`/`[]` per call would miss.
const EMPTY_RESULTS: ToolResultsByCallId = {};
const EMPTY_APPROVALS: Approval[] = [];

/** A SegmentReader backed by getProcessedMessage's per-message memo. */
export function createSegmentReader(): SegmentReader {
  return (message) => getProcessedMessage(message, EMPTY_RESULTS, EMPTY_APPROVALS).segments;
}

const NO_INDEXES: readonly number[] = [];

/**
 * Tools whose media IS the result the user asked for. Their calls stay on
 * screen as rows of their own; any other tool's media (a browser screenshot,
 * a viewed image file) is a record of work and stays in the fold.
 */
const SURFACED_MEDIA_TOOLS = new Set(["generate_image", "generate_video"]);

export function isSurfacedMediaTool(toolName: string): boolean {
  return SURFACED_MEDIA_TOOLS.has(toolBaseName(toolName).toLowerCase());
}

type ActivityMessage = Pick<Message, "role" | "displayStyle" | "attachments" | "sender"> & {
  contentBlocks: ReadonlyArray<{ type: ContentBlockType; content?: string }>;
};

/** Only successful machine-classified outcomes are routine. */
export function isRoutineActivityMessage(message: ActivityMessage): boolean {
  if (message.displayStyle === DisplayStyle.WARNING || (message.attachments?.length ?? 0) > 0) return false;
  if (message.sender) {
    return Boolean(message.sender.threadId) && message.sender.kind === AgentMessageKind.COMPLETION;
  }
  // Older drain_agent_messages writes SYSTEM+INFO without sender metadata.
  // Match its entire Go %q notification grammar, never arbitrary completion prose.
  if (message.role !== MessageRole.SYSTEM || message.displayStyle !== DisplayStyle.INFO) return false;
  if (message.contentBlocks?.length !== 1 || message.contentBlocks[0].type !== ContentBlockType.TEXT) return false;
  return /^spawn "(?:[^"\\\n]|\\.)*" completed$/.test(message.contentBlocks[0].content ?? "");
}

/**
 * Messages a fold may absorb: routine completions, and mail from another
 * agent or run (spawn_send, send_to_run). Each stays readable, in order, in
 * the expanded body; an orchestrator receiving many reports reads as one fold
 * rather than a column of alternating folds and mail.
 *
 * What asks for attention stays its own row: a warning, mail carrying
 * attachments, and a sub-agent's failure or cancellation. The human's own
 * messages carry no sender and never fold.
 */
export function isFoldableActivityMessage(message: ActivityMessage): boolean {
  if (isRoutineActivityMessage(message)) return true;
  if (!message.sender?.threadId) return false;
  if (message.displayStyle === DisplayStyle.WARNING || (message.attachments?.length ?? 0) > 0) return false;
  return message.sender.kind !== AgentMessageKind.FAILED && message.sender.kind !== AgentMessageKind.CANCELLED;
}

function isBlankText(segment: MessageSegment): boolean {
  return segment.kind === "text" && segment.text.trim() === "";
}

/**
 * Rewrites `items` for the simplified thread in one linear pass. Rows the pass
 * does not change (non-message items, text-only messages, boundaries) are
 * returned as the same objects.
 *
 * A work run is a maximal sequence, on one thread, of tool calls, blank
 * text, and foldable activity messages. Non-blank assistant text, a surfaced
 * tool call, a human message, errors, info, run outputs, thread changes,
 * warnings and anything else closes it. A run becomes a fold when it ran a tool or holds at least
 * two activity messages; a lone message with no tool work stays its own row.
 */
export function buildSimplifiedTimeline<I extends SimplifiableItem, W>(
  items: readonly I[],
  opts: {
    segmentsOf: SegmentReader;
    workflowOf: (item: I & { type: "message" }) => W;
    /**
     * A tool call that stays on screen as its own row (see
     * SurfacedExecutionPredicate). Default: none.
     */
    isSurfaced?: SurfacedExecutionPredicate;
  },
): Array<SlicedMessageItem<I> | WorkItem<W>> {
  const { segmentsOf, workflowOf, isSurfaced = NEVER_SURFACED } = opts;
  const out: Array<SlicedMessageItem<I> | WorkItem<W>> = [];

  type Part = {
    message: Message;
    segments: readonly MessageSegment[];
    segmentIndexes: number[];
    callIds: string[];
  };
  type Event = { type: "message"; message: Message; afterPart: number };
  type Run = {
    key: string;
    thread: string;
    workflow: W;
    parts: Part[];
    events: Event[];
    eventItems: I[];
    tools: number;
  };
  // The open run. Nothing is pushed to `out` while it is open — whatever would
  // be closes it first — so the run's row is pushed, in place, on close.
  // (Cast, not annotated: the helpers below reassign it, which TypeScript's
  // narrowing-by-assignment would not see.)
  let run = null as Run | null;

  const close = () => {
    if (!run) return;
    const r = run;
    run = null;
    if (r.tools > 0 || r.events.length >= 2) {
      out.push({
        type: "work",
        work: {
          key: r.key,
          thread: r.thread,
          parts: r.parts.map(toTurnWorkPart),
          ...(r.events.length ? { events: r.events } : {}),
        },
        workflow: r.workflow,
      });
      return;
    }
    // Not worth a fold: the lone message keeps its own row. Any parts are
    // blank text, which renders nothing.
    for (const item of r.eventItems) out.push(item);
  };

  const open = (key: string, thread: string, item: I & { type: "message" }): Run => {
    const opened: Run = { key, thread, workflow: workflowOf(item), parts: [], events: [], eventItems: [], tools: 0 };
    run = opened;
    return opened;
  };

  // A part names whole segments, plus the calls it holds when a surfaced call
  // cut one of its tools segments.
  const toTurnWorkPart = ({ message, segments, segmentIndexes, callIds }: Part): TurnWork["parts"][number] => {
    const held = new Set(callIds);
    const partial = segmentIndexes.some((s) => {
      const segment = segments[s];
      return segment.kind === "tools" && segment.executions.some((execution) => !held.has(execution.call.id));
    });
    return partial ? { message, segmentIndexes, callIds } : { message, segmentIndexes };
  };

  /** Adds segment `s` of `message` to the open run: one call of it, or blank text. */
  const addWork = (message: Message, segments: readonly MessageSegment[], s: number, callId?: string) => {
    if (!run) return;
    let last = run.parts[run.parts.length - 1];
    // A message's work segments are contiguous within a run (its prose or a
    // surfaced call would have closed the run), so the last part is this
    // message's if any is.
    if (last?.message !== message) {
      last = { message, segments, segmentIndexes: [], callIds: [] };
      run.parts.push(last);
    }
    if (last.segmentIndexes[last.segmentIndexes.length - 1] !== s) last.segmentIndexes.push(s);
    if (callId !== undefined) {
      last.callIds.push(callId);
      run.tools += 1;
    }
  };

  // The latest surfaced-call row. Adjacent surfaced calls of one tools
  // segment share it while it is still the last row out.
  let surfaced = null as { row: SlicedMessageItem<I>; message: Message; s: number; callIds: string[] } | null;

  for (const item of items) {
    if (item.type !== "message") {
      close();
      out.push(item);
      continue;
    }
    const messageItem = item as I & { type: "message"; message: Message; workflow: { thread: string } };
    const { message } = messageItem;
    const thread = messageItem.workflow.thread;

    if (isFoldableActivityMessage(message)) {
      // The timeline workflow can be an aggregate parent while the message
      // still names a different thread; never fold that cross-thread notice.
      if (!message.thread || message.thread === thread) {
        if (run && run.thread !== thread) close();
        const r = run ?? open(`work-${message.id}`, thread, messageItem);
        r.events.push({ type: "message", message, afterPart: r.parts.length });
        r.eventItems.push(item);
        continue;
      }
      close();
      out.push(item);
      continue;
    }

    if (message.role !== MessageRole.ASSISTANT || message.displayStyle) {
      close();
      out.push(item);
      continue;
    }

    const segments = segmentsOf(message);
    const hasAttachments = (message.attachments?.length ?? 0) > 0;

    if (!segments.some((segment) => segment.kind === "tools")) {
      // A blank assistant message (an LLM call that has not said anything
      // yet) joins the open run, so a live fold stays the last row between
      // calls. Anything else here is prose: the whole message, as detailed.
      if (run && run.thread === thread && !hasAttachments && segments.every(isBlankText)) {
        segments.forEach((_, s) => addWork(message, segments, s));
        continue;
      }
      close();
      out.push(item);
      continue;
    }

    // A message with tool work: cut it into prose slices, work, and the
    // surfaced calls' rows, each at its place.
    type ProseSlice = { segmentIndexes: number[]; role: "prose"; chrome?: boolean };
    let prose = null as ProseSlice | null;
    let lastProse = null as ProseSlice | null;
    for (let s = 0; s < segments.length; s++) {
      const segment = segments[s];
      if (segment.kind === "tools") {
        prose = null;
        segment.executions.forEach((execution, e) => {
          const callId = execution.call.id;
          if (isSurfaced(execution)) {
            // A boundary, like prose: the run before it folds on its own.
            close();
            if (surfaced && surfaced.message === message && surfaced.s === s && out[out.length - 1] === surfaced.row) {
              surfaced.callIds.push(callId);
              return;
            }
            const callIds = [callId];
            const row: SlicedMessageItem<I> = {
              ...item,
              slice: { segmentIndexes: [s], role: "surfaced", callIds },
              key: `${message.id}:${s}:${callId}`,
            };
            out.push(row);
            surfaced = { row, message, s, callIds };
            return;
          }
          if (run && run.thread !== thread) close();
          // A run opening mid-segment (after a surfaced call) names the call,
          // so the folds either side of that call keep distinct keys.
          if (!run) open(e === 0 ? `work-${message.id}-${s}` : `work-${message.id}-${s}:${callId}`, thread, messageItem);
          addWork(message, segments, s, callId);
        });
      } else if (isBlankText(segment)) {
        if (prose) prose.segmentIndexes.push(s);
        else if (run && run.thread === thread) addWork(message, segments, s);
        // Otherwise there is nothing to attach it to, and it renders nothing.
      } else if (prose) {
        prose.segmentIndexes.push(s);
      } else {
        close();
        prose = { segmentIndexes: [s], role: "prose" };
        // The message's first row keeps its plain key, so a text-only
        // message that starts calling tools mid-stream keeps its row.
        out.push(lastProse
          ? { ...item, slice: prose, key: `${message.id}:${s}` }
          : { ...item, slice: prose });
        lastProse = prose;
      }
    }
    if (lastProse) {
      lastProse.chrome = true;
    } else if (hasAttachments) {
      // Attachments live on the message chrome; keep them visible.
      close();
      out.push({ ...item, slice: { segmentIndexes: NO_INDEXES, role: "prose", chrome: true } });
    }
  }
  close();
  return out;
}
