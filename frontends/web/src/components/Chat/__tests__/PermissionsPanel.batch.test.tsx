import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ApprovalStatus, ApprovalType } from "../../../gen/reliant/v1/approval_pb";
import type { Approval } from "../../../gen/reliant/v1/approval_pb";

const mocks = vi.hoisted(() => ({
  pending: [] as Approval[],
  batchApprove: vi.fn(),
  batchDeny: vi.fn(),
  approve: vi.fn(),
  deny: vi.fn(),
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
}));

vi.mock("../../../hooks/approval-queries", () => ({
  usePendingApprovals: () => ({ data: { approvals: mocks.pending } }),
  useBatchApproveInChat: (chatId: string) => ({ mutate: (req: unknown) => mocks.batchApprove(chatId, req) }),
  useBatchDenyInChat: (chatId: string) => ({ mutate: (req: unknown) => mocks.batchDeny(chatId, req) }),
  useApproveToolRequest: (chatId: string) => ({ mutate: (req: unknown) => mocks.approve(chatId, req) }),
  useDenyToolRequest: (chatId: string) => ({ mutate: (req: unknown) => mocks.deny(chatId, req) }),
}));

import { PermissionsPanel } from "../PermissionsPanel";

const now = new Date().toISOString();

const stepApproval: Approval = {
  $typeName: "reliant.v1.Approval",
  id: "appr-step",
  chatId: "chat-1",
  entityId: "wf:2",
  approvalType: ApprovalType.WORKFLOW_STEP,
  title: "Deploy to production?",
  status: ApprovalStatus.PENDING,
  createdAt: now,
};

// A row the removed integration action approval prompt recorded: its run may
// still be waiting on it, so it must stay answerable. Its type is the reserved
// 1, which the generated enum no longer names.
const retiredToolApproval: Approval = {
  $typeName: "reliant.v1.Approval",
  id: "appr-tool",
  chatId: "chat-1",
  entityId: "wf:3",
  approvalType: 1 as ApprovalType,
  title: "Post message in #general?",
  status: ApprovalStatus.PENDING,
  createdAt: now,
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
    expect(screen.getByText("Deploy to production?")).toBeInTheDocument();
    expect(screen.getByText("Post message in #general?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /approve all/i }));
    expect(mocks.batchApprove).toHaveBeenCalledWith("chat-1", { requestIds: ["appr-step", "appr-tool"] });
    fireEvent.click(screen.getByRole("button", { name: /deny all/i }));
    expect(mocks.batchDeny).toHaveBeenCalledWith("chat-1", { requestIds: ["appr-step", "appr-tool"] });
  });

  it("keeps each of several approvals decidable on its own", () => {
    const batched = (id: string, title: string): Approval => ({
      ...stepApproval,
      id,
      title,
      approvalType: ApprovalType.TOOL_CALL,
      batchId: "run/call-1",
    });
    mocks.pending = [batched("a", "Approval: pause nightly"), batched("b", "Approval: pause weekly"), batched("c", "Approval: pause hourly")];
    renderPanel();
    expect(screen.getByText("3 Pending")).toBeInTheDocument();
    const second = within(screen.getByTestId("pending-approval-b"));
    fireEvent.click(second.getByRole("button", { name: "Approve" }));
    expect(mocks.approve).toHaveBeenCalledWith("chat-1", { requestId: "b" });
    fireEvent.click(within(screen.getByTestId("pending-approval-c")).getByRole("button", { name: "Deny" }));
    expect(mocks.deny).toHaveBeenCalledWith("chat-1", { requestId: "c" });
    fireEvent.click(screen.getByRole("button", { name: /approve all 3/i }));
    expect(mocks.batchApprove).toHaveBeenCalledWith("chat-1", { requestIds: ["a", "b", "c"] });
  });

  it("decides a single approval from the bar alone", () => {
    mocks.pending = [stepApproval];
    renderPanel();
    expect(within(screen.getByTestId("pending-approval-appr-step")).queryByRole("button")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /^approve\b(?! all)/i }));
    expect(mocks.batchApprove).toHaveBeenCalledWith("chat-1", { requestIds: ["appr-step"] });
  });
});
