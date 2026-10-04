// Copyright (c) 2025 Reliant Labs

/**
 * Automation-launched chats are kept out of the project chat list (the
 * sidebar) and stay reachable by id.
 *
 *  1. The list request asks the server to exclude them. The filter is
 *     server-side on purpose: a busy schedule must not fill the default page
 *     and push real conversations out, and the server keeps automation chats
 *     that are waiting on the user.
 *  2. resolveChat — the lookup the reopen paths use — falls back to GetChat
 *     on a detail-cache miss, because a miss no longer means "deleted".
 *  3. The regression this guards: on reload, workspace restore used to read
 *     the active chat from the cache loadChats seeds from the list. An
 *     automation chat is not in that list, so restore declared it "no longer
 *     exists" and cleared it, dropping the user out of the chat they had open.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { ChatSchema, GetChatResponseSchema, ListChatsResponseSchema } from "@/gen/reliant/v1/chat_pb";

const listChats = vi.fn();
const getChat = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: { chat: () => ({ listChats, getChat }) },
}));

// ── Stores used by useWorkspaceRestore, as plain recorders ────────────────
const selectChat = vi.fn();
const setActiveChatId = vi.fn();
const workspace = {
  activeChatId: "auto-chat" as string | null,
};

vi.mock("@/store/workspaceStateStore", () => {
  const state = () => ({
    getWorktreeState: () => ({
      activeChatId: workspace.activeChatId,
      scrollPositions: {},
      terminalOpen: false,
    }),
    setActiveChatId,
  });
  return {
    useWorkspaceStateStore: Object.assign(() => state(), {
      getState: state,
      persist: { hasHydrated: () => true },
    }),
  };
});

vi.mock("@/store/projectStore", () => {
  const state = () => ({
    projects: [{ id: "proj-1", name: "Reliant" }],
    currentProject: { id: "proj-1", name: "Reliant" },
    isLoading: false,
    loadProjects: vi.fn(async () => undefined),
    restoreLastProject: vi.fn(async () => true),
  });
  return { useProjectStore: Object.assign(() => state(), { getState: state }) };
});

vi.mock("@/store/worktreeStore", () => {
  const state = () => ({
    worktrees: [{ id: "wt-main", name: "main", is_main: true }],
    currentWorktree: null,
    loadWorktrees: vi.fn(async () => undefined),
    restoreLastWorktree: vi.fn(() => false),
    switchWorktreeContext: vi.fn(async () => undefined),
  });
  return { useWorktreeStore: Object.assign(() => state(), { getState: state }) };
});

vi.mock("@/store/viewerStore", () => {
  const state = () => ({ restoreFromWorkspaceState: vi.fn(), viewers: [] });
  return { useViewerStore: Object.assign(() => state(), { getState: state }) };
});

vi.mock("@/store/chatNavigationStore", () => {
  const state = () => ({ restoreFromWorkspaceState: vi.fn() });
  return { useChatNavigationStore: Object.assign(() => state(), { getState: state }) };
});

vi.mock("@/store/terminalStore", () => {
  const state = () => ({ showTerminal: vi.fn() });
  return { useTerminalStore: Object.assign(() => state(), { getState: state }) };
});

vi.mock("@/store/chatStore", () => {
  const state = () => ({
    hasLoaded: false,
    // loadChats seeds the detail cache from the LIST; the automation chat is
    // not in it, so this seeds nothing for it.
    loadChats: vi.fn(async () => undefined),
    selectChat,
  });
  return { useChatStore: Object.assign(() => state(), { getState: state }) };
});

import { chatGrpc } from "@/api/chat-grpc";
import { chatKeys, getChatFromCache, resolveChat } from "@/hooks/chat-queries";
import { queryClient } from "@/lib/query-client";
import { useWorkspaceRestore } from "@/hooks/useWorkspaceRestore";

const automationChat = create(ChatSchema, {
  id: "auto-chat",
  title: "Morning triage · 9:00",
  projectId: "proj-1",
  launchKind: "schedule",
  triggerId: "trig-1",
});

beforeEach(() => {
  listChats.mockReset();
  getChat.mockReset();
  selectChat.mockReset();
  setActiveChatId.mockReset();
  workspace.activeChatId = "auto-chat";
  queryClient.clear();
});

describe("project chat list", () => {
  it("asks the server to exclude automation chats", async () => {
    listChats.mockResolvedValue(create(ListChatsResponseSchema, { chats: [], total: 0 }));

    await chatGrpc.list("proj-1");

    expect(listChats).toHaveBeenCalledTimes(1);
    expect(listChats.mock.calls[0]![0]).toMatchObject({ projectId: "proj-1", sidebarOnly: true });
  });
});

describe("resolveChat", () => {
  it("returns a cached chat without a request", async () => {
    queryClient.setQueryData(chatKeys.detail("auto-chat"), { id: "auto-chat", title: "cached" });

    expect(await resolveChat("auto-chat")).toMatchObject({ title: "cached" });
    expect(getChat).not.toHaveBeenCalled();
  });

  it("fetches a chat the list omitted, and seeds the detail cache", async () => {
    getChat.mockResolvedValue(create(GetChatResponseSchema, { chat: automationChat }));

    const chat = await resolveChat("auto-chat");

    expect(getChat.mock.calls[0]![0]).toMatchObject({ chatId: "auto-chat" });
    expect(chat).toMatchObject({ id: "auto-chat", launchKind: "schedule", triggerId: "trig-1" });
    expect(getChatFromCache("auto-chat")).toMatchObject({ id: "auto-chat" });
  });

  it("resolves undefined for a chat that really is gone", async () => {
    getChat.mockRejectedValue(new ConnectError("chat not found", Code.NotFound));

    expect(await resolveChat("deleted-chat")).toBeUndefined();
  });
});

describe("workspace restore", () => {
  it("reopens an active automation chat that is not in the chat list", async () => {
    getChat.mockResolvedValue(create(GetChatResponseSchema, { chat: automationChat }));

    const { result } = renderHook(() => useWorkspaceRestore({ skipProjectRestore: true }));

    await waitFor(() => expect(result.current.isComplete).toBe(true));
    expect(selectChat).toHaveBeenCalledWith(expect.objectContaining({ id: "auto-chat" }));
    // The stored active chat was NOT cleared as "no longer exists".
    expect(setActiveChatId).not.toHaveBeenCalled();
    expect(result.current.warnings).toEqual([]);
  });

  it("still clears an active chat that no longer exists", async () => {
    workspace.activeChatId = "deleted-chat";
    getChat.mockRejectedValue(new ConnectError("chat not found", Code.NotFound));

    const { result } = renderHook(() => useWorkspaceRestore({ skipProjectRestore: true }));

    await waitFor(() => expect(result.current.isComplete).toBe(true));
    expect(selectChat).not.toHaveBeenCalled();
    expect(setActiveChatId).toHaveBeenCalledWith("proj-1", null, null);
  });
});
