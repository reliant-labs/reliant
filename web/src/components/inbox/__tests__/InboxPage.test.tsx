// Copyright (c) 2025 Reliant Labs

/**
 * The Inbox (WORKFLOW_UI.md §8): every item kind renders its inline action and
 * the action calls the right RPC; items of one run group; clicking a row opens
 * run detail; dismiss removes a failure; and the loading / empty / error /
 * truncated states.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { ListInboxResponseSchema, type InboxItem } from "@/gen/reliant/v1/inbox_pb";
import {
  approvalItem,
  failingItem,
  launchFailedItem,
  questionItem,
  renderInboxAt,
  runFinishedItem,
  waitingItem,
} from "./inboxTestUtils";

const mocks = vi.hoisted(() => ({
  listInbox: vi.fn(),
  dismissInboxItem: vi.fn(),
  approve: vi.fn(),
  deny: vi.fn(),
  resolveQuestion: vi.fn(),
  resumeDaemon: vi.fn(),
  setEnabled: vi.fn(),
}));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: { inbox: () => ({ listInbox: mocks.listInbox, dismissInboxItem: mocks.dismissInboxItem }) },
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

import { InboxPage } from "../InboxPage";

function respond(items: InboxItem[], extra: { truncated?: boolean } = {}) {
  mocks.listInbox.mockResolvedValue(
    create(ListInboxResponseSchema, {
      items,
      blockingCount: items.filter((i) => i.kind <= 3).length,
      hasInformational: items.some((i) => i.kind >= 4),
      truncated: extra.truncated ?? false,
    }),
  );
}

beforeEach(() => {
  for (const mock of Object.values(mocks)) mock.mockReset();
  mocks.approve.mockResolvedValue({ success: true, message: "" });
  mocks.deny.mockResolvedValue({ success: true, message: "" });
  mocks.resolveQuestion.mockResolvedValue({});
  mocks.dismissInboxItem.mockResolvedValue({});
  mocks.resumeDaemon.mockResolvedValue({});
});

describe("InboxPage item kinds", () => {
  it("an approval shows its tool, argument summary and Approve / Deny", async () => {
    respond([approvalItem()]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-appr-1");
    expect(within(row).getByText("git push origin main")).toBeInTheDocument();
    expect(within(row).getByText("bash")).toBeInTheDocument();

    await userEvent.click(within(row).getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(mocks.approve).toHaveBeenCalledWith("appr-1", undefined));

    await userEvent.click(within(row).getByRole("button", { name: "Deny" }));
    await waitFor(() => expect(mocks.deny).toHaveBeenCalledWith("appr-1", undefined, undefined));
  });

  it("a question renders the answer form inline and replies with the answers", async () => {
    respond([questionItem()]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-q-1");
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
    const row = await screen.findByTestId("inbox-item-chat-1");
    await userEvent.click(within(row).getByRole("button", { name: "Wake MacBook" }));
    expect(mocks.resumeDaemon).toHaveBeenCalledWith("d-1");
  });

  it("a refused wake shows the shared resume copy", async () => {
    respond([waitingItem()]);
    mocks.resumeDaemon.mockRejectedValue(new Error("[resource_exhausted] compute limit"));
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-chat-1");
    await userEvent.click(within(row).getByRole("button", { name: "Wake MacBook" }));
    expect(await within(row).findByText(/used the compute included with your account/)).toBeInTheDocument();
  });

  it("a failing automation offers Open last run and Pause automation", async () => {
    respond([failingItem()]);
    const { router } = renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-failing:trg-1:evt-9");
    expect(within(row).getByText(/Failed 3 times in a row/)).toBeInTheDocument();

    await userEvent.click(within(row).getByRole("button", { name: "Pause automation" }));
    expect(mocks.setEnabled).toHaveBeenCalledWith({ id: "trg-1", enabled: false }, expect.anything());

    await userEvent.click(within(row).getByRole("link", { name: "Open last run" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/runs/chat-last"));
  });

  it("a repeated failing automation shows one row counting the streak", async () => {
    respond([failingItem()]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-failing:trg-1:evt-9");
    expect(within(row).getByText("Failed 3 times in a row. tool error")).toBeInTheDocument();
    expect(screen.getAllByTestId(/^inbox-item-/)).toHaveLength(1);
  });

  it("a launch-failed episode shows its count", async () => {
    respond([
      launchFailedItem({
        payload: {
          case: "automationLaunchFailed",
          value: { reason: "machine was deleted", eventId: "evt-5", consecutiveFailures: 4 },
        },
      }),
    ]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-evt-5");
    expect(within(row).getByText("Failed 4 times in a row.")).toBeInTheDocument();
  });

  it("a single launch failure shows no streak count", async () => {
    respond([launchFailedItem()]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-evt-5");
    expect(within(row).queryByText(/in a row/)).toBeNull();
  });

  it("a finished run renders, opens its run, and can be dismissed without blocking", async () => {
    respond([runFinishedItem()]);
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-run_finished:chat-1");
    expect(within(row).getByText(/Run finished/)).toBeInTheDocument();
    expect(within(row).getByLabelText("Run finished")).toBeInTheDocument();

    await userEvent.click(within(row).getByRole("button", { name: /Dismiss/ }));
    expect(mocks.dismissInboxItem.mock.calls[0]![0]).toMatchObject({ itemId: "run_finished:chat-1" });
  });

  it("the Open action of a finished run navigates to the run", async () => {
    respond([runFinishedItem()]);
    const { router } = renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-run_finished:chat-1");
    await userEvent.click(within(row).getByRole("link", { name: "Open" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/runs/chat-1"));
  });

  it("a failed launch shows the reason and Edit automation", async () => {
    respond([launchFailedItem()]);
    const { router } = renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-evt-5");
    expect(within(row).getByText("machine was deleted")).toBeInTheDocument();
    await userEvent.click(within(row).getByRole("link", { name: "Edit automation" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/automations/trg-2"));
  });

  it("only failure kinds offer Dismiss; dismissing calls the RPC and the row leaves", async () => {
    respond([approvalItem(), launchFailedItem()]);
    renderInboxAt(<InboxPage />);
    const approval = await screen.findByTestId("inbox-item-appr-1");
    expect(within(approval).queryByRole("button", { name: /Dismiss/ })).toBeNull();

    const failed = screen.getByTestId("inbox-item-evt-5");
    // After the dismissal the server no longer returns it.
    respond([approvalItem()]);
    await userEvent.click(within(failed).getByRole("button", { name: /Dismiss/ }));
    expect(mocks.dismissInboxItem.mock.calls[0]![0]).toMatchObject({ itemId: "evt-5" });
    await waitFor(() => expect(screen.queryByTestId("inbox-item-evt-5")).toBeNull());
  });

  it("an approval already handled elsewhere says so instead of erroring", async () => {
    respond([approvalItem()]);
    mocks.approve.mockRejectedValue(new ConnectError("approval already processed", Code.FailedPrecondition));
    renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-appr-1");
    await userEvent.click(within(row).getByRole("button", { name: "Approve" }));
    expect(await within(row).findByText("Already handled")).toBeInTheDocument();
  });
});

describe("InboxPage grouping and navigation", () => {
  it("groups items that share a run_id under one heading", async () => {
    respond([
      approvalItem({}, "appr-1"),
      approvalItem({}, "appr-2"),
      approvalItem({}, "appr-3"),
      approvalItem({ chatId: "chat-2", runId: "wf-chat-2", chatTitle: "Other" }, "appr-4"),
    ]);
    renderInboxAt(<InboxPage />);
    const group = await screen.findByTestId("inbox-group-wf-chat-1");
    expect(within(group).getByText("3 approvals waiting")).toBeInTheDocument();
    expect(within(group).getAllByTestId(/^inbox-item-/)).toHaveLength(3);
    expect(screen.queryByTestId("inbox-group-wf-chat-2")).toBeNull();
    expect(screen.getByTestId("inbox-item-appr-4")).toBeInTheDocument();
  });

  it("clicking a row opens run detail by the run's chat id", async () => {
    respond([waitingItem({ chatId: "chat-9", runId: "wf-root-9", itemId: "chat-9" })]);
    const { router } = renderInboxAt(<InboxPage />);
    const row = await screen.findByTestId("inbox-item-chat-9");
    await userEvent.click(within(row).getByRole("link", { name: /Nightly triage/ }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/runs/chat-9"));
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
