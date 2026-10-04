/**
 * The sidebar footer pill (WORKFLOW_UI.md §4.3, §6.6): counts live automation
 * runs the chat list does not show, links to Runs filtered to them, and is
 * absent when there are none. It reads ListRuns once and is refreshed by the
 * update stream's invalidation of the runs lists, never by polling.
 */
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { RunDisplayState } from "../../../gen/reliant/v1/run_pb";
import type { RunSummary } from "../../../api/run-grpc";
import { renderWithQuery } from "../../../test/renderWithQuery";

const { listRuns } = vi.hoisted(() => ({ listRuns: vi.fn() }));

vi.mock("../../../api/run-grpc", async () => {
  const actual = await vi.importActual<typeof import("../../../api/run-grpc")>("../../../api/run-grpc");
  return { ...actual, runGrpc: { ...actual.runGrpc, list: listRuns } };
});

import { AutomationActivityPill } from "../AutomationActivityPill";
import { runKeys, summarizeLiveAutomations, LIVE_AUTOMATION_LIMIT } from "../../../hooks/run-queries";

function run(chatId: string, launchKind: string, displayState: RunDisplayState): RunSummary {
  return {
    runId: `wf-${chatId}`,
    chatId,
    title: chatId,
    workflowName: "builtin://agent",
    projectId: "p1",
    launchKind,
    triggerId: "",
    triggerName: "",
    daemonId: "",
    state: 0,
    stopReason: 0,
    activity: 0,
    displayState,
    outcome: "",
    createdAt: 0,
  } as RunSummary;
}

beforeEach(() => listRuns.mockReset());

describe("summarizeLiveAutomations", () => {
  it("counts live automation runs, separating the ones that need you", () => {
    const summary = summarizeLiveAutomations(
      [
        run("a", "schedule", RunDisplayState.RUNNING),
        run("b", "agent.start_run", RunDisplayState.NEEDS_INPUT),
        run("c", "schedule", RunDisplayState.WAITING_FOR_MACHINE),
      ],
      new Set(),
    );
    expect(summary).toEqual({ running: 3, needsYou: 1, truncated: false });
  });

  it("skips interactive chats, runs already in the sidebar, and runs that are not live", () => {
    const summary = summarizeLiveAutomations(
      [
        run("chat", "chat.start", RunDisplayState.RUNNING),
        run("legacy", "", RunDisplayState.RUNNING),
        run("adopted", "schedule", RunDisplayState.RUNNING),
        run("done", "schedule", RunDisplayState.COMPLETED),
        run("paused", "schedule", RunDisplayState.PAUSED),
      ],
      new Set(["adopted"]),
    );
    expect(summary).toEqual({ running: 0, needsYou: 0, truncated: false });
  });
});

describe("AutomationActivityPill", () => {
  it("reads one page of live runs, under the runs-list key the update stream invalidates", async () => {
    listRuns.mockResolvedValue({ runs: [run("a", "schedule", RunDisplayState.RUNNING)], nextPageToken: "" });
    const { queryClient } = renderWithQuery(<AutomationActivityPill listedChatIds={new Set()} />);

    await screen.findByTestId("automation-activity-pill");
    const request = listRuns.mock.calls[0]![0];
    expect(request.displayStates).toEqual([
      RunDisplayState.RUNNING,
      RunDisplayState.NEEDS_INPUT,
      RunDisplayState.WAITING_FOR_MACHINE,
    ]);
    expect(request.limit).toBe(LIVE_AUTOMATION_LIMIT);
    expect(request.projectId).toBeUndefined();
    expect(request.startedAfter).toBeUndefined();

    const pillQuery = queryClient.getQueryCache().find({ queryKey: runKeys.lists(), exact: false });
    expect(pillQuery?.options).not.toHaveProperty("refetchInterval");
  });

  it("shows the count and links to live runs", async () => {
    listRuns.mockResolvedValue({
      runs: [run("a", "schedule", RunDisplayState.RUNNING), run("b", "schedule", RunDisplayState.RUNNING)],
      nextPageToken: "",
    });
    const onOpenRuns = vi.fn();
    renderWithQuery(<AutomationActivityPill listedChatIds={new Set()} onOpenRuns={onOpenRuns} />);

    const running = await screen.findByTestId("automation-activity-pill-running");
    expect(running).toHaveTextContent("2 automations running");
    expect(screen.queryByTestId("automation-activity-pill-needs-you")).toBeNull();

    fireEvent.click(running);
    expect(onOpenRuns).toHaveBeenCalledWith({ state: ["live"], allProjects: true, range: "all" });
  });

  // "Needs you" is the Inbox's job (§8, §14.1 decision 1): the Inbox is where
  // the question or approval can be ANSWERED, while a filtered run list only
  // points at it. So the pill opens the Inbox, not Runs.
  it("shows how many need you, and opens the Inbox", async () => {
    listRuns.mockResolvedValue({
      runs: [run("a", "schedule", RunDisplayState.RUNNING), run("b", "agent.start_run", RunDisplayState.NEEDS_INPUT)],
      nextPageToken: "",
    });
    const onOpenRuns = vi.fn();
    const onOpenInbox = vi.fn();
    renderWithQuery(
      <AutomationActivityPill listedChatIds={new Set()} onOpenRuns={onOpenRuns} onOpenInbox={onOpenInbox} />,
    );

    const needsYou = await screen.findByTestId("automation-activity-pill-needs-you");
    expect(needsYou).toHaveTextContent("1 needs you");
    fireEvent.click(needsYou);
    expect(onOpenInbox).toHaveBeenCalledTimes(1);
    expect(onOpenRuns).not.toHaveBeenCalled();
  });

  it("says the count is a floor when the server has more", async () => {
    listRuns.mockResolvedValue({ runs: [run("a", "schedule", RunDisplayState.RUNNING)], nextPageToken: "next" });
    renderWithQuery(<AutomationActivityPill listedChatIds={new Set()} />);

    expect(await screen.findByTestId("automation-activity-pill-running")).toHaveTextContent(
      "1+ automation running",
    );
  });

  it("hides at zero, including when the only live run is already a sidebar row", async () => {
    listRuns.mockResolvedValue({ runs: [run("adopted", "schedule", RunDisplayState.RUNNING)], nextPageToken: "" });
    renderWithQuery(<AutomationActivityPill listedChatIds={new Set(["adopted"])} />);

    await waitFor(() => expect(listRuns).toHaveBeenCalled());
    expect(screen.queryByTestId("automation-activity-pill")).toBeNull();
  });
});
