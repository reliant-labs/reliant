import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ApprovalStatus, ApprovalType } from "../../../gen/reliant/v1/approval_pb";
import type { ToolApprovalRequest } from "../../../api/client";

const mocks = vi.hoisted(() => ({
  pending: [] as ToolApprovalRequest[],
  batchApprove: vi.fn(),
  batchDeny: vi.fn(),
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
}));

vi.mock("../../../hooks/approval-queries", () => ({
  usePendingApprovals: () => ({ data: mocks.pending }),
  useBatchApprove: () => ({ mutate: mocks.batchApprove }),
  useBatchDeny: () => ({ mutate: mocks.batchDeny }),
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

// A row the removed integration action approval prompt recorded: its run may
// still be waiting on it, so it must stay answerable. Its type is the reserved
// 1, which the generated enum no longer names.
const retiredToolApproval: ToolApprovalRequest = {
  id: "appr-tool",
  chat_id: "chat-1",
  content_block_id: "wf:3",
  approval_type: 1 as ApprovalType,
  title: "Post message in #general?",
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
  it("answers every pending approval from the batch bar", () => {
    mocks.pending = [stepApproval, retiredToolApproval];
    renderPanel();
    expect(screen.getByText("2 Pending")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /approve all/i }));
    expect(mocks.batchApprove).toHaveBeenCalledWith({ chatId: "chat-1", requestIds: ["appr-step", "appr-tool"] });
    fireEvent.click(screen.getByRole("button", { name: /deny all/i }));
    expect(mocks.batchDeny).toHaveBeenCalledWith({ chatId: "chat-1", requestIds: ["appr-step", "appr-tool"] });
  });
});
