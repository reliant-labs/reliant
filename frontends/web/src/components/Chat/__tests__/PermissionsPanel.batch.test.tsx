import { fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ApprovalStatus, ApprovalType } from "../../../gen/reliant/v1/approval_pb";
import type { Approval } from "../../../gen/reliant/v1/approval_pb";

const mocks = vi.hoisted(() => ({
  pending: [] as Approval[],
  approve: vi.fn(),
  deny: vi.fn(),
  batchApprove: vi.fn(),
  batchDeny: vi.fn(),
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
}));

vi.mock("../../../hooks/approval-queries", () => ({
  usePendingApprovals: () => ({ data: { approvals: mocks.pending } }),
  useApproveToolRequest: (chatId: string) => ({ mutate: (req: unknown) => mocks.approve(chatId, req) }),
  useDenyToolRequest: (chatId: string) => ({ mutate: (req: unknown) => mocks.deny(chatId, req) }),
  useBatchApproveInChat: (chatId: string) => ({ mutate: (req: unknown) => mocks.batchApprove(chatId, req) }),
  useBatchDenyInChat: (chatId: string) => ({ mutate: (req: unknown) => mocks.batchDeny(chatId, req) }),
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

// An agent asking to turn on an automation (activation_card.go's layout).
const activationApproval: Approval = {
  $typeName: "reliant.v1.Approval",
  id: "appr-activation",
  chatId: "chat-1",
  entityId: "wf:4",
  approvalType: ApprovalType.WORKFLOW_STEP,
  title: [
    "Turn on PR review for acme/shopfront: it will run on each GitHub event it watches (pull_request.opened), and use GitHub as the Reliant GitHub app (a bot account).",
    "Approval needed: A new activation is new standing power.",
    "Repositories: acme/shopfront",
    "",
    "Technical details",
    "Operation: create_trigger_activation",
  ].join("\n"),
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
  beforeEach(() => {
    for (const fn of [mocks.approve, mocks.deny, mocks.batchApprove, mocks.batchDeny]) fn.mockReset();
  });

  it("answers each approval on its own, named after what it approves", () => {
    mocks.pending = [stepApproval, retiredToolApproval];
    renderPanel();
    fireEvent.click(screen.getByRole("button", { name: "Approve: Post message in #general?" }));
    expect(mocks.approve).toHaveBeenCalledWith("chat-1", { requestId: "appr-tool" });
    fireEvent.click(screen.getByRole("button", { name: "Deny: Deploy to production?" }));
    expect(mocks.deny).toHaveBeenCalledWith("chat-1", { requestId: "appr-step" });
  });

  it("offers Approve all for two or more routine approvals", () => {
    mocks.pending = [stepApproval, retiredToolApproval];
    renderPanel();
    fireEvent.click(screen.getByRole("button", { name: /approve all 2/i }));
    expect(mocks.batchApprove).toHaveBeenCalledWith("chat-1", { requestIds: ["appr-step", "appr-tool"] });
    fireEvent.click(screen.getByRole("button", { name: /deny all 2/i }));
    expect(mocks.batchDeny).toHaveBeenCalledWith("chat-1", { requestIds: ["appr-step", "appr-tool"] });
  });

  it("never puts an approval that grants power in Approve all", () => {
    mocks.pending = [stepApproval, activationApproval, retiredToolApproval];
    renderPanel();
    fireEvent.click(screen.getByRole("button", { name: /approve all 2/i }));
    expect(mocks.batchApprove).toHaveBeenCalledWith("chat-1", { requestIds: ["appr-step", "appr-tool"] });

    const card = screen.getByTestId("approval-appr-activation");
    expect(within(card).getByText(/grants an automation standing power/i)).toBeInTheDocument();
    expect(within(card).getByText("Repositories")).toBeInTheDocument();
    expect(within(card).getByText("acme/shopfront")).toBeInTheDocument();
    // The technical lines are behind a disclosure, not in the body.
    expect(within(card).getByText("Technical details").closest("details")).not.toHaveAttribute("open");
    fireEvent.click(within(card).getByRole("button", { name: /^Approve: Turn on PR review/ }));
    expect(mocks.approve).toHaveBeenCalledWith("chat-1", { requestId: "appr-activation" });
  });

  it("has no batch bar when the only other approval grants power", () => {
    mocks.pending = [stepApproval, activationApproval];
    renderPanel();
    expect(screen.queryByRole("button", { name: /approve all/i })).not.toBeInTheDocument();
    // The shortcut belongs to the routine approval, never the activation.
    expect(screen.getByRole("button", { name: "Approve: Deploy to production?" }).textContent).toMatch(/\+↵/);
    expect(screen.getByRole("button", { name: /^Approve: Turn on PR review/ }).textContent).not.toMatch(/\+↵/);
  });
});
