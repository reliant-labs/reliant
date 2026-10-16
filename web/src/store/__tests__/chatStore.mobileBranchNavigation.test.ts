import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useChatStore } from "../chatStore";
import { useChatNavigationStore } from "../chatNavigationStore";
import { useProjectStore } from "../projectStore";
import type { Chat } from "../../types/chat";

const { navigate } = vi.hoisted(() => ({ navigate: vi.fn().mockResolvedValue(undefined) }));
vi.mock("../../routes", () => ({ router: { navigate } }));
const branch = { id: "branched-chat" } as Chat;
beforeEach(() => {
  navigate.mockClear();
  useProjectStore.setState({ currentProject: null });
  useChatNavigationStore.setState({ chatQueue: [] });
});
afterEach(() => window.history.replaceState({}, "", "/"));

describe("Branched chat navigation", () => {
  it("opens a branch in the mobile route as well as updating the chat queue", async () => {
    window.history.replaceState({}, "", "/m/chats/source-chat");
    await useChatStore.getState()._navigateToBranchedChat(branch);
    expect(navigate).toHaveBeenCalledWith({ to: "/m/chats/$chatId", params: { chatId: "branched-chat" } });
    expect(useChatNavigationStore.getState().chatQueue[0]).toBe("branched-chat");
  });
  it("retains desktop navigation without changing the mobile URL", async () => {
    window.history.replaceState({}, "", "/");
    await useChatStore.getState()._navigateToBranchedChat(branch);
    expect(navigate).not.toHaveBeenCalled();
    expect(useChatNavigationStore.getState().chatQueue[0]).toBe("branched-chat");
  });
});
