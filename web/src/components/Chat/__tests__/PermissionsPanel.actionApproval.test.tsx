import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ApprovalStatus, ApprovalType } from "../../../gen/reliant/v1/approval_pb";
import type { ToolApprovalRequest } from "../../../api/client";

const mocks = vi.hoisted(() => ({
  pending: [] as ToolApprovalRequest[],
  batchApprove: vi.fn(),
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
}));

vi.mock("../../../hooks/approval-queries", () => ({
  usePendingApprovals: () => ({ data: mocks.pending }),
  useBatchApprove: () => ({ mutate: mocks.batchApprove }),
  useBatchDeny: () => ({ mutate: vi.fn() }),
  useApproveToolRequest: () => ({ mutate: vi.fn(), isPending: false }),
  useDenyToolRequest: () => ({ mutate: vi.fn(), isPending: false }),
}));

import { PermissionsPanel } from "../PermissionsPanel";

const now = new Date().toISOString();

const actionApproval: ToolApprovalRequest = {
  id: "appr-tool",
  chat_id: "chat-1",
  content_block_id: "wf:1",
  tool_name: "gmail__message_send",
  title: "Send email to ann@example.com?",
  approval_type: ApprovalType.TOOL,
  status: ApprovalStatus.PENDING,
  created_at: now,
  params: { to: ["ann@example.com"], subject: "Hi" },
  integration_name: "Gmail",
  integration_icon: "gmail",
};

const stepApproval: ToolApprovalRequest = {
  id: "appr-step",
  chat_id: "chat-1",
  content_block_id: "wf:2",
  approval_type: ApprovalType.WORKFLOW_STEP,
  title: "Deploy to production?",
  status: ApprovalStatus.PENDING,
  created_at: now,
};

function renderPanel() {
  return render(
    <SurfaceProvider surface="desktop">
      <PermissionsPanel chatId="chat-1" />
    </SurfaceProvider>,
  );
}

describe("PermissionsPanel with an integration action asking first", () => {
  it("renders the action's own card, not the batch bar", () => {
    mocks.pending = [actionApproval];
    renderPanel();
    expect(screen.getByRole("region", { name: "Send email to ann@example.com?" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /allow once/i })).toBeInTheDocument();
    expect(screen.queryByTestId("permissions-popup")).not.toBeInTheDocument();
  });

  it("keeps workflow approvals on the batch bar, which approves only them", () => {
    mocks.pending = [actionApproval, stepApproval];
    renderPanel();
    expect(screen.getByTestId("action-approval-card")).toBeInTheDocument();
    expect(screen.getByText("1 Pending")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /approve all/i }));
    expect(mocks.batchApprove).toHaveBeenCalledWith({ chatId: "chat-1", requestIds: ["appr-step"] });
  });
});
