/**
 * The owner's share dialog for a saved workflow: its org section says what
 * teammates get (the definition, to read and fork) and do not (running it as
 * the owner) before sharing, shares or stops sharing and shows the state the
 * server answered, and shows a refusal in words.
 */

import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ResourceVisibility } from "@/gen/reliant/v1/common_pb";

import type { ShareableWorkflow } from "../shareTargets";

const mocks = vi.hoisted(() => ({
  share: vi.fn(),
  unshare: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@/hooks/sharing-queries", () => ({
  useSetWorkflowDraftShared: () => ({ share: mocks.share, unshare: mocks.unshare, isPending: false }),
}));
vi.mock("@/hooks/useOrgs", () => ({ useActiveOrg: () => ({ orgs: [{ id: "acme", name: "Acme" }] }) }));
vi.mock("@/lib/toast-manager", () => ({
  toast: { success: mocks.toastSuccess, error: vi.fn(), info: vi.fn(), notify: vi.fn() },
}));

const { ShareWorkflowDialog, SHARED_WORKFLOW_GETS, SHARED_WORKFLOW_DOES_NOT_GET } = await import("../ShareWorkflowDialog");

function workflow(over: Partial<ShareableWorkflow> = {}): ShareableWorkflow {
  return { draftId: "d1", name: "Triage", orgId: "acme", shared: false, ...over };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.share.mockResolvedValue({ workflow: { visibility: ResourceVisibility.ORG }, changed: true });
  mocks.unshare.mockResolvedValue({ workflow: { visibility: ResourceVisibility.PRIVATE }, changed: true });
});

describe("ShareWorkflowDialog", () => {
  it("says teammates get the definition to read and fork, not running it as you, then shares", async () => {
    const onClose = vi.fn();
    render(<ShareWorkflowDialog workflow={workflow()} onClose={onClose} />);
    expect(screen.getByRole("dialog")).toHaveTextContent("Share “Triage”");
    expect(screen.getByText(/Teammates get its definition, to read and fork. They cannot run it as you./)).toBeInTheDocument();
    for (const item of [...SHARED_WORKFLOW_GETS, ...SHARED_WORKFLOW_DOES_NOT_GET]) {
      expect(screen.getByText(item)).toBeInTheDocument();
    }
    const org = screen.getByTestId("share-workflow-org");
    expect(within(org).getByTestId("share-workflow-org-state")).toHaveTextContent("Private");
    fireEvent.click(within(org).getByTestId("share-workflow-confirm"));
    await waitFor(() => expect(mocks.share).toHaveBeenCalledWith("d1"));
    expect(mocks.unshare).not.toHaveBeenCalled();
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Shared with Acme");
    // Calling the mutation isn't its completion: wait for React to commit
    // the state the server answered before asserting on the open dialog.
    await waitFor(() => expect(within(org).getByTestId("share-workflow-org-state")).toHaveTextContent("Shared"));
    expect(within(org).getByTestId("share-workflow-confirm")).toHaveTextContent("Stop sharing");
    expect(onClose).not.toHaveBeenCalled();
  });

  it("stops sharing a shared workflow", async () => {
    render(<ShareWorkflowDialog workflow={workflow({ shared: true })} onClose={vi.fn()} />);
    expect(screen.queryByText(SHARED_WORKFLOW_GETS[0])).not.toBeInTheDocument();
    fireEvent.click(screen.getByTestId("share-workflow-confirm"));
    await waitFor(() => expect(mocks.unshare).toHaveBeenCalledWith("d1"));
    expect(mocks.share).not.toHaveBeenCalled();
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Stopped sharing");
    expect(screen.getByTestId("share-workflow-org-state")).toHaveTextContent("Private");
  });

  it("closes on Done and renders nothing without a workflow", () => {
    const onClose = vi.fn();
    const { rerender } = render(<ShareWorkflowDialog workflow={workflow()} onClose={onClose} />);
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(onClose).toHaveBeenCalled();
    rerender(<ShareWorkflowDialog workflow={null} onClose={onClose} />);
    expect(screen.queryByTestId("share-workflow-org")).not.toBeInTheDocument();
  });

  it("shows a refusal in words and keeps the state", async () => {
    mocks.share.mockRejectedValueOnce(new ConnectError("server prose", Code.PermissionDenied, { "x-forge-error-reason": "owner_only" }));
    render(<ShareWorkflowDialog workflow={workflow()} onClose={vi.fn()} />);
    fireEvent.click(screen.getByTestId("share-workflow-confirm"));
    expect(await screen.findByRole("alert")).toHaveTextContent("Only its owner can share it");
    expect(screen.getByTestId("share-workflow-org-state")).toHaveTextContent("Private");
  });
});
