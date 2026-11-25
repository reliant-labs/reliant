// Copyright (c) 2025 Reliant Labs

/**
 * Merging one project into another: the guardrails of an action that moves
 * every chat, workspace and checkout and schedules the source for deletion.
 *
 * Pinned here: the dialog previews the pair it names and says what moves (by
 * machine name for checkouts); the server's blockers are shown in its words
 * and keep Merge disabled; Merge sends the same pair and ends in a toast; a
 * refused merge shows the server's message, not Connect's framing; Escape
 * cancels.
 */
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { HostedAppsState, ProjectInstallState, ProjectMergeBlockerKind, ProjectSchema } from "@/gen/reliant/v1/project_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport, renderWithTransport } from "@/lib/test-utils";
import { catalogFromResponse, type ProjectCatalogRow } from "@/services/projects/catalog";
import { serviceHandlers } from "@/test/serviceTransport";

const mocks = vi.hoisted(() => ({
  preview: vi.fn(),
  merge: vi.fn(),
  success: vi.fn(),
}));

vi.mock("@/lib/toast-manager", () => ({ toast: { success: mocks.success, error: vi.fn(), notify: vi.fn() } }));
vi.mock("@/lib/logger", () => ({ logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() } }));

import { MergeProjectsDialog } from "../MergeProjectsDialog";

const transport = () =>
  mockTransport(
    serviceHandlers("ProjectService", {
      previewProjectMerge: (req) => mocks.preview(req),
      mergeProjects: (req) => mocks.merge(req),
    }),
  );

afterEach(() => setTransport(null));

function rows(): [ProjectCatalogRow, ProjectCatalogRow] {
  const project = (id: string, machine: string) => ({
    project: create(ProjectSchema, { id, name: "shop", path: `/home/me/${id}`, remoteKey: "github.com/acme/shop", orgId: "org-1" }),
    repos: [],
    apps: [],
    checkouts: [{ daemonId: `d-${id}`, daemonName: machine, installState: ProjectInstallState.INSTALLED }],
  });
  const catalog = catalogFromResponse({
    orgId: "org-1",
    hostedAppsState: HostedAppsState.OK,
    projects: [project("p-laptop", "laptop"), project("p-cloud", "cloud-1")],
    unlinkedApps: [],
  });
  return catalog.rows as [ProjectCatalogRow, ProjectCatalogRow];
}

const counts = { chats: 4, worktrees: 2, checkouts: [{ daemonId: "d-p-laptop", daemonName: "laptop" }], triggers: 1, repos: 1, otherRows: 6 };
const clear = { counts, blockers: [] };
const busy = {
  counts,
  blockers: [{ kind: ProjectMergeBlockerKind.RUNNING_WORK, message: "A chat in shop is running. Stop it, then merge.", chatId: "c1" }],
};

function renderDialog() {
  const onClose = vi.fn();
  const [source, target] = rows();
  renderWithTransport(<MergeProjectsDialog source={source} target={target} onClose={onClose} />, { transport: transport() });
  return { onClose };
}

describe("MergeProjectsDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.preview.mockResolvedValue(clear);
    mocks.merge.mockResolvedValue({ project: { id: "p-cloud", name: "shop" }, counts });
  });

  it("previews the pair and says what moves, where the source is, and what happens to it", async () => {
    renderDialog();
    expect(screen.getByRole("dialog", { name: "Merge shop into shop?" })).toBeInTheDocument();
    const moves = await screen.findByTestId("merge-projects-moves");
    expect(mocks.preview).toHaveBeenCalledWith(expect.objectContaining({ sourceProjectId: "p-laptop", targetProjectId: "p-cloud" }));
    expect(moves).toHaveTextContent("4 chats");
    expect(moves).toHaveTextContent("2 workspaces");
    expect(moves).toHaveTextContent("Its checkout on laptop, which becomes shop’s at the same path");
    expect(moves).toHaveTextContent("1 automation");
    expect(moves).toHaveTextContent("1 repository");
    expect(moves).toHaveTextContent("6 other records");
    // The two same-named projects are told apart by their machines.
    expect(screen.getByText("On laptop")).toBeInTheDocument();
    expect(screen.getByText("On cloud-1")).toBeInTheDocument();
    expect(screen.getByTestId("merge-projects-after")).toHaveTextContent(
      "shop is then left empty and scheduled for deletion: it leaves your projects, and can be restored, empty, for 7 days.",
    );
    expect(screen.getByTestId("merge-projects-confirm")).toBeEnabled();
  });

  it("merges the pair it previewed, says so, and closes", async () => {
    const user = userEvent.setup();
    const { onClose } = renderDialog();
    await screen.findByTestId("merge-projects-moves");
    await user.click(screen.getByTestId("merge-projects-confirm"));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(mocks.merge).toHaveBeenCalledWith(expect.objectContaining({ sourceProjectId: "p-laptop", targetProjectId: "p-cloud" }));
    expect(mocks.success).toHaveBeenCalledWith("Merged shop into shop");
  });

  it("shows what refuses the merge in the server's words and keeps Merge disabled", async () => {
    mocks.preview.mockResolvedValue(busy);
    renderDialog();
    const blockers = await screen.findByTestId("merge-projects-blockers");
    expect(blockers).toHaveTextContent("A chat in shop is running. Stop it, then merge.");
    expect(screen.getByTestId("merge-projects-confirm")).toBeDisabled();
    fireEvent.click(screen.getByTestId("merge-projects-confirm"));
    expect(mocks.merge).not.toHaveBeenCalled();
  });

  it("shows the server's message when the merge is refused, and asks for a fresh preview", async () => {
    const user = userEvent.setup();
    mocks.merge.mockRejectedValue(new ConnectError("Both projects are checked out on laptop. Detach one first.", Code.FailedPrecondition));
    const { onClose } = renderDialog();
    await screen.findByTestId("merge-projects-moves");
    await user.click(screen.getByTestId("merge-projects-confirm"));
    const error = await screen.findByTestId("merge-projects-error");
    expect(error).toHaveTextContent("Both projects are checked out on laptop. Detach one first.");
    expect(error).not.toHaveTextContent("failed_precondition");
    expect(onClose).not.toHaveBeenCalled();
    expect(mocks.success).not.toHaveBeenCalled();
    await waitFor(() => expect(mocks.preview).toHaveBeenCalledTimes(2));
  });

  it("says when the preview could not be read, and offers no merge", async () => {
    mocks.preview.mockRejectedValue(new ConnectError("project not found", Code.NotFound));
    renderDialog();
    expect(await screen.findByTestId("merge-projects-preview-error")).toHaveTextContent("project not found");
    expect(screen.getByTestId("merge-projects-confirm")).toBeDisabled();
  });

  it("cancels on Escape", async () => {
    const { onClose } = renderDialog();
    await screen.findByTestId("merge-projects-moves");
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    expect(onClose).toHaveBeenCalled();
  });
});
