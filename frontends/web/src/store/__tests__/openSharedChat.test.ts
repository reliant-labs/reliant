/**
 * Opening another member's shared chat, and losing it.
 *
 * openSharedChat lets the chat stream ask the server again for a chat it
 * refused before (it may be shared again). leaveSharedChat closes a shared
 * chat the server stopped streaming — made private, deleted, the viewer left
 * the org — and leaves the person's own chats alone.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  chats: new Map<string, { id: string; projectId: string; viewerAccess?: boolean }>(),
  state: { activeChatId: null as string | null },
  selectChat: vi.fn(),
  clearCurrentChat: vi.fn(),
  evictChat: vi.fn(),
  navigateToChat: vi.fn(),
  removeFromQueue: vi.fn(async () => {}),
  seedChatDetail: vi.fn(),
  removeChatFromListCache: vi.fn(),
  invalidateQueries: vi.fn(async () => {}),
  toastInfo: vi.fn(),
}));

vi.mock("../chatStore", () => ({
  useChatStore: {
    getState: () => ({
      activeChatId: mocks.state.activeChatId,
      selectChat: mocks.selectChat,
      clearCurrentChat: mocks.clearCurrentChat,
      evictChat: mocks.evictChat,
    }),
  },
}));
vi.mock("../chatNavigationStore", () => ({
  useChatNavigationStore: {
    getState: () => ({ navigateToChat: mocks.navigateToChat, removeFromQueue: mocks.removeFromQueue }),
  },
}));
vi.mock("../../hooks/chat-queries", () => ({
  getChatFromCache: (id: string) => mocks.chats.get(id),
  seedChatDetail: mocks.seedChatDetail,
  removeChatFromListCache: mocks.removeChatFromListCache,
}));
vi.mock("../../lib/toast-manager", () => ({
  toast: { info: mocks.toastInfo },
}));

import { leaveSharedChat, openSharedChat } from "../openChat";
import { sharingServiceKeys } from "../../hooks/sharing-service-hooks_gen";
import { queryClient } from "../../lib/query-client";
import { orgKeys } from "../../services/controlPlane/orgs";

const shared = { id: "shared-1", projectId: "their-project", viewerAccess: true };
const own = { id: "own-1", projectId: "my-project" };

beforeEach(() => {
  vi.restoreAllMocks();
  vi.clearAllMocks();
  vi.spyOn(queryClient, "invalidateQueries").mockImplementation(mocks.invalidateQueries);
  mocks.chats.clear();
  mocks.chats.set(shared.id, shared);
  mocks.chats.set(own.id, own);
  mocks.state.activeChatId = null;
});

describe("openSharedChat", () => {
  it("lets the stream ask again for a chat it was refused before", () => {
    openSharedChat(shared as never);
    expect(mocks.selectChat).toHaveBeenCalledWith(shared, { deferPersist: true, retryRefused: true });
    expect(mocks.navigateToChat).toHaveBeenCalledWith("shared-1");
  });
});

describe("leaveSharedChat", () => {
  it("closes the open shared chat, says why, and drops what was cached of it", () => {
    mocks.state.activeChatId = "shared-1";
    expect(leaveSharedChat("shared-1")).toBe(true);
    expect(mocks.clearCurrentChat).toHaveBeenCalledWith(null);
    expect(mocks.toastInfo).toHaveBeenCalledWith("This chat is no longer shared with you.");
    expect(mocks.evictChat).toHaveBeenCalledWith("shared-1");
    expect(mocks.removeChatFromListCache).toHaveBeenCalledWith("their-project", "shared-1");
    expect(mocks.removeFromQueue).toHaveBeenCalledWith("shared-1");
    expect(mocks.invalidateQueries).toHaveBeenCalledWith({ queryKey: sharingServiceKeys.all });
    // Losing it may mean losing the org.
    expect(mocks.invalidateQueries).toHaveBeenCalledWith({ queryKey: orgKeys.mine });
  });

  it("drops a shared chat that is not open without moving the person or telling them", () => {
    mocks.state.activeChatId = "own-1";
    expect(leaveSharedChat("shared-1")).toBe(true);
    expect(mocks.clearCurrentChat).not.toHaveBeenCalled();
    expect(mocks.toastInfo).not.toHaveBeenCalled();
    expect(mocks.evictChat).toHaveBeenCalledWith("shared-1");
  });

  it("leaves the person's own chats, and chats it knows nothing of, alone", () => {
    mocks.state.activeChatId = "own-1";
    expect(leaveSharedChat("own-1")).toBe(false);
    expect(leaveSharedChat("unknown")).toBe(false);
    expect(mocks.clearCurrentChat).not.toHaveBeenCalled();
    expect(mocks.evictChat).not.toHaveBeenCalled();
    expect(mocks.invalidateQueries).not.toHaveBeenCalled();
  });
});
