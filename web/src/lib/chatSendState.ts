/**
 * What happens if the user sends a message right now?
 *
 * The rules for "can I type into this chat" were implicit: spread across
 * composer disable logic, the busy checks in `chatStore.selectChat`, and
 * assorted `activity === RUNNING` comparisons. That works when one composer
 * implements it, but the mobile surface adds a second composer and an
 * embeddable widget will add a third — three places reconstructing the same
 * semantics from scratch is how they drift.
 *
 * This module answers the question once, as a pure function over chat state,
 * with no store, transport, or React dependency. That makes it directly
 * testable, shareable with a native shell, and safe for an embed to call.
 *
 * It intentionally returns an *action* rather than a boolean. "Can't send"
 * and "send will queue" and "send will resume a dead workflow" are different
 * outcomes that want different UI, and collapsing them into `disabled` is
 * what forces each surface to re-derive the nuance.
 */

import { ChatActivity } from "../gen/reliant/v1/chat_pb";

/** What a send would actually do. */
export type SendAction =
  /** Normal path — deliver to a live, idle workflow. */
  | "send"
  /** Workflow is mid-turn. Accept the text and deliver when it yields. */
  | "queue"
  /** Workflow is gone or errored; sending must recover it first. */
  | "resume"
  /** Sending is not possible right now. */
  | "blocked";

export interface SendState {
  action: SendAction;
  /** Whether the composer should accept input at all. */
  canType: boolean;
  /**
   * Short, user-facing explanation. Present whenever the action is not a
   * plain `send`; null otherwise so callers can render nothing.
   */
  reason: string | null;
}

/**
 * The subset of chat state that determines send behavior.
 *
 * Deliberately a narrow structural type rather than the full `Chat`: it keeps
 * the function callable from a list row (which may only have a summary),
 * from the detail view, and from an embed that models chats differently.
 */
export interface SendStateInput {
  /** Current activity, from the activity store or a chat summary. */
  activity: ChatActivity;
  /**
   * Backend detected the durable workflow is missing (e.g. the Temporal
   * workflow was lost while the DB still said RUNNING).
   */
  needsRecovery?: boolean;
  /** Chat is archived — read-only. */
  isArchived?: boolean;
  /**
   * Whether a daemon is currently available to execute work. When false,
   * messages can still be composed but cannot execute.
   */
  hasActiveDaemon?: boolean;
}

/**
 * Decide what sending would do.
 *
 * Order matters — the checks run most-fatal first, so a chat that is both
 * archived and needs recovery reports the archive (the condition the user
 * must resolve first, and the one recovery can't fix).
 */
export function getSendState(input: SendStateInput): SendState {
  const {
    activity,
    needsRecovery = false,
    isArchived = false,
    hasActiveDaemon = true,
  } = input;

  // Archived chats are read-only regardless of workflow state.
  if (isArchived) {
    return {
      action: "blocked",
      canType: false,
      reason: "This chat is archived",
    };
  }

  // Recovery outranks activity: the stored activity is exactly what we've
  // learned not to trust when needsRecovery is set (DB says RUNNING, the
  // durable workflow is gone). Checking activity first would report "queue"
  // for a workflow that will never drain the queue.
  if (needsRecovery || activity === ChatActivity.ERROR) {
    return {
      action: "resume",
      canType: true,
      reason: "Workflow stopped — sending will restart it",
    };
  }

  // No daemon means nothing can execute. Composing is still allowed so the
  // user can write while a daemon spins up, but the surface should say so.
  if (!hasActiveDaemon) {
    return {
      action: "blocked",
      canType: true,
      reason: "No active daemon — start one to run this chat",
    };
  }

  switch (activity) {
    case ChatActivity.RUNNING:
      // Mid-turn. The backend accepts the message and delivers it when the
      // current turn yields, so this is a real send, not a rejection.
      return {
        action: "queue",
        canType: true,
        reason: "Agent is working — your message will be queued",
      };

    case ChatActivity.AWAITING_INPUT:
      // The workflow is blocked *on the user*. This is the highest-priority
      // send there is: it unblocks a stalled run.
      return { action: "send", canType: true, reason: null };

    case ChatActivity.PAUSED:
      return {
        action: "resume",
        canType: true,
        reason: "Chat is paused — sending will resume it",
      };

    case ChatActivity.IDLE:
    default:
      return { action: "send", canType: true, reason: null };
  }
}

/**
 * Whether the chat is doing work right now.
 *
 * Distinct from `getSendState().action === 'queue'` — AWAITING_INPUT is busy
 * from the workflow's perspective (a run is open) but is precisely when the
 * user *should* type. Spinners want this; composers want `getSendState`.
 */
export function isChatBusy(input: SendStateInput): boolean {
  if (input.needsRecovery) return false;
  return (
    input.activity === ChatActivity.RUNNING ||
    input.activity === ChatActivity.AWAITING_INPUT
  );
}

/**
 * Whether the chat is blocked waiting on this user — the signal worth a badge
 * in a list, a push notification, or an "action needed" filter. This is the
 * single most valuable thing a mobile surface can surface.
 */
export function needsUserAttention(input: SendStateInput): boolean {
  if (input.isArchived) return false;
  return (
    input.activity === ChatActivity.AWAITING_INPUT ||
    input.activity === ChatActivity.ERROR ||
    input.needsRecovery === true
  );
}
