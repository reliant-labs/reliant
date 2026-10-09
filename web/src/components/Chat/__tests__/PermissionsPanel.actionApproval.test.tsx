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
}));

import { PermissionsPanel } from "../PermissionsPanel";

const now = new Date().toISOString();

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

describe("PermissionsPanel", () => {
  it("keeps workflow approvals on the batch bar", () => {
    mocks.pending = [stepApproval];
    renderPanel();
    expect(screen.getByText("1 Pending")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /approve all/i }));
    expect(mocks.batchApprove).toHaveBeenCalledWith({ chatId: "chat-1", requestIds: ["appr-step"] });
  });
});
