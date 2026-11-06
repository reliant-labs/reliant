/**
 * Simplified chat thread: the presentation-only pass that folds one agent
 * turn's work into a single row.
 *
 * One agent turn is MANY assistant messages (one per LLM call), and each of
 * them interleaves narration with tool runs. The detailed timeline renders
 * every one, so a long turn reads as a stack of tool boxes with prose between
 * them. Popular agent UIs (ChatGPT "Worked for 7m", Cursor "Explored 6 files",
 * Codex "Worked for 4m 12s") instead show: the user message, ONE fold for the
 * turn's work, and the final answer. This module computes that shape.
 *
 * Presentation only, like transcriptPresentation.ts: it never clones or
 * mutates a message. A row names a message plus the indexes of the
 * `ProcessedMessage.segments` it renders (a MessageSlice).
 */

import type { Message } from "../../../gen/reliant/v1/chat_pb";
import { MessageRole } from "../../../gen/reliant/v1/chat_pb";
import { getProcessedMessage } from "../../../lib/messageProcessor";
import type { MessageSegment, ToolResultsByCallId } from "../../../lib/messageProcessor";
import type { Approval } from "../../../gen/reliant/v1/approval_pb";

/**
 * How a sliced ChatMessage renders the segments it was given.
 *
 * - `answer`: the turn's reply — prose, attachments and message actions; no
 *   tool rows.
 * - `work`: the fold's expanded body — narration and tool rows, without the
 *   avatar/actions chrome.
 * - `work-surfaced`: what must stay visible while the fold is COLLAPSED — the
 *   user cards (ask_user, request_machine, draft automation), tool calls
 *   awaiting approval, and generated images/videos. Renders nothing when the
 *   slice has none.
 */
export type MessageSliceRole = "answer" | "work" | "work-surfaced";

/** Which of a message's segments a row renders, and how. */
export interface MessageSlice {
  /** Indexes into `getProcessedMessage(message).segments`, ascending. */
  segmentIndexes: readonly number[];
  role: MessageSliceRole;
}

/** One turn's folded work: every segment of the turn except its answer. */
export interface TurnWork {
  /**
   * `work-<first assistant message id of the turn>`. Stable while the turn
   * streams — the virtualizer re-finds rows by key, so a key that changed as
   * messages arrived would remount the fold and jump the scroll.
   */
  key: string;
  thread: string;
  /** The turn's work, in render order; each entry is one message's slice. */
  parts: ReadonlyArray<{ message: Message; segmentIndexes: readonly number[] }>;
}

/** The subset of an InterleavedTimeline item this pass reads. */
export type SimplifiableItem =
  | { type: "message"; message: Message; workflow: { thread: string } }
  | { type: Exclude<string, "message"> };

/** A message row annotated with the slice it renders (answers only). */
export type SlicedMessageItem<T> = T & { slice?: MessageSlice };

/** The fold row inserted for a turn that did any work. */
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

function isBlankText(segment: MessageSegment): boolean {
  return segment.kind === "text" && segment.text.trim() === "";
}

/**
 * Folds each agent turn of `items` into one WorkItem followed by the turn's
 * answer rows. Single linear pass; rows the pass does not change (non-message
 * items, turns that never ran a tool) are returned as the same objects.
 *
 * A turn is a maximal run of consecutive assistant message items on one thread
 * with no displayStyle; any other item (user message, thread-start, handoff,
 * error, info, run_output, a displayStyle message, another thread's message)
 * ends it.
 *
 * `isLiveTurn` is asked about a turn's last message only when the turn ends on
 * a tools segment: while live there is no answer yet; otherwise the last
 * narration is promoted to the answer so a finished turn never ends mute.
 */
