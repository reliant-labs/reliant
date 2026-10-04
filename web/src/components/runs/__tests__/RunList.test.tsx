// Copyright (c) 2025 Reliant Labs

/**
 * The Runs list's structure (§5.3): Needs you, then Live, then Finished; and
 * repeats of one automation collapse into a group unless one of them failed.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { groupRuns, sectionRuns } from "../runSections";
import { RunList } from "../RunList";
import { buildRun, MINUTE, renderRunsAt } from "./runTestUtils";

vi.mock("@/hooks/useDaemonStatus", () => ({ useDaemonStatus: () => ({ daemons: [] }) }));

const hourly = (id: string, displayState = RunDisplayState.COMPLETED, ageMin = 0) =>
  buildRun({
    chatId: id,
    title: `Sweep ${id}`,
    launchKind: "schedule",
    triggerId: "trig-hourly",
    triggerName: "Hourly sweep",
    displayState,
    createdAt: Date.now() - ageMin * MINUTE,
  });

describe("sectionRuns", () => {
  it("splits runs into needs-you, live and finished", () => {
    const sections = sectionRuns([
      buildRun({ chatId: "done", displayState: RunDisplayState.COMPLETED }),
      buildRun({ chatId: "ask", displayState: RunDisplayState.NEEDS_INPUT }),
      buildRun({ chatId: "go", displayState: RunDisplayState.RUNNING }),
      buildRun({ chatId: "zzz", displayState: RunDisplayState.WAITING_FOR_MACHINE }),
      buildRun({ chatId: "park", displayState: RunDisplayState.PAUSED }),
      buildRun({ chatId: "bad", displayState: RunDisplayState.FAILED }),
    ]);
    expect(sections.needsYou.map((r) => r.chatId)).toEqual(["ask", "zzz"]);
    expect(sections.live.map((r) => r.chatId)).toEqual(["go", "park"]);
    expect(sections.finished.map((r) => r.chatId)).toEqual(["done", "bad"]);
  });
});

describe("groupRuns", () => {
  it("collapses more than three repeats of one automation that all completed", () => {
    const items = groupRuns([hourly("a", undefined, 1), hourly("b", undefined, 2), hourly("c", undefined, 3), hourly("d", undefined, 4)]);
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ kind: "group", triggerName: "Hourly sweep" });
  });

  it("leaves three or fewer repeats as rows", () => {
    const items = groupRuns([hourly("a"), hourly("b"), hourly("c")]);
    expect(items.every((item) => item.kind === "run")).toBe(true);
  });

  it("breaks a failure out of the group, above it", () => {
    const items = groupRuns([
      hourly("a", RunDisplayState.COMPLETED, 1),
      hourly("b", RunDisplayState.FAILED, 2),
      hourly("c", RunDisplayState.COMPLETED, 3),
      hourly("d", RunDisplayState.COMPLETED, 4),
      hourly("e", RunDisplayState.COMPLETED, 5),
    ]);
    expect(items[0]).toMatchObject({ kind: "run", run: { chatId: "b" } });
    expect(items[1]).toMatchObject({ kind: "group" });
    expect(items[1]!.kind === "group" && items[1]!.runs.map((r) => r.chatId)).toEqual(["a", "c", "d", "e"]);
  });

  it("does not group runs nobody scheduled", () => {
    const chats = ["a", "b", "c", "d", "e"].map((id) => buildRun({ chatId: id }));
    expect(groupRuns(chats).every((item) => item.kind === "run")).toBe(true);
  });
});

describe("RunList", () => {
  it("renders sections in order and only the ones with runs", async () => {
    renderRunsAt(
      <RunList
        runs={[
          buildRun({ chatId: "done", title: "Finished one" }),
          buildRun({ chatId: "go", title: "Live one", displayState: RunDisplayState.RUNNING }),
        ]}
        groupRepeats
      />,
    );
    const lists = await screen.findAllByRole("list");
    expect(lists.map((list) => list.getAttribute("aria-label"))).toEqual(["Live runs", "Finished runs"]);
    expect(within(lists[0]!).getByText("Live one")).toBeInTheDocument();
  });

  it("expands a group of repeats", async () => {
    const user = userEvent.setup();
    renderRunsAt(
      <RunList runs={["a", "b", "c", "d"].map((id, i) => hourly(id, RunDisplayState.COMPLETED, i))} groupRepeats />,
    );
    const toggle = await screen.findByRole("button", { name: /Hourly sweep · 4 runs · all completed/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Sweep a")).not.toBeInTheDocument();
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Sweep a")).toBeInTheDocument();
  });

  it("shows every repeat when grouping is off", async () => {
    renderRunsAt(
      <RunList runs={["a", "b", "c", "d"].map((id, i) => hourly(id, RunDisplayState.COMPLETED, i))} groupRepeats={false} />,
    );
    expect(await screen.findByText("Sweep d")).toBeInTheDocument();
  });
});
