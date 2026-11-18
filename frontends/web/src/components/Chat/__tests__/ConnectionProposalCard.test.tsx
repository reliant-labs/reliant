import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ApprovalStatus, ApprovalType, ConnectionProposalScope } from "../../../gen/reliant/v1/approval_pb";
import type { Approval } from "../../../gen/reliant/v1/approval_pb";

const mocks = vi.hoisted(() => ({
  orgs: [] as { id: string; name: string; role: string }[],
  pending: [] as unknown[],
  approve: vi.fn(),
  deny: vi.fn(),
  batchApprove: vi.fn(),
  dialog: vi.fn(),
}));

vi.mock("../../../hooks/useOrgs", () => ({
  useActiveOrg: () => ({ orgs: mocks.orgs }),
}));

vi.mock("../../../hooks/connection-queries", () => ({
  useIntegrations: () => ({
    data: [{ id: "slack", displayName: "Slack", methods: [{ kind: "oauth2", available: true }], params: [] }],
  }),
}));

// The existing connection dialog, reduced to what the card hands it and the
// two ways it ends: a connection made, or closed without one.
vi.mock("../../workflow/connections/ConnectIntegrationDialog", () => ({
  ConnectIntegrationDialog: (props: {
    target: { integrationId: string } | null;
    owner?: { orgId: string };
    redirectAfter?: string;
    onConnected?: (c: { id: string }) => void;
    onClose: () => void;
  }) => {
    if (!props.target) return null;
    mocks.dialog(props);
    return (
      <div role="dialog">
        <button
          onClick={() => {
            props.onConnected?.({ id: "conn-1" });
            props.onClose();
          }}
        >
          Finish sign-in
        </button>
        <button onClick={props.onClose}>Close dialog</button>
      </div>
    );
  },
}));

vi.mock("../../../store/chatStoreHooks", () => ({ useActiveChatId: () => "chat-1" }));
vi.mock("../../../hooks/approval-queries", () => ({
  usePendingApprovals: () => ({ data: { approvals: mocks.pending } }),
  useApproveToolRequest: () => ({ mutate: (req: unknown) => mocks.approve(req) }),
  useDenyToolRequest: () => ({ mutate: (req: unknown) => mocks.deny(req) }),
  useBatchApproveInChat: () => ({ mutate: (req: unknown) => mocks.batchApprove(req) }),
  useBatchDenyInChat: () => ({ mutate: vi.fn() }),
}));

import { PermissionsPanel } from "../PermissionsPanel";

const CREATED_AT = "2026-11-18T12:00:00Z";

const proposalTitle = [
  "The agent asks you to connect Slack.",
  "Reason: Post the nightly report to #ops.",
  "Owner: You (a personal connection)",
  "Credential: You enter it in the connection dialog; the agent never sees it",
  "",
  "Technical details",
  "Operation: propose_connection",
  "Integration: slack",
].join("\n");

function proposal(scope: ConnectionProposalScope, orgId?: string): Approval {
  return {
    $typeName: "reliant.v1.Approval",
    id: "appr-proposal",
    chatId: "chat-1",
    entityId: "wf:1",
    approvalType: ApprovalType.TOOL_CALL,
    title: proposalTitle,
    status: ApprovalStatus.PENDING,
    createdAt: CREATED_AT,
    connectionProposal: {
      $typeName: "reliant.v1.ConnectionProposal",
      integrationId: "slack",
      integrationName: "Slack",
      scope,
      orgId,
      reason: "Post the nightly report to #ops.",
    },
  };
}

const routine: Approval = {
  $typeName: "reliant.v1.Approval",
  id: "appr-step",
  chatId: "chat-1",
  entityId: "wf:2",
  approvalType: ApprovalType.WORKFLOW_STEP,
  title: "Deploy to production?",
  status: ApprovalStatus.PENDING,
  createdAt: CREATED_AT,
};