export function buildSimplifiedTimeline<I extends SimplifiableItem, W>(
  items: readonly I[],
  opts: {
    segmentsOf: SegmentReader;
    isLiveTurn: (lastMessage: Message) => boolean;
    workflowOf: (item: I & { type: "message" }) => W;
  },
): Array<SlicedMessageItem<I> | WorkItem<W>> {
  const { segmentsOf, isLiveTurn, workflowOf } = opts;
  const out: Array<SlicedMessageItem<I> | WorkItem<W>> = [];

  const flush = (start: number, end: number) => {
    if (start >= end) return;

    // Locate the turn's last tools segment.
    let lastM = -1;
    let lastS = -1;
    for (let i = end - 1; i >= start && lastM < 0; i--) {
      const segs = segmentsOf((items[i] as { message: Message }).message);
      for (let s = segs.length - 1; s >= 0; s--) {
        if (segs[s].kind === "tools") {
          lastM = i;
          lastS = s;
          break;
        }
      }
    }
    if (lastM < 0) {
      for (let i = start; i < end; i++) out.push(items[i]);
      return;
    }

    // Answer = non-blank text after the last tools segment.
    let hasAnswer = false;
    for (let i = lastM; i < end && !hasAnswer; i++) {
      const segs = segmentsOf((items[i] as { message: Message }).message);
      for (let s = i === lastM ? lastS + 1 : 0; s < segs.length; s++) {
        if (segs[s].kind === "text" && !isBlankText(segs[s])) {
          hasAnswer = true;
          break;
        }
      }
    }

    // Turn ended on tools and is no longer live: promote the last narration.
    let promoteM = -1;
    let promoteS = -1;
    if (!hasAnswer && !isLiveTurn((items[end - 1] as { message: Message }).message)) {
      for (let i = lastM; i >= start && promoteM < 0; i--) {
        const segs = segmentsOf((items[i] as { message: Message }).message);
        for (let s = (i === lastM ? lastS : segs.length) - 1; s >= 0; s--) {
          if (segs[s].kind === "text" && !isBlankText(segs[s])) {
            promoteM = i;
            promoteS = s;
            break;
          }
        }
      }
    }

    const parts: Array<{ message: Message; segmentIndexes: readonly number[] }> = [];
    const answers: Array<SlicedMessageItem<I>> = [];
    for (let i = start; i < end; i++) {
      const item = items[i] as I & { type: "message" };
      const message = (item as { message: Message }).message;
      const segs = segmentsOf(message);
      let work: number[] | null = null;
      let answer: number[] | null = null;
      for (let s = 0; s < segs.length; s++) {
        const afterTools = i > lastM || (i === lastM && s > lastS);
        if (i === promoteM && s === promoteS) {
          (answer ??= []).push(s);
        } else if (!afterTools) {
          (work ??= []).push(s);
        } else if (!isBlankText(segs[s])) {
          (answer ??= []).push(s);
        }
      }
      if (work) parts.push({ message, segmentIndexes: work });
      if (answer) {
        answers.push({ ...item, slice: { segmentIndexes: answer, role: "answer" } });
      } else if (message.attachments?.length) {
        // Attachments live on the answer chrome; keep them visible.
        answers.push({ ...item, slice: { segmentIndexes: NO_INDEXES, role: "answer" } });
      }
    }

    const first = items[start] as I & { type: "message" };
    out.push({
      type: "work",
      work: {
        key: `work-${(first as { message: Message }).message.id}`,
        thread: (first as { workflow: { thread: string } }).workflow.thread,
        parts,
      },
      workflow: workflowOf(first),
    });
    for (const a of answers) out.push(a);
  };

  let start = 0; // first index of the open turn
  let end = 0; // exclusive end of the open turn (end === start: none open)
  let thread = "";
  for (let i = 0; i < items.length; i++) {
    const item = items[i];
    if (item.type === "message") {
      const m = item as { message: Message; workflow: { thread: string } };
      if (m.message.role === MessageRole.ASSISTANT && !m.message.displayStyle) {
        if (end > start && m.workflow.thread !== thread) {
          flush(start, end);
          start = i;
        } else if (end === start) {
          start = i;
        }
        thread = m.workflow.thread;
        end = i + 1;
        continue;
      }
    }
    flush(start, end);
    out.push(item);
    start = end = i + 1;
  }
  flush(start, end);
  return out;
}
