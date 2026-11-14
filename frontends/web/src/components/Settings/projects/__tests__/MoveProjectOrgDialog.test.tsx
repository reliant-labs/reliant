/**
 * Moving a project to another organization: the guardrails of an action that
 * moves every chat and run and changes who pays.
 *
 * Pinned here: nothing moves until the project's name is typed; running work
 * is named and "Stop all and move" stops it with the chat's own stop before it
 * moves; the move carries the org the dialog saw (expected_from_org_id) and one
 * request key; and a settled move ends in a toast that offers the switch.
 */
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ChatActivity } from "@/gen/reliant/v1/chat_pb";
import { ProjectOrgMoveState } from "@/gen/reliant/v1/project_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport, renderWithTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";

const mocks = vi.hoisted(() => ({
  preview: vi.fn(),
  move: vi.fn(),
  getMove: vi.fn(),
  terminateChat: vi.fn(),
  notify: vi.fn(),
  select: vi.fn(),
}));

vi.mock("@/hooks/useOrgs", () => ({
  useActiveOrg: () => ({
    orgs: [
      { id: "acme", name: "Acme", slug: "acme", role: "owner", isOwner: true, isDefault: true },
      { id: "globex", name: "Globex", slug: "globex", role: "member", isOwner: false, isDefault: false },
    ],
    select: mocks.select,
  }),
}));
vi.mock("@/lib/toast-manager", () => ({ toast: { notify: mocks.notify, error: vi.fn(), success: vi.fn() } }));
vi.mock("@/lib/logger", () => ({ logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() } }));

import { MoveProjectOrgDialog } from "../MoveProjectOrgDialog";

const transport = () =>
  mockTransport({
    ...serviceHandlers("ProjectService", {
      previewProjectOrgMove: (req) => mocks.preview(req),
      moveProjectToOrg: (req) => mocks.move(req),
      getProjectOrgMove: (req) => mocks.getMove(req),
    }),
    ...serviceHandlers("ChatService", { terminateChat: (req) => mocks.terminateChat(req) }),
  });

afterEach(() => setTransport(null));

const project = {
  id: "p1",
  name: "web",
  path: "/home/me/web",
  is_git_repo: true,
  is_forge: false,
  last_active: "",
  created_at: "",
  updated_at: "",
  org_id: "acme",
};

const effects = {
  chats: 12,
  runs: 40,
  triggers: 2,
  triggersToPause: 1,
  triggersNeedingSetup: [],
  sharesToClear: 0,
  orgBindingsToClear: 0,
  machines: [],
  forgeProjectName: "",
  controlPlaneEffectsKnown: false,
  hostedProjectsStaying: [],
};
const idle = { fromOrgId: "acme", toOrgId: "globex", blockers: { chats: [], chatsTotal: 0, liveRuns: 0, backgroundProcesses: 0 }, effects };
const busy = {
  ...idle,
  blockers: { chats: [{ chatId: "c1", title: "Refactor", activity: ChatActivity.RUNNING }], chatsTotal: 1, liveRuns: 1, backgroundProcesses: 0 },
};
const done = { move: { id: "m1", projectId: "p1", fromOrgId: "acme", toOrgId: "globex", state: ProjectOrgMoveState.COMPLETED } };

function renderDialog() {
  const onClose = vi.fn();
  renderWithTransport(<MoveProjectOrgDialog project={project} isOpen onClose={onClose} />, { transport: transport() });
  return { onClose };
}

describe("MoveProjectOrgDialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.preview.mockResolvedValue(idle);
    mocks.move.mockResolvedValue(done);
    mocks.terminateChat.mockResolvedValue({ success: true });
  });

  it("previews the only other org and says what moves", async () => {
    renderDialog();
    expect(await screen.findByText(/12 chats and 40 runs move with it/)).toBeInTheDocument();
    expect(mocks.preview).toHaveBeenCalledWith(expect.objectContaining({ projectId: "p1", toOrgId: "globex" }));
  });

  it("moves nothing until the project's name is typed", async () => {
    const user = userEvent.setup();
    const { onClose } = renderDialog();
    const field = await screen.findByLabelText(/to confirm/);
    const confirm = screen.getByTestId("move-project-org-confirm-button");
    expect(confirm).toBeDisabled();
    await user.type(field, "we");
    expect(confirm).toBeDisabled();
    await user.type(screen.getByLabelText(/to confirm/), "b");
    expect(confirm).toBeEnabled();
    await user.click(confirm);

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    const req = mocks.move.mock.calls[0][0];
    expect(req).toMatchObject({ projectId: "p1", toOrgId: "globex", expectedFromOrgId: "acme" });
    expect(req.requestKey).not.toBe("");
    expect(mocks.notify).toHaveBeenCalledWith(
      "Moved web to Globex",
      expect.objectContaining({ action: expect.objectContaining({ label: "Switch to Globex" }) }),
    );
    mocks.notify.mock.calls[0][1].action.onClick();
    expect(mocks.select).toHaveBeenCalledWith("globex");
  });

  it("stops the running chats, then moves", async () => {
    const user = userEvent.setup();
    mocks.preview.mockResolvedValueOnce(busy).mockResolvedValue(idle);
    renderDialog();
    expect(await screen.findByText("Refactor")).toBeInTheDocument();
    await user.type(screen.getByLabelText(/to confirm/), "web");
    await user.click(screen.getByRole("button", { name: "Stop all and move" }));

    await waitFor(() => expect(mocks.move).toHaveBeenCalled());
    expect(mocks.terminateChat).toHaveBeenCalledWith(expect.objectContaining({ chatId: "c1" }));
    expect(mocks.terminateChat.mock.invocationCallOrder[0]).toBeLessThan(mocks.move.mock.invocationCallOrder[0]);
  });

  it("does not move while work it cannot stop keeps running", async () => {
    const user = userEvent.setup();
    mocks.preview.mockResolvedValue({ ...idle, blockers: { chats: [], chatsTotal: 0, liveRuns: 0, backgroundProcesses: 2 } });
    renderDialog();
    expect(await screen.findByText(/2 background processes are still active/)).toBeInTheDocument();
    await user.type(screen.getByLabelText(/to confirm/), "web");
    await user.click(screen.getByRole("button", { name: "Stop all and move" }));
    expect(await screen.findByText(/Some work is still running/)).toBeInTheDocument();
    expect(mocks.move).not.toHaveBeenCalled();
  });

  it("follows a move that is still under way until it settles", async () => {
    const user = userEvent.setup();
    mocks.move.mockResolvedValue({ move: { ...done.move, state: ProjectOrgMoveState.PREPARING } });
    mocks.getMove.mockResolvedValue({ move: { ...done.move, state: ProjectOrgMoveState.MOVED } });
    const { onClose } = renderDialog();
    await user.type(await screen.findByLabelText(/to confirm/), "web");
    await user.click(screen.getByTestId("move-project-org-confirm-button"));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(mocks.getMove).toHaveBeenCalledWith(expect.objectContaining({ moveId: "m1" }));
  });

  it("says why an aborted move did not happen and keeps the dialog open", async () => {
    const user = userEvent.setup();
    mocks.move.mockResolvedValue({ move: { ...done.move, state: ProjectOrgMoveState.ABORTED, detail: "you are no longer an admin of Acme" } });
    const { onClose } = renderDialog();
    await user.type(await screen.findByLabelText(/to confirm/), "web");
    await user.click(screen.getByTestId("move-project-org-confirm-button"));
    expect(await screen.findByText(/not moved: you are no longer an admin of Acme/)).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
  });
});
