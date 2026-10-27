/**
 * Focus the composer when the user switches chats.
 *
 * Switching chats is a request to work in the chat you switched to, so the
 * next keystroke should land in its composer. There are a dozen ways to switch
 * (sidebar click, the Cmd+E switcher, next/prev and next-attention shortcuts,
 * a search hit, sending a new chat, a project or worktree switch, a
 * notification) and they all end in `chatStore.selectChat`. So the intent is
 * recorded once, where the active chat id changes, rather than at every call
 * site.
 *
 * The intent cannot be acted on at that moment: the new chat's composer has
 * not mounted yet (`ChatInterface` keys `ChatContainer` on the chat id, and
 * several switch paths await a worktree switch first). It is therefore held as
 * a pending request and consumed by the composer for that chat once it is
 * mounted and enabled — including later, if the composer is hidden while the
 * chat loads or behind an ask_user prompt.
 *
 * Taking focus is guarded, because not every change of the active chat is the
 * user asking to type there (a workspace restore, the active chat being
 * archived elsewhere):
 *
 *   - Focus is never taken from somewhere the user can type (another input,
 *     the terminal, an editor) or from inside an open dialog.
 *   - On a touch-primary device nothing is focused: that would pop the
 *     on-screen keyboard over the chat the user just opened to read.
 *
 * Either way the request is consumed, so it is decided once per switch and a
 * composer that remounts later in the same chat does not grab focus again.
 */

import { useChatStore } from "../../store/chatStore";

/** The chat whose composer should take focus when it is ready, if any. */
let pendingChatId: string | null = null;
let lastActiveChatId: string | null | undefined;

/** Record that the active chat is now `chatId`. */
export function noteActiveChat(chatId: string | null): void {
  if (chatId === lastActiveChatId) return;
  lastActiveChatId = chatId;
  pendingChatId = chatId;
}

// The app's initial chat counts as a switch too: landing on a chat should
// leave the cursor in it, the way the new-chat view already does.
noteActiveChat(useChatStore.getState().activeChatId);
useChatStore.subscribe((state) => noteActiveChat(state.activeChatId));

/**
 * Consume the pending request if it is for `chatId`.
 *
 * Returns true when the caller should focus its composer now.
 */
export function takeSwitchFocus(chatId: string): boolean {
  if (pendingChatId !== chatId) return false;
  pendingChatId = null;
  return !isTouchPrimary() && !isFocusClaimed();
}

function isTouchPrimary(): boolean {
  return window.matchMedia?.("(pointer: coarse)").matches ?? false;
}

/**
 * Whether focus currently sits somewhere the user would lose work or context
 * by having it moved: an editable control, or anything inside a dialog.
 *
 * A button, a sidebar row or the page body is residue of the click or shortcut
 * that did the switch, and is fair to move off.
 */
function isFocusClaimed(): boolean {
  const el = document.activeElement;
  if (!(el instanceof HTMLElement) || el === document.body) return false;
  if (el.closest('[role="dialog"], [role="alertdialog"], [aria-modal="true"]')) {
    return true;
  }
  if (el.isContentEditable) return true;
  if (el instanceof HTMLTextAreaElement || el instanceof HTMLSelectElement) {
    return true;
  }
  if (el instanceof HTMLInputElement) {
    return !NON_TEXT_INPUTS.has(el.type);
  }
  return false;
}

const NON_TEXT_INPUTS = new Set([
  "button",
  "checkbox",
  "radio",
  "reset",
  "submit",
  "image",
  "range",
  "color",
  "file",
]);

/** Test-only: forget every recorded switch. */
export function resetSwitchFocusForTests(): void {
  pendingChatId = null;
  lastActiveChatId = undefined;
}
