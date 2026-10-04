// Copyright (c) 2025 Reliant Labs

/**
 * The client half of the sidebar policy (WORKFLOW_UI.md §6.2).
 *
 * The server decides which chats the sidebar lists (`ListChats` with
 * `sidebar_only`, from the `list_in_sidebar` view column): interactive chats
 * and adopted runs. Rule 4 is the one the server cannot know: the chat the
 * user has OPEN stays in the list, whatever the server returned. Without it,
 * an automation chat opened from Runs or the Inbox has no row at all, and one
 * that was listed only while it awaited input vanishes under the cursor once
 * the user answers (L2).
 */

import { ChatState } from "../gen/reliant/v1/chat_pb";

/** The fields of a chat this policy reads. */
interface SidebarPolicyChat {
  id: string;
  projectId?: string;
  state?: ChatState;
  launchKind?: string;
  adoptedAt?: string;
}

/**
 * Whether a chat was started by something other than a person typing: a
 * schedule, an agent, later a webhook. Null predates launch kinds and reads
 * as a chat.
 */
export function isAutomationLaunch(launchKind: string | null | undefined): boolean {
  return !!launchKind && launchKind !== "chat.start";
}

/** An automation run the user took over (§6.3). It lists, with an origin glyph. */
export function isAdoptedAutomation(chat: SidebarPolicyChat): boolean {
  return isAutomationLaunch(chat.launchKind) && !!chat.adoptedAt;
}

/** An automation run nobody has taken over yet: replying would adopt it. */
export function isUnadoptedAutomation(chat: SidebarPolicyChat): boolean {
  return isAutomationLaunch(chat.launchKind) && !chat.adoptedAt;
}

/**
 * The listed chats plus the open one, when the server left it out.
 *
 * The open chat is appended rather than placed: the sidebar sorts the result,
 * so it lands in its natural position for the current sort order. It is only
 * pinned into the project it belongs to, never when archived (archived chats
 * have their own tab), and never when the user has just moved it out of the
 * list themselves (`releasedChatId`, see useSidebarPinStore).
 */
export function withOpenChatPinned<T extends SidebarPolicyChat>(
  listed: readonly T[],
  openChat: T | undefined,
  options: { projectId: string | undefined; releasedChatId?: string | null },
): T[] {
  if (!openChat || !options.projectId) return [...listed];
  if (openChat.id === options.releasedChatId) return [...listed];
  if (openChat.projectId !== options.projectId) return [...listed];
  if (openChat.state === ChatState.ARCHIVED) return [...listed];
  if (listed.some((chat) => chat.id === openChat.id)) return [...listed];
  return [...listed, openChat];
}
