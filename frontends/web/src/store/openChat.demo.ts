// Copyright (c) 2025 Reliant Labs

/**
 * store/openChat.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): chats are Reliant's, and the demo has none, so
 * "Chat about this app" says where that happens instead of opening one. The
 * real module reaches store/chatStore and the session, which the demo bundle
 * must not contain.
 */

import { toast } from "@/lib/toast-manager";

import type * as Real from "./openChat";

export const openNewChat: typeof Real.openNewChat = async () => {
  void toast.error(new Error("Chats run in Reliant, not in the forge demo."));
};
