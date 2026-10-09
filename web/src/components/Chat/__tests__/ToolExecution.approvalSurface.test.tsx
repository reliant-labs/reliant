import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ToolExecution } from "../ToolExecution";
import { ApprovalStatus } from "../../../gen/reliant/v1/approval_pb";
import type { ToolApprovalRequest } from "../../../api/client";

// This is the approval UI that actually ships: ChatContainer -> ChatPresenter
// -> InterleavedTimeline -> ChatMessage -> ToolExecution, shared by desktop
// and by the mobile surface via MobileChatScreen. The mobile failure mode
// these tests pin is a decision screen whose Approve/Deny are too small to
// hit, or pushed off-screen by the diff above them.
vi.mock("../../../store/chatStoreHooks", () => ({
  useChat: () => undefined,
  useChatMessages: () => [],
  useStreamingMessages: () => [],
  useToolCallStates: () => new Map(),
}));
vi.mock("../../../hooks/approval-queries", () => ({
  useApproveToolRequest: () => ({ mutate: vi.fn() }),
  useDenyToolRequest: () => ({ mutate: vi.fn() }),
}));
vi.mock("../../../hooks/task-queries", () => ({
  useTasksForChat: () => ({ data: undefined }),
}));
vi.mock("../../../store/threadActivityStore", () => ({
  useActiveThreads: () => [],
}));
vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: [] }),
}));

const pendingApproval: ToolApprovalRequest = {
  id: "approval-1",
  chat_id: "chat-1",
  content_block_id: "block-1",
  tool_call_id: "toolu_1",
  tool_name: "edit",
  description: "Edit src/index.ts",
  status: ApprovalStatus.PENDING,
  created_at: new Date().toISOString(),
};

function renderApproval(surface: "desktop" | "mobile" | "embed") {
  return render(
    <SurfaceProvider surface={surface}>
      <ToolExecution
        toolCall={{
          id: "toolu_1",
          name: "edit",
          input: {
            file_path: "src/index.ts",
            old_string: "const a = 1;",
            new_string: "const a = 2;",
          },
          finished: true,
        }}
        approval={pendingApproval}
        chatId="chat-1"
        showRichContent
      />
    </SurfaceProvider>,
  );
}

describe("ToolExecution approval UI across surfaces", () => {
  it("shows the approval affordance with both actions on every surface", () => {
    for (const surface of ["desktop", "mobile", "embed"] as const) {
      const { unmount } = renderApproval(surface);

      expect(screen.getByText("Approval required")).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "Approve tool execution" }),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "Deny tool execution" }),
      ).toBeInTheDocument();

      unmount();
    }
  });

  it("gives Approve/Deny 44px touch targets on mobile and embed", () => {
    for (const surface of ["mobile", "embed"] as const) {
      const { unmount } = renderApproval(surface);

      const approve = screen.getByRole("button", { name: "Approve tool execution" });
      const deny = screen.getByRole("button", { name: "Deny tool execution" });
      expect(approve.className).toMatch(/min-h-\[44px\]/);
      expect(deny.className).toMatch(/min-h-\[44px\]/);

      unmount();
    }
  });

  it("keeps the compact desktop buttons unchanged", () => {
    renderApproval("desktop");

    const approve = screen.getByRole("button", { name: "Approve tool execution" });
    expect(approve.className).not.toMatch(/min-h-\[44px\]/);
    expect(approve.className).toMatch(/py-1\.5/);
  });

  it("renders the diff with the lightweight viewer, never Monaco", () => {
    const { container } = renderApproval("mobile");

    expect(container.querySelector(".diff-viewer")).toBeInTheDocument();
    expect(screen.queryByTestId("monaco-diff-editor")).not.toBeInTheDocument();
  });
});
