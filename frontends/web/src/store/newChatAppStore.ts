// Copyright (c) 2025 Reliant Labs

/**
 * The app the next new chat is about (Chat.app_name, control-plane
 * docs/design/unified-shell-and-projects.md §8.5, D4): set by a screen that
 * starts a chat FROM an app — its console's "Start a chat here", Ask Reliant,
 * "Fix with agent" — and sent as StartChatRequest.app_name on the first send.
 *
 * The URL carries it too (`/new?app=<name>`, store/openChat.ts), so a
 * refresh keeps it: the `/new` route applies it here (applyNewChat). Not a
 * one-shot mailbox like newChatMachineStore: the new-chat view remounts while
 * the shell settles the project, so it READS the tag (for its project) rather
 * than taking it. It ends when the chat is created, when the person removes
 * it from the composer, and on every other way into a new chat: a new chat
 * opened without an app (the sidebar's New chat, the shortcut, the project
 * chip) clears it, so a chat started from the sidebar carries no tag.
 * Nothing infers one.
 */

import { create } from "zustand";

export interface NewChatAppTag {
  /** The project the new chat starts in; the tag applies only there. */
  projectId: string;
  /** forge.yaml `name`, in the chat's org. */
  appName: string;
}

interface NewChatAppStore {
  tag: NewChatAppTag | null;
  /** Tag the next new chat in `projectId`; an invalid name clears it instead. */
  set: (tag: NewChatAppTag | null) => void;
  clear: () => void;
}

// forge's project-name shape, as StartChatRequest.app_name validates it.
const APP_NAME = /^[A-Za-z][A-Za-z0-9_-]{0,127}$/;

/** Whether `name` has the shape the server accepts as an app name. */
export function isAppName(name: string | null | undefined): name is string {
  return typeof name === "string" && APP_NAME.test(name);
}

export const useNewChatAppStore = create<NewChatAppStore>((set) => ({
  tag: null,
  set: (tag) => set({ tag: tag && tag.projectId && isAppName(tag.appName) ? tag : null }),
  clear: () => set({ tag: null }),
}));

/** The app the next new chat in `projectId` is about, if one was set for it. */
export function newChatAppFor(tag: NewChatAppTag | null, projectId: string | null | undefined): string | undefined {
  return tag && projectId && tag.projectId === projectId ? tag.appName : undefined;
}
