/**
 * The owner's share toggle for a project: it says what the org's members will
 * and will not see before sharing (the viewer's shape of a project, and that
 * no chat is shared with it), shares or stops sharing, and shows a refusal
 * in words.
 */

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Project } from "@/store/projectStore";

const mocks = vi.hoisted(() => ({
  share: vi.fn(async () => ({})),
  unshare: vi.fn(async () => ({})),
  toastSuccess: vi.fn(),
}));

vi.mock("@/hooks/sharing-queries", () => ({
  useSetProjectShared: () => ({ share: mocks.share, unshare: mocks.unshare, isPending: false }),
}));
vi.mock("@/lib/toast-manager", () => ({
  toast: { success: mocks.toastSuccess, error: vi.fn(), info: vi.fn(), notify: vi.fn() },
}));

const { ShareProjectDialog, SHARED_PROJECT_SEES, SHARED_PROJECT_DOES_NOT_SEE } = await import("../ShareProjectDialog");

function project(over: Partial<Project> = {}): Project {
  return {
    id: "p1",
    name: "shop",
    path: "/home/me/shop",
    is_git_repo: true,
    is_forge: false,
    last_active: "2026-01-01T00:00:00Z",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    org_id: "acme",
    ...over,
  };
}

beforeEach(() => vi.clearAllMocks());

describe("ShareProjectDialog", () => {
  it("discloses what members will and will not see, then shares", async () => {
    const onClose = vi.fn();
    render(<ShareProjectDialog project={project()} orgName="Acme" open onClose={onClose} />);
    for (const item of [...SHARED_PROJECT_SEES, ...SHARED_PROJECT_DOES_NOT_SEE]) {
      expect(screen.getByText(item)).toBeInTheDocument();
    }
    // Sharing the project shares none of its chats.
    expect(screen.getByText("Any chat in it you have not shared")).toBeInTheDocument();
    expect(screen.getByText(/Sharing the project shares none of its chats/)).toBeInTheDocument();
    fireEvent.click(screen.getByTestId("share-project-confirm"));
    await waitFor(() => expect(mocks.share).toHaveBeenCalledWith("p1"));
    expect(mocks.unshare).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalled();
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Shared with Acme");
  });

  it("stops sharing a shared project", async () => {
    render(<ShareProjectDialog project={project({ visibility: "org" })} orgName="Acme" open onClose={vi.fn()} />);
    expect(screen.queryByText(SHARED_PROJECT_SEES[0])).not.toBeInTheDocument();
    expect(screen.getByTestId("share-project-confirm")).toHaveTextContent("Stop sharing");
    fireEvent.click(screen.getByTestId("share-project-confirm"));
    await waitFor(() => expect(mocks.unshare).toHaveBeenCalledWith("p1"));
    expect(mocks.share).not.toHaveBeenCalled();
  });

  it.each([
    ["no_org", Code.FailedPrecondition, "belongs to no organization"],
    ["owner_only", Code.PermissionDenied, "Only its owner can share it"],
    ["person_required", Code.PermissionDenied, "Sign in to the Reliant app"],
  ])("shows a %s refusal in words and stays open", async (reason, code, words) => {
    mocks.share.mockRejectedValueOnce(new ConnectError("server prose", code, { "x-forge-error-reason": reason }));
    const onClose = vi.fn();
    render(<ShareProjectDialog project={project()} orgName="Acme" open onClose={onClose} />);
    fireEvent.click(screen.getByTestId("share-project-confirm"));
    expect(await screen.findByRole("alert")).toHaveTextContent(words);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("shows the server's words for a refusal it has no sentence for", async () => {
    mocks.share.mockRejectedValueOnce(new ConnectError("could not verify organization membership; try again", Code.Unavailable));
    render(<ShareProjectDialog project={project()} orgName="Acme" open onClose={vi.fn()} />);
    fireEvent.click(screen.getByTestId("share-project-confirm"));
    expect(await screen.findByRole("alert")).toHaveTextContent("could not verify organization membership; try again");
  });
});
