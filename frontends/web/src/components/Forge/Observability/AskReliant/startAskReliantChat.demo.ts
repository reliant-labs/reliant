// Copyright (c) 2025 Reliant Labs

/**
 * startAskReliantChat.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). The demo has no Reliant chat: "Ask an agent"
 * is in handoff mode there (a sheet to copy from), so this is never reached;
 * if it were, it says so instead of loading the chat store.
 */

import type * as Real from "./startAskReliantChat";

export type StartAskReliantChat = Real.StartAskReliantChat;
export type StartedAskReliantChat = Real.StartedAskReliantChat;

export const startAskReliantChat: typeof Real.startAskReliantChat = async () => {
  throw new Error("Chats are not part of the forge demo.");
};
