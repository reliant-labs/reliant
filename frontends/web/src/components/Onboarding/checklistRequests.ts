/**
 * Requests the Setup Guide (OnboardingChecklist) makes of surfaces it does not
 * own, which may not be mounted when the request is made.
 *
 * The guide shows on every page of the shell, but the composer and the
 * create-workspace modal live on the chat home. A request is a window event
 * plus a pending flag: a mounted chat home answers the event and clears the
 * flag (dispatchEvent is synchronous), so a flag still set after the dispatch
 * means nobody listened. The guide then opens the chat home, which finds the
 * flag as it mounts.
 *
 * Mounting does not clear it: arriving at the chat home can mount it twice
 * (the first instance unmounts in the same tick), and a cleared flag would
 * leave the second without the modal. The modal clears it when it closes, and
 * an unanswered request expires so it never opens the modal later, unasked.
 */

export const OPEN_CREATE_WORKTREE_EVENT = "open-create-worktree-modal";

/** How long a request waits for the chat home to mount. */
export const CREATE_WORKTREE_REQUEST_TTL_MS = 10_000;

let createWorktreeRequestedAt: number | null = null;

/** Asks the chat home to open its create-workspace modal. True when a mounted one took it. */
export function requestCreateWorktree(): boolean {
  createWorktreeRequestedAt = Date.now();
  window.dispatchEvent(new CustomEvent(OPEN_CREATE_WORKTREE_EVENT));
  return createWorktreeRequestedAt === null;
}

/** Whether a create-workspace request is waiting (and not expired). Does not clear it. */
export function createWorktreeRequested(): boolean {
  if (createWorktreeRequestedAt === null) return false;
  if (Date.now() - createWorktreeRequestedAt > CREATE_WORKTREE_REQUEST_TTL_MS) {
    createWorktreeRequestedAt = null;
    return false;
  }
  return true;
}

/** The request is answered: the modal took it (from the event) or closed. */
export function clearCreateWorktreeRequest(): void {
  createWorktreeRequestedAt = null;
}

/** The composer's own anchor (ChatInput's `data-onboarding="chat-input"`). */
export const CHAT_INPUT_SELECTOR = "[data-onboarding='chat-input']";

/** Focuses the composer when one is on screen (ChatTextArea listens). True when there was one. */
export function requestChatInputFocus(): boolean {
  if (!document.querySelector(CHAT_INPUT_SELECTOR)) return false;
  window.dispatchEvent(new CustomEvent("focus-chat-input"));
  return true;
}
