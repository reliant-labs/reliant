// Copyright (c) 2025 Reliant Labs

/**
 * The Inbox (WORKFLOW_UI.md §8): every item kind renders its inline action and
 * the action calls the right RPC; items section by kind with the verb said
 * once; every row (and every section) can be dismissed, with Undo; the scope
 * follows the current project with an All projects switch; and the loading /
 * empty / error / truncated states.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  CleanupStorageOutcome,
  CleanupStorageResponseSchema,
  InboxItemKind,
  ListInboxResponseSchema,
  type InboxItem,
} from "@/gen/reliant/v1/inbox_pb";
import { useInboxScopeStore } from "@/hooks/inbox-queries";
import { useProjectStore } from "@/store/projectStore";
import {
  approvalItem,
  failingItem,
  launchFailedItem,
  questionItem,
  renderInboxAt,
  runFinishedItem,
  storageItem,
  waitingItem,
} from "./inboxTestUtils";

const mocks = vi.hoisted(() => ({
  listInbox: vi.fn(),
  cleanupStorage: vi.fn(),
  dismissInboxItem: vi.fn(),
  restoreInboxItem: vi.fn(),
  approve: vi.fn(),
  deny: vi.fn(),
  resolveQuestion: vi.fn(),
  resumeDaemon: vi.fn(),
  setEnabled: vi.fn(),
  notify: vi.fn(),
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    inbox: () => ({
      listInbox: mocks.listInbox,
      cleanupStorage: mocks.cleanupStorage,
      dismissInboxItem: mocks.dismissInboxItem,
      restoreInboxItem: mocks.restoreInboxItem,
    }),
  },
}));
vi.mock("@/api/client", () => ({
  api: { approvals: { approve: mocks.approve, deny: mocks.deny } },
}));
vi.mock("@/api/question-grpc", () => ({
  questionGrpc: { resolveQuestion: mocks.resolveQuestion, getPendingQuestion: vi.fn() },
}));
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useResumeDaemon: (callbacks: { onError?: (error: unknown) => void } = {}) => ({
    mutate: (id: string) => {
      Promise.resolve(mocks.resumeDaemon(id)).catch((error) => callbacks.onError?.(error));
    },
    isPending: false,
    variables: undefined,
  }),
}));
vi.mock("@/hooks/trigger-queries", () => ({
  useSetTriggerEnabled: () => ({ mutate: mocks.setEnabled, isPending: false }),
}));
vi.mock("@/hooks/useGoToBilling", () => ({ useGoToBilling: () => vi.fn() }));
vi.mock("@/hooks/useTitleBarChrome", () => ({
  useTitleBarChrome: () => ({ isElectron: false, trafficLightPadding: "0", dragRegionStyle: {}, noDragRegionStyle: {} }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/lib/toast-manager", () => ({ toast: { notify: mocks.notify, error: vi.fn() } }));

import { InboxPage } from "../InboxPage";

function respond(items: InboxItem[], extra: { truncated?: boolean; otherProjectsCount?: number } = {}) {
  mocks.listInbox.mockResolvedValue(
    create(ListInboxResponseSchema, {
      items,
      blockingCount: items.filter((i) => i.kind <= 3).length,
      hasInformational: items.some((i) => i.kind >= 4),
      truncated: extra.truncated ?? false,
      otherProjectsCount: extra.otherProjectsCount ?? 0,
    }),
  );
}

function selectProject(id: string | null, name = "reliant") {
  useProjectStore.setState({
    currentProject: id ? ({ id, name } as ReturnType<typeof useProjectStore.getState>["currentProject"]) : null,
  });
}

beforeEach(() => {
  for (const mock of Object.values(mocks)) mock.mockReset();
  mocks.approve.mockResolvedValue({ success: true, message: "" });
  mocks.deny.mockResolvedValue({ success: true, message: "" });
  mocks.resolveQuestion.mockResolvedValue({});
  mocks.dismissInboxItem.mockResolvedValue({});
  mocks.restoreInboxItem.mockResolvedValue({});
  mocks.resumeDaemon.mockResolvedValue({});
  useInboxScopeStore.setState({ scope: "all" });
  selectProject(null);
});

describe("InboxPage item kinds", () => {
  it("an approval shows its tool and arguments on one line, with Approve / Deny", async () => {
    respond([approvalItem()]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-approval:appr-1");
    expect(within(row).getByText("bash")).toBeInTheDocument();
    expect(within(row).getByText(/git push origin main/)).toBeInTheDocument();

    await userEvent.click(within(row).getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(mocks.approve).toHaveBeenCalledWith("appr-1", undefined));

    await userEvent.click(within(row).getByRole("button", { name: "Deny" }));
    await waitFor(() => expect(mocks.deny).toHaveBeenCalledWith("appr-1", undefined, undefined));
  });

  it("a question shows its prompt and opens the answer form on demand", async () => {
    respond([questionItem()]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-question:q-1");
    expect(within(row).getByText("Which branch?")).toBeInTheDocument();
    expect(within(row).queryByRole("radio")).toBeNull();

    await userEvent.click(within(row).getByRole("button", { name: /Answer/ }));
    await userEvent.click(within(row).getByRole("radio", { name: /main/ }));
    await userEvent.click(within(row).getByRole("button", { name: /Submit/ }));
    await waitFor(() =>
      expect(mocks.resolveQuestion).toHaveBeenCalledWith(
        "q-1",
        "reply",
        JSON.stringify({ answers: [{ question: "Which branch?", selected: ["main"] }] }),
      ),
    );
  });

  it("waiting for machine offers Wake <machine>, which resumes that daemon", async () => {
    respond([waitingItem()]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-waiting_for_machine:chat-1@1");
    await userEvent.click(within(row).getByRole("button", { name: "Wake MacBook" }));
    expect(mocks.resumeDaemon).toHaveBeenCalledWith("d-1");
  });

  it("a refused wake shows the shared resume copy", async () => {
    respond([waitingItem()]);
    mocks.resumeDaemon.mockRejectedValue(new Error("[resource_exhausted] compute limit"));
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-waiting_for_machine:chat-1@1");
    await userEvent.click(within(row).getByRole("button", { name: "Wake MacBook" }));
    expect(await within(row).findByText(/used the compute included with your account/)).toBeInTheDocument();
  });

  it("a failing automation shows its streak and reason, and offers Open last run and Pause", async () => {
    respond([failingItem()]);
    const { router } = renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-automation_failing:evt-9");
    expect(within(row).getByText("3 in a row · tool error")).toBeInTheDocument();

    await userEvent.click(within(row).getByRole("button", { name: "Pause" }));
    expect(mocks.setEnabled).toHaveBeenCalledWith({ id: "trg-1", enabled: false }, expect.anything());

    await userEvent.click(within(row).getByRole("link", { name: "Open last run" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflows/runs/chat-last"));
  });

  it("a failed launch shows the reason, a single failure no count, and Edit automation", async () => {
    respond([launchFailedItem()]);
    const { router } = renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-automation_launch_failed:evt-5");
    expect(within(row).getByText("Machine was deleted")).toBeInTheDocument();
    expect(within(row).queryByText(/in a row/)).toBeNull();
    await userEvent.click(within(row).getByRole("link", { name: "Edit automation" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflows/automations/trg-2"));
  });

  it("a finished run opens its run", async () => {
    respond([runFinishedItem()]);
    const { router } = renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-run_finished:chat-1");
    expect(within(row).getByLabelText("Run finished")).toBeInTheDocument();
    await userEvent.click(within(row).getByRole("link", { name: "Open" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflows/runs/chat-1"));
  });

  it("an approval already handled elsewhere says so instead of erroring", async () => {
    respond([approvalItem()]);
    mocks.approve.mockRejectedValue(new ConnectError("approval already processed", Code.FailedPrecondition));
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-approval:appr-1");
    await userEvent.click(within(row).getByRole("button", { name: "Approve" }));
    expect(await within(row).findByText("Already handled")).toBeInTheDocument();
  });
});

describe("InboxPage layout", () => {
  it("sections by kind in priority order and says the verb once, not per row", async () => {
    respond([
      approvalItem({}, "appr-1"),
      questionItem(),
      questionItem({ itemId: "question:q-2", chatId: "chat-2", runId: "wf-2", chatTitle: "Other run" }),
      launchFailedItem(),
    ]);
    renderInboxAt(<InboxPage />);
    const questions = await screen.findByTestId(`inbox-section-${InboxItemKind.QUESTION}`);
    expect(within(questions).getByRole("heading", { name: /Questions\s*2/ })).toBeInTheDocument();
    expect(within(questions).getAllByTestId(/^inbox-item-/)).toHaveLength(2);
    expect(screen.queryByText(/Answer a question in/)).toBeNull();

    const headings = screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent);
    expect(headings).toEqual(["Approvals1", "Questions2", "Automations that could not start1"]);
  });

  it("a row's name opens the run by its chat id", async () => {
    respond([waitingItem({ chatId: "chat-9", runId: "wf-root-9", itemId: "waiting_for_machine:chat-9@1" })]);
    const { router } = renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-waiting_for_machine:chat-9@1");
    await userEvent.click(within(row).getByRole("link", { name: "Nightly triage" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflows/runs/chat-9"));
  });

  it("a row is one dense line: compact age shown, the long form for assistive tech", async () => {
    respond([approvalItem({ waitingSince: new Date(Date.now() - 3 * 60 * 60_000).toISOString() })]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-approval:appr-1");
    const time = row.querySelector("time")!;
    expect(within(time).getByText("3h")).toHaveAttribute("aria-hidden", "true");
    expect(within(time).getByText(/waiting since 3 hours ago/)).toHaveClass("sr-only");
    // The answer form and other tall content are not rendered until asked for.
    expect(row.querySelectorAll("textarea, input")).toHaveLength(0);
  });

  it("an automation item without a run links to the automation", async () => {
    respond([launchFailedItem()]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-automation_launch_failed:evt-5");
    expect(within(row).getByRole("link", { name: "Weekly report" })).toHaveAttribute(
      "href",
      "/workflows/automations/trg-2",
    );
  });
});

describe("InboxPage dismiss", () => {
  it("every kind can be dismissed; the row leaves at once and Undo restores it", async () => {
    respond([approvalItem(), launchFailedItem()]);
    renderInboxAt(<InboxPage />);
    const approval = await screen.findByTestId("inbox-item-approval:appr-1");
    expect(within(screen.getByTestId("inbox-item-automation_launch_failed:evt-5")).getByRole("button", { name: /Dismiss/ })).toBeInTheDocument();

    respond([launchFailedItem()]);
    await userEvent.click(within(approval).getByRole("button", { name: "Dismiss Nightly triage" }));
    expect(mocks.dismissInboxItem.mock.calls[0]![0]).toMatchObject({ itemIds: ["approval:appr-1"] });
    await waitFor(() => expect(screen.queryByTestId("inbox-item-approval:appr-1")).toBeNull());

    await waitFor(() => expect(mocks.notify).toHaveBeenCalled());
    const [message, options] = mocks.notify.mock.calls[0]!;
    expect(message).toBe("Dismissed “Nightly triage”");
    options.action.onClick();
    await waitFor(() =>
      expect(mocks.restoreInboxItem.mock.calls[0]![0]).toMatchObject({ itemIds: ["approval:appr-1"] }),
    );
  });

  it("Dismiss all hides a whole section in one call", async () => {
    respond([
      questionItem(),
      questionItem({ itemId: "question:q-2", chatId: "chat-2", runId: "wf-2", chatTitle: "Other run" }),
      approvalItem(),
    ]);
    renderInboxAt(<InboxPage />);
    const questions = await screen.findByTestId(`inbox-section-${InboxItemKind.QUESTION}`);
    respond([approvalItem()]);
    await userEvent.click(within(questions).getByRole("button", { name: "Dismiss all" }));
    expect(mocks.dismissInboxItem.mock.calls[0]![0]).toMatchObject({ itemIds: ["question:q-1", "question:q-2"] });
    await waitFor(() => expect(screen.queryByTestId(`inbox-section-${InboxItemKind.QUESTION}`)).toBeNull());
    // A section of one has nothing to "dismiss all" of.
    expect(
      within(screen.getByTestId(`inbox-section-${InboxItemKind.APPROVAL}`)).queryByRole("button", { name: "Dismiss all" }),
    ).toBeNull();
  });
});

describe("InboxPage scope", () => {
  it("defaults to the current project, hides the project name, and switches to every project", async () => {
    useInboxScopeStore.setState({ scope: "project" });
    selectProject("proj-1", "reliant");
    respond([approvalItem()], { otherProjectsCount: 3 });
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-approval:appr-1");
    expect(mocks.listInbox.mock.calls[0]![0]).toMatchObject({ projectId: "proj-1" });
    expect(within(row).queryByText("reliant")).toBeNull();
    expect(screen.getByRole("radio", { name: "reliant" })).toHaveAttribute("aria-checked", "true");

    respond([approvalItem()]);
    await userEvent.click(screen.getByRole("button", { name: "3 more in other projects" }));
    await waitFor(() => expect(mocks.listInbox.mock.calls.at(-1)![0].projectId).toBeUndefined());
    expect(screen.getByRole("radio", { name: "All projects" })).toHaveAttribute("aria-checked", "true");
    expect(await within(screen.getByTestId("inbox-item-approval:appr-1")).findByText("reliant")).toBeInTheDocument();
  });

  it("an empty project says what waits elsewhere", async () => {
    useInboxScopeStore.setState({ scope: "project" });
    selectProject("proj-1");
    respond([], { otherProjectsCount: 2 });
    renderInboxAt(<InboxPage />);
    expect(await screen.findByText("Nothing needs you in this project.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "2 waiting in other projects" })).toBeInTheDocument();
  });

  it("with no current project there is no toggle and every project is listed", async () => {
    useInboxScopeStore.setState({ scope: "project" });
    respond([approvalItem()]);
    renderInboxAt(<InboxPage />);
    await screen.findByTestId("inbox-item-approval:appr-1");
    expect(mocks.listInbox.mock.calls[0]![0].projectId).toBeUndefined();
    expect(screen.queryByRole("radiogroup")).toBeNull();
  });
});

describe("InboxPage states", () => {
  it("loading shows skeleton rows", async () => {
    mocks.listInbox.mockReturnValue(new Promise(() => {}));
    renderInboxAt(<InboxPage />);
    expect(await screen.findByLabelText("Loading inbox")).toBeInTheDocument();
  });

  it("empty is calm: no call to action", async () => {
    respond([]);
    renderInboxAt(<InboxPage />);
    expect(await screen.findByText("Nothing needs you.")).toBeInTheDocument();
    expect(screen.getByText("Approvals, questions and automation problems show up here.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Try again/ })).toBeNull();
  });

  it("error shows an alert with Try again", async () => {
    mocks.listInbox.mockRejectedValue(new Error("boom"));
    renderInboxAt(<InboxPage />);
    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText("The inbox could not be loaded.")).toBeInTheDocument();
    expect(within(alert).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("truncated says more are waiting", async () => {
    respond([approvalItem()], { truncated: true });
    renderInboxAt(<InboxPage />);
    expect(await screen.findByText(/more items are waiting/i)).toBeInTheDocument();
  });
});


describe("InboxPage storage item", () => {
  it("shows the machine's disk and what it kept, under its own heading", async () => {
    respond([storageItem()]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-storage:d-1:ab12cd34");
    expect(screen.getByTestId(`inbox-section-${InboxItemKind.STORAGE}`)).toHaveTextContent(/^Storage1/);
    expect(within(row).getByText("MacBook storage")).toBeInTheDocument();
    expect(within(row).getByText(/12 GB free of 500 GB/)).toBeInTheDocument();
    expect(within(row).getByText(/3 workspaces kept \(3\.3 GB\)/)).toBeInTheDocument();
  });

  it("sorts after the blocking kinds and does not count as blocking", async () => {
    respond([storageItem(), approvalItem()]);
    renderInboxAt(<InboxPage />);
    await screen.findByTestId("inbox-item-storage:d-1:ab12cd34");
    const headings = screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent ?? "");
    expect(headings.findIndex((t) => t.startsWith("Approvals"))).toBeGreaterThanOrEqual(0);
    expect(headings.findIndex((t) => t.startsWith("Approvals"))).toBeLessThan(
      headings.findIndex((t) => t.startsWith("Storage")),
    );
  });

  it("Clean up lists exactly what will be removed and calls nothing until confirmed", async () => {
    respond([storageItem()]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-storage:d-1:ab12cd34");
    await userEvent.click(within(row).getByRole("button", { name: "Clean up" }));

    const dialog = await screen.findByRole("dialog");
    const list = within(dialog).getByRole("list", { name: "Workspaces to remove" });
    expect(within(list).getByText("fix-login")).toBeInTheDocument();
    expect(within(list).getByText("spike")).toBeInTheDocument();
    expect(within(list).getByText(/Uncommitted changes/)).toBeInTheDocument();
    expect(within(list).getByText(/Commits not pushed/)).toBeInTheDocument();
    expect(within(dialog).getByText(/frees about/i)).toHaveTextContent("3.2 GB");
    expect(within(dialog).getByText(/nothing is pushed and no branch is deleted/i)).toBeInTheDocument();
    // B2: a workspace holding data is listed, but under "remove manually", and
    // is not in the list of things that will be removed.
    expect(within(list).queryByText("taxes")).not.toBeInTheDocument();
    const manual = within(dialog).getByRole("list", { name: "Workspaces to remove manually" });
    expect(within(manual).getByText("taxes")).toBeInTheDocument();
    expect(within(manual).getByText(/Remove it manually/)).toBeInTheDocument();
    expect(within(dialog).getByText(/Ignored files that are only build output/i)).toBeInTheDocument();
    expect(mocks.cleanupStorage).not.toHaveBeenCalled();
  });

  it("Cancel makes no call", async () => {
    respond([storageItem()]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-storage:d-1:ab12cd34");
    await userEvent.click(within(row).getByRole("button", { name: "Clean up" }));
    await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Cancel" }));
    expect(mocks.cleanupStorage).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("confirming starts the clean-up for exactly the removable workspaces, never the manual ones", async () => {
    mocks.cleanupStorage.mockResolvedValue(
      create(CleanupStorageResponseSchema, {
        results: [
          { worktreeId: "wt-1", outcome: CleanupStorageOutcome.ACCEPTED },
          { worktreeId: "wt-2", outcome: CleanupStorageOutcome.ACCEPTED },
        ],
      }),
    );
    respond([storageItem()]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-storage:d-1:ab12cd34");
    await userEvent.click(within(row).getByRole("button", { name: "Clean up" }));
    await userEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Remove 2 workspaces" }));

    await waitFor(() => expect(mocks.cleanupStorage).toHaveBeenCalledTimes(1));
    const request = mocks.cleanupStorage.mock.calls[0][0];
    expect(request.daemonId).toBe("d-1");
    expect(request.worktreeIds).toEqual(["wt-1", "wt-2"]);
    expect(request.worktreeIds).not.toContain("wt-3");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("while a clean-up runs, Clean up is disabled and says so", async () => {
    respond([storageItem({}, { held: [{ worktreeId: "wt-1", name: "fix", removable: true, cleaning: true, reason: "dirty", sizeBytes: 1n } as never] })]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-storage:d-1:ab12cd34");
    expect(within(row).getByRole("button", { name: "Cleaning up…" })).toBeDisabled();
  });

  it("when nothing is removable, Clean up is disabled and explains why", async () => {
    respond([storageItem({}, { held: [{ worktreeId: "wt-3", name: "taxes", removable: false, reason: "data", sizeBytes: 1n } as never] })]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-storage:d-1:ab12cd34");
    const button = within(row).getByRole("button", { name: "Clean up" });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("title", expect.stringMatching(/remove it manually/i));
  });

  it("an offline machine's Clean up is disabled", async () => {
    respond([storageItem({}, { online: false })]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-storage:d-1:ab12cd34");
    expect(within(row).getByRole("button", { name: "Clean up" })).toBeDisabled();
    expect(within(row).getByText(/offline/)).toBeInTheDocument();
  });

  it("a disk-low item with nothing held has no Clean up action", async () => {
    respond([storageItem({}, { held: [] })]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-storage:d-1:ab12cd34");
    expect(within(row).queryByRole("button", { name: "Clean up" })).not.toBeInTheDocument();
  });
});
