// Copyright (c) 2025 Reliant Labs

/**
 * One row of the Runs list: status words from lib/runStatus, a "started by"
 * cell from the launch-kind vocabulary, and a link to the run's own page.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";

import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { runStatusFromDisplayState } from "@/lib/runStatus";
import { RunRow } from "../RunRow";
import { buildRun, renderRunsAt } from "./runTestUtils";

vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ daemons: [{ daemonId: "d-1", hostname: "laptop" }] }),
}));

function renderRow(...args: Parameters<typeof buildRun>) {
  const run = buildRun(...args);
  renderRunsAt(
    <ul>
      <RunRow run={run} projectName="Reliant" />
    </ul>,
  );
  return run;
}

describe("RunRow", () => {
  it.each([
    RunDisplayState.QUEUED,
    RunDisplayState.RUNNING,
    RunDisplayState.NEEDS_INPUT,
    RunDisplayState.PAUSED,
    RunDisplayState.COMPLETED,
    RunDisplayState.FAILED,
    RunDisplayState.CANCELLED,
    RunDisplayState.WAITING_FOR_MACHINE,
  ])("labels display state %s with the runStatus vocabulary", async (displayState) => {
    renderRow({ displayState });
    const row = await screen.findByTestId("run-row-chat-1");
    expect(within(row).getByText(runStatusFromDisplayState(displayState).label)).toBeInTheDocument();
  });

  it("links the title to the run's page", async () => {
    renderRow({ chatId: "chat-9", title: "Nightly triage" });
    const link = await screen.findByRole("link", { name: "Nightly triage" });
    expect(link).toHaveAttribute("href", "/runs/chat-9");
  });

  it("names the automation for a scheduled run, linked to it", async () => {
    renderRow({ launchKind: "schedule", triggerId: "trig-1", triggerName: "Hourly sweep" });
    const link = await screen.findByRole("link", { name: "Hourly sweep" });
    expect(link).toHaveAttribute("href", "/automations/trig-1");
  });

  it("says an agent started an agent-started run", async () => {
    renderRow({ launchKind: "agent.start_run" });
    const row = await screen.findByTestId("run-row-chat-1");
    expect(within(row).getByText("Agent")).toBeInTheDocument();
  });

  it("reads a run with no launch kind as a chat", async () => {
    renderRow({ launchKind: "" });
    const row = await screen.findByTestId("run-row-chat-1");
    expect(within(row).getByText("Chat")).toBeInTheDocument();
  });

  it("shows the workflow, project and machine on the second line", async () => {
    renderRow({ workflowName: "builtin://agent", daemonId: "d-1" });
    const row = await screen.findByTestId("run-row-chat-1");
    expect(within(row).getByText(/Agent · Reliant · laptop/)).toBeInTheDocument();
  });

  it("shows a duration for a finished run", async () => {
    renderRow({ createdAt: 0, completedAt: 3 * 60_000 + 5_000 });
    const row = await screen.findByTestId("run-row-chat-1");
    expect(within(row).getByText("3m 5s")).toBeInTheDocument();
  });
});
