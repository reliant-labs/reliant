// Copyright (c) 2025 Reliant Labs

/**
 * The strip of agent-started runs in the parent chat's header (decision 4:
 * those runs are never in the sidebar, so this is where they are found).
 *
 * The RPC client is the only mock. What is pinned: it asks ListRuns for this
 * chat's children only; it renders nothing when there are none; it links the
 * filtered Runs list and each run; and it does not fetch at all where the
 * surface has no Runs area (the /m header).
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import { ListRunsResponseSchema, RunDisplayState, RunSchema } from "@/gen/reliant/v1/run_pb";
import { SurfaceProvider } from "@/lib/surfaceContext";
import { renderRunsAt } from "./runTestUtils";

const listRuns = vi.fn();
vi.mock("@/api/grpc-client", () => ({
  grpcClient: { run: () => ({ listRuns }) },
}));

import { ChildRunsStrip } from "../ChildRunsStrip";

function child(id: string, displayState: RunDisplayState, title = `Child ${id}`) {
  return create(RunSchema, {
    id: `wf-${id}`,
    sessionId: id,
    title,
    launchKind: "agent.start_run",
    parentChatId: "parent-1",
    displayState,
    createdAtMs: BigInt(Date.now()),
  });
}

describe("ChildRunsStrip", () => {
  beforeEach(() => listRuns.mockReset());

  it("asks for this chat's children only, then shows a count, a dot per run, and links", async () => {
    listRuns.mockResolvedValue(
      create(ListRunsResponseSchema, {
        runs: [child("c1", RunDisplayState.RUNNING, "Fix flaky test"), child("c2", RunDisplayState.FAILED)],
      }),
    );
    renderRunsAt(<ChildRunsStrip chatId="parent-1" />, "/runs/parent-1");

    const strip = await screen.findByTestId("child-runs-strip");
    expect(strip).toHaveTextContent("2 runs started here");
    expect(listRuns).toHaveBeenCalledTimes(1);
    expect(listRuns.mock.calls[0]![0]).toMatchObject({ parentChatId: "parent-1", includeArchived: false });
    // No time window or project: a child is a child wherever and whenever it ran.
    expect(listRuns.mock.calls[0]![0].startedAfter).toBeUndefined();
    expect(listRuns.mock.calls[0]![0].projectId).toBeUndefined();

    expect(screen.getByRole("link", { name: "2 runs started here" })).toHaveAttribute(
      "href",
      "/runs?parent=parent-1",
    );
    const first = screen.getByRole("link", { name: /Fix flaky test/ });
    expect(first).toHaveAttribute("href", "/runs/c1");
    expect(first.querySelector('[data-run-status="running"]')).not.toBeNull();
    expect(screen.getByRole("link", { name: /Child c2/ }).querySelector('[data-run-status="failed"]')).not.toBeNull();
  });

  it("says one run in the singular", async () => {
    listRuns.mockResolvedValue(create(ListRunsResponseSchema, { runs: [child("c1", RunDisplayState.COMPLETED)] }));
    renderRunsAt(<ChildRunsStrip chatId="parent-1" />, "/runs/parent-1");
    expect(await screen.findByTestId("child-runs-strip")).toHaveTextContent("1 run started here");
  });

  it("renders nothing when the chat started no runs", async () => {
    listRuns.mockResolvedValue(create(ListRunsResponseSchema, { runs: [] }));
    renderRunsAt(<ChildRunsStrip chatId="parent-1" />, "/runs/parent-1");
    await waitFor(() => expect(listRuns).toHaveBeenCalledTimes(1));
    expect(screen.queryByTestId("child-runs-strip")).not.toBeInTheDocument();
  });

  it("does not query on a surface with no Runs area (the mobile header)", async () => {
    listRuns.mockResolvedValue(create(ListRunsResponseSchema, { runs: [child("c1", RunDisplayState.RUNNING)] }));
    renderRunsAt(
      <SurfaceProvider surface="mobile">
        <span data-testid="mounted" />
        <ChildRunsStrip chatId="parent-1" />
      </SurfaceProvider>,
      "/runs/parent-1",
    );
    await screen.findByTestId("mounted");
    expect(listRuns).not.toHaveBeenCalled();
    expect(screen.queryByTestId("child-runs-strip")).not.toBeInTheDocument();
  });
});