function renderPanel(...approvals: Approval[]) {
  mocks.pending = approvals;
  return render(
    <SurfaceProvider surface="desktop">
      <PermissionsPanel />
    </SurfaceProvider>,
  );
}

describe("ConnectionProposalCard", () => {
  beforeEach(() => {
    mocks.orgs = [];
    mocks.approve.mockReset();
    mocks.deny.mockReset();
    mocks.batchApprove.mockReset();
    mocks.dialog.mockReset();
  });

  it("raises a card that opens the connection dialog, and approves with the connection made", () => {
    renderPanel(proposal(ConnectionProposalScope.PERSONAL));
    expect(screen.getByText("The agent asks you to connect Slack.")).toBeTruthy();
    expect(screen.getByText("Post the nightly report to #ops.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Approve/ })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Connect Slack…" }));
    const props = mocks.dialog.mock.lastCall?.[0];
    expect(props.target.integrationId).toBe("slack");
    expect(props.owner).toBeUndefined();
    expect(props.redirectAfter).toContain("connection_proposal=appr-proposal");

    fireEvent.click(screen.getByRole("button", { name: "Finish sign-in" }));
    expect(mocks.approve).toHaveBeenCalledWith({ requestId: "appr-proposal", connectionId: "conn-1" });
    expect(mocks.deny).not.toHaveBeenCalled();
  });

  it("answers not completed when the dialog closes without a connection", () => {
    renderPanel(proposal(ConnectionProposalScope.PERSONAL));
    fireEvent.click(screen.getByRole("button", { name: "Connect Slack…" }));
    fireEvent.click(screen.getByRole("button", { name: "Close dialog" }));
    expect(mocks.deny).toHaveBeenCalledWith(expect.objectContaining({ requestId: "appr-proposal", actionTaken: "not_completed" }));
    expect(mocks.approve).not.toHaveBeenCalled();
  });

  it("declines", () => {
    renderPanel(proposal(ConnectionProposalScope.PERSONAL));
    fireEvent.click(screen.getByRole("button", { name: "Decline connecting Slack" }));
    expect(mocks.deny).toHaveBeenCalledWith({ requestId: "appr-proposal", denialReason: "Declined" });
  });

  it("offers an org connection only to an org owner or admin", () => {
    mocks.orgs = [{ id: "org-1", name: "Acme", role: "member" }];
    const { unmount } = renderPanel(proposal(ConnectionProposalScope.UNSPECIFIED, "org-1"));
    expect(screen.queryByRole("radio")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Connect Slack…" }));
    expect(mocks.dialog.mock.lastCall?.[0].owner).toBeUndefined();
    unmount();

    mocks.orgs = [{ id: "org-1", name: "Acme", role: "admin" }];
    renderPanel(proposal(ConnectionProposalScope.UNSPECIFIED, "org-1"));
    fireEvent.click(screen.getByRole("radio", { name: "Acme's" }));
    fireEvent.click(screen.getByRole("button", { name: "Connect Slack…" }));
    expect(mocks.dialog.mock.lastCall?.[0].owner).toEqual({ orgId: "org-1", orgName: "Acme" });
  });

  it("tells a non-admin to ask an org admin when the agent asked for the org's connection", () => {
    mocks.orgs = [{ id: "org-1", name: "Acme", role: "member" }];
    renderPanel(proposal(ConnectionProposalScope.ORG, "org-1"));
    expect(screen.getByRole("note").textContent).toMatch(/Only an owner or admin of Acme can make one/);
    expect(screen.queryByRole("button", { name: "Connect Slack…" })).toBeNull();
    expect(screen.getByRole("button", { name: "Decline connecting Slack" })).toBeTruthy();
  });

  it("is never approved in Approve all", () => {
    renderPanel(proposal(ConnectionProposalScope.PERSONAL), routine, { ...routine, id: "appr-step-2" });
    fireEvent.click(screen.getByRole("button", { name: "Approve all 2" }));
    expect(mocks.batchApprove).toHaveBeenCalledWith({ requestIds: ["appr-step", "appr-step-2"] });
  });
});
