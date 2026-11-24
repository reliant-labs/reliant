/**
 * Keyboard next/prev walks the list the sidebar shows when the chat filter
 * names several projects (`?projects=`): that filter's own list, never the
 * every-project one, so a shortcut cannot open a chat of a project not chosen.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { chatKeys } from "../../hooks/chat-queries";
import { resetChatProjectFilterForTests, useChatProjectFilterStore } from "../../hooks/useChatProjectFilter";
import { chatListScope } from "../../lib/chatProjectFilter";
import { queryClient } from "../../lib/query-client";
import { publishShellScope, resetShellScopeForTests } from "../../lib/urlState/shellScopeStore";
import { useChatListPreferencesStore } from "../chatListPreferencesStore";
import { useChatNavigationStore } from "../chatNavigationStore";
import { useWorkspaceStateStore } from "../workspaceStateStore";

const opened = vi.hoisted(() => vi.fn());
vi.mock("../../lib/openChatInWorkspace", () => ({ openChatInWorkspace: opened }));
vi.mock("../chatStore", () => ({ useChatStore: { getState: () => ({ activeChatId: null }) } }));

const chat = (id: string, projectId: string) => ({
  id,
  title: id,
  projectId,
  state: 0,
  unread: false,
  createdAt: "2024-01-01T00:00:00.000Z",
  updatedAt: "2024-01-01T00:00:00.000Z",
  lastMessageAt: "2024-01-01T00:00:00.000Z",
});

const envelope = (chats: ReturnType<typeof chat>[]) => ({ chats, total: chats.length, lastUserUpdateSequence: 1 });

describe("chatNavigationStore and a filter of several projects", () => {
  beforeEach(() => {
    queryClient.clear();
    opened.mockReset();
    resetChatProjectFilterForTests();
    useChatListPreferencesStore.setState({ viewMode: "flat", sortOrder: "alphabetical_asc" });
    useWorkspaceStateStore.setState({ projectFilterId: null, machineFilterIds: null });
    useChatProjectFilterStore.setState({ many: ["p-1", "p-2"] });
    // What the nav shows (useShellProject publishes it), which the keyboard walks.
    publishShellScope({ scope: { mode: "many", projectIds: ["p-1", "p-2"] }, focus: null });
  });

  afterEach(() => {
    queryClient.clear();
    resetChatProjectFilterForTests();
    resetShellScopeForTests();
  });

  it("walks the chosen projects' list, not every project's", async () => {
    // "a-other" sorts first, but its project was not chosen.
    queryClient.setQueryData(chatKeys.list(null), envelope([chat("a-other", "p-3"), chat("b-one", "p-1")]));
    queryClient.setQueryData(chatKeys.list(chatListScope(["p-1", "p-2"])), envelope([chat("c-two", "p-2"), chat("b-one", "p-1")]));
    await useChatNavigationStore.getState().navigateNext();
    expect(opened).toHaveBeenCalledWith(expect.objectContaining({ id: "b-one" }));
  });

  it("walks the chosen projects' list when every project's was never loaded (a direct ?projects= URL)", async () => {
    queryClient.setQueryData(chatKeys.list(chatListScope(["p-2", "p-1"])), envelope([chat("c-two", "p-2")]));
    await useChatNavigationStore.getState().navigateNext();
    expect(opened).toHaveBeenCalledWith(expect.objectContaining({ id: "c-two" }));
  });
});
