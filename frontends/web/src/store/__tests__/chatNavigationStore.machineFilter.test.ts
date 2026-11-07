/**
 * Keyboard next/prev walks the list the sidebar shows, so it skips the chats
 * the machine filter hides.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DaemonStatus } from "../../gen/reliant/v1/daemon_registry_pb";
import { chatKeys } from "../../hooks/chat-queries";
import { queryClient } from "../../lib/query-client";
import { seedDaemonList } from "../../test/daemonRegistry";
import { useChatListPreferencesStore } from "../chatListPreferencesStore";
import { useChatNavigationStore } from "../chatNavigationStore";
import { useWorkspaceStateStore } from "../workspaceStateStore";

const opened = vi.hoisted(() => vi.fn());
vi.mock("../../lib/openChatInWorkspace", () => ({ openChatInWorkspace: opened }));
vi.mock("../chatStore", () => ({ useChatStore: { getState: () => ({ activeChatId: null }) } }));

const chat = (id: string, activeDaemonId?: string) => ({
  id,
  title: id,
  projectId: "project-1",
  activeDaemonId,
  state: 0,
  unread: false,
  createdAt: "2024-01-01T00:00:00.000Z",
  updatedAt: "2024-01-01T00:00:00.000Z",
  lastMessageAt: "2024-01-01T00:00:00.000Z",
});

describe("chatNavigationStore and the machine filter", () => {
  beforeEach(() => {
    queryClient.clear();
    opened.mockReset();
    useChatListPreferencesStore.setState({ viewMode: "flat", sortOrder: "alphabetical_asc" });
    useWorkspaceStateStore.setState({ projectFilterId: "project-1", machineFilterIds: null });
    seedDaemonList(queryClient, [
      { daemonId: "laptop", hostname: "laptop", status: DaemonStatus.ACTIVE, isDefault: true },
      { daemonId: "cloud", hostname: "cloud", status: DaemonStatus.ACTIVE },
    ]);
    // "a-unpinned" sorts first, and is the default machine's (the laptop's).
    queryClient.setQueryData(chatKeys.list("project-1"), {
      chats: [chat("a-unpinned"), chat("b-on-cloud", "cloud")],
      total: 2,
      lastUserUpdateSequence: 1,
    });
  });

  afterEach(() => queryClient.clear());

  it("walks every chat under All machines", async () => {
    await useChatNavigationStore.getState().navigateNext();
    expect(opened).toHaveBeenCalledWith(expect.objectContaining({ id: "a-unpinned" }));
  });

  it("skips the chats the filter hides", async () => {
    useWorkspaceStateStore.setState({ machineFilterIds: ["cloud"] });
    await useChatNavigationStore.getState().navigateNext();
    expect(opened).toHaveBeenCalledWith(expect.objectContaining({ id: "b-on-cloud" }));
  });
});
