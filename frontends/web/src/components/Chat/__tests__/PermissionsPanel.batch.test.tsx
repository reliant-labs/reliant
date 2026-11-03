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

// The plain-text form servers wrote before the card: no plain-words summary
// of what it grants, so it cannot be shown in a batch confirmation.
const legacyActivationApproval: Approval = {
  ...activationApproval,
  id: "appr-legacy",
  title: [
    "Approval: A new activation is new standing power.",
    "Machine: laptop (id d_1)",
    "Operation: enable_trigger",
    'Activation: {"name":"nightly"}',
  ].join("\n"),
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

  it("puts an activation card in Approve all, after a confirmation that shows what it grants", () => {
    mocks.pending = [stepApproval, activationApproval, retiredToolApproval];
    renderPanel();

    // The card is still read and decidable on its own.
    const card = screen.getByTestId("approval-appr-activation");
    expect(within(card).getByText(/grants an automation standing power/i)).toBeInTheDocument();
    expect(within(card).getByText("Repositories")).toBeInTheDocument();
    expect(within(card).getByText("Technical details").closest("details")).not.toHaveAttribute("open");

    fireEvent.click(screen.getByRole("button", { name: /approve all 3/i }));
    // Nothing is approved until the confirmation has shown the grant summary.
    expect(mocks.batchApprove).not.toHaveBeenCalled();
    const confirm = within(screen.getByTestId("approval-batch-confirm"));
    const grant = within(screen.getByTestId("batch-grant-appr-activation"));
    expect(grant.getByText(/^Turn on PR review for acme\/shopfront/)).toBeInTheDocument();
    expect(grant.getByText("Repositories")).toBeInTheDocument();
    expect(grant.getByText("acme/shopfront")).toBeInTheDocument();
    expect(grant.getByText("Approval needed")).toBeInTheDocument();
    expect(confirm.getByText("And 2 routine requests")).toBeInTheDocument();
    expect(confirm.getByText("Deploy to production?")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Approve 3" }));
    expect(mocks.batchApprove).toHaveBeenCalledWith("chat-1", {
      requestIds: ["appr-step", "appr-activation", "appr-tool"],
    });
  });

  it("cancelling the confirmation approves nothing", () => {
    mocks.pending = [stepApproval, activationApproval];
    renderPanel();
    fireEvent.click(screen.getByRole("button", { name: /approve all 2/i }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByTestId("approval-batch-confirm")).not.toBeInTheDocument();
    expect(mocks.batchApprove).not.toHaveBeenCalled();
  });

  it("denies a batch with activation cards at once: denying grants nothing", () => {
    mocks.pending = [stepApproval, activationApproval];
    renderPanel();
    fireEvent.click(screen.getByRole("button", { name: /deny all 2/i }));
    expect(mocks.batchDeny).toHaveBeenCalledWith("chat-1", { requestIds: ["appr-step", "appr-activation"] });
  });

  it("keeps an old-format activation approval out of Approve all, and says why", () => {
    mocks.pending = [stepApproval, activationApproval, legacyActivationApproval, retiredToolApproval];
    renderPanel();
    const legacy = within(screen.getByTestId("approval-appr-legacy"));
    expect(legacy.getByText(/^Not in Approve all: this request was raised before approval cards said/)).toBeInTheDocument();
    expect(screen.getByText(/1 decided on its own/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /approve all 3/i }));
    expect(screen.queryByTestId("batch-grant-appr-legacy")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Approve 3" }));
    expect(mocks.batchApprove).toHaveBeenCalledWith("chat-1", {
      requestIds: ["appr-step", "appr-activation", "appr-tool"],
    });

    // It is still decided on its own.
    fireEvent.click(legacy.getByRole("button", { name: /^Approve: Approval: A new activation/ }));
    expect(mocks.approve).toHaveBeenCalledWith("chat-1", { requestId: "appr-legacy" });
  });

  it("has no batch bar when the only other approval is an old-format activation", () => {
    mocks.pending = [stepApproval, legacyActivationApproval];
    renderPanel();
    expect(screen.queryByRole("button", { name: /approve all/i })).not.toBeInTheDocument();
    // The shortcut belongs to the approval it would approve, never the old card.
    expect(screen.getByRole("button", { name: "Approve: Deploy to production?" }).textContent).toMatch(/\+↵/);
    expect(screen.getByRole("button", { name: /^Approve: Approval: A new activation/ }).textContent).not.toMatch(/\+↵/);
  });
});
