// Copyright (c) 2025 Reliant Labs

/**
 * The client half of the sidebar policy (WORKFLOW_UI.md §6.2).
 *
 * The server decides which chats the chat list holds (`ListChats`, from the
 * `list_in_sidebar` view column): chats a person started (origin USER) and
 * runs the user adopted. The client applies the same rule to whatever reaches
 * the list cache (`isListedInChatList`), so a run that arrives some other way
 * (a stale cache, an upsert) never renders a row. Rule 4 is the one the server
 * cannot know: the chat the user has OPEN stays in the list, whatever the
 * server returned. Without it, an automation chat opened from Runs or the
 * Inbox has no row at all, and one that was listed only while it awaited
 * input vanishes under the cursor once the user answers (L2).
 */

import { ChatOrigin, ChatState } from "../gen/reliant/v1/chat_pb";

/** The fields of a chat this policy reads. */
interface SidebarPolicyChat {
  id: string;
  projectId?: string;
  state?: ChatState;
  origin?: ChatOrigin;
  launchKind?: string;
  adoptedAt?: string;
}

/**
 * Whether a launch kind is something other than a person typing: a schedule,
 * a webhook, a provider event, an agent. Null predates launch kinds and reads
 * as a chat. Policy reads `isRun`, which prefers the chat's origin.
 */
export function isAutomationLaunch(launchKind: string | null | undefined): boolean {
  return !!launchKind && launchKind !== "chat.start";
}

/**
 * Whether a chat is a run (started by an automation, an agent, Run now or a
 * builder test) rather than a conversation a person started. The server
 * stamps every chat's origin; UNSPECIFIED only comes from a server that
 * predates it, and then the launch kind decides as it used to.
 */
export function isRun(chat: Pick<SidebarPolicyChat, "origin" | "launchKind">): boolean {
  if (chat.origin !== undefined && chat.origin !== ChatOrigin.UNSPECIFIED) {
    return chat.origin !== ChatOrigin.USER;
  }
  return isAutomationLaunch(chat.launchKind);
}

/** An automation run the user took over (§6.3). It lists, with an origin glyph. */
export function isAdoptedAutomation(chat: SidebarPolicyChat): boolean {
  return isRun(chat) && !!chat.adoptedAt;
}

/** An automation run nobody has taken over yet: replying would adopt it. */
export function isUnadoptedAutomation(chat: SidebarPolicyChat): boolean {
  return isRun(chat) && !chat.adoptedAt;
}

/** The chat list's rule: a conversation, or a run the user adopted. */
export function isListedInChatList(chat: SidebarPolicyChat): boolean {
  return !isUnadoptedAutomation(chat);
}

/** The chats the chat list renders: `listed` minus any unadopted run. */
export function chatListRows<T extends SidebarPolicyChat>(listed: readonly T[]): T[] {
  return listed.filter(isListedInChatList);
}

/**
 * The chat list's rows (`chatListRows` of the listed chats) plus the open one,
 * when the server or the policy left it out.
 *
 * The open chat is appended rather than placed: the sidebar sorts the result,
 * so it lands in its natural position for the current sort order. It is only
 * pinned into a list whose scope holds its project (`scope` null is every
 * project), never when archived (archived chats have their own tab), and never
 * when the user has just moved it out of the list themselves
 * (`releasedChatId`, see useSidebarPinStore).
 */
export function withOpenChatPinned<T extends SidebarPolicyChat>(
  listed: readonly T[],
  openChat: T | undefined,
  options: { scope: string | null; releasedChatId?: string | null },
): T[] {
  const rows = chatListRows(listed);
  if (!openChat) return rows;
  if (openChat.id === options.releasedChatId) return rows;
  if (options.scope !== null && openChat.projectId !== options.scope) return rows;
  if (openChat.state === ChatState.ARCHIVED) return rows;
  if (rows.some((chat) => chat.id === openChat.id)) return rows;
  return [...rows, openChat];
}
