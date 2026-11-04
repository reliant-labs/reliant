import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { useChatStore } from "../chatStore";
import { useProjectStore } from "../projectStore";

const branch = vi.fn();

beforeEach(() => {
  branch.mockReset();
  useProjectStore.setState({
    currentProject: { id: "project-branch-result", name: "Project", path: "/project" },
  } as never);
  setTransport(mockTransport({
    "ChatService.BranchChat": async (request: unknown) => ({ chat: await branch(request) }),
  }));
});
afterEach(() => setTransport(null));

describe("branch completion results", () => {
  it.each(["same-workspace", "other-workspace", "without-machine"])(
    "returns the created chat for route-owned navigation: %s",
    async (variant) => {
      branch.mockResolvedValue({
        id: `branch-${variant}`,
        projectId: "project-branch-result",
        title: "Branched conversation",
      });
      const store = useChatStore.getState();
      const result = variant === "same-workspace"
        ? await store.branchChat("source", "original-message")
        : variant === "other-workspace"
          ? await store.branchChatToWorktree("source", "original-message", "workspace")
          : await store.branchChatWithoutMachine("source", "original-message");

      expect(result?.id).toBe(`branch-${variant}`);
      expect(useChatStore.getState().activeChatId).toBe(result?.id);
      expect(branch).toHaveBeenCalledOnce();
      expect(branch.mock.calls[0][0]).toMatchObject({
        chatId: "source", messageId: "original-message",
      });
    },
  );

  it("does not report a successful result when creation fails", async () => {
    const failure = new Error("Branch could not be created");
    branch.mockRejectedValue(failure);
    await expect(useChatStore.getState().branchChat("source", "original-message"))
      .rejects.toBe(failure);
  });
});
