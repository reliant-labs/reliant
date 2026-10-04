/**
 * The builder's trigger rail (research/WORKFLOW_UI.md §3.2): "Starts when",
 * the always-present chat line, one line per trigger in THIS project naming
 * THIS workflow, and "+ Add trigger". The RPC client is the only mock, so the
 * filtering, dimming and health dot are asserted on what ListTriggers returned.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { ReactFlowProvider } from "@xyflow/react";

import {
  ListTriggersResponseSchema,
  ScheduleSourceSchema,
  TriggerHealthSchema,
  TriggerHealthStatus,
  TriggerSchema,
} from "@/gen/reliant/v1/trigger_pb";
import { renderWithQuery } from "@/test/renderWithQuery";
import type { Trigger } from "@/api/trigger-grpc";
import { TriggerRailProvider, type TriggerRailContextValue } from "../../TriggerRailContext";

const listTriggers = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    trigger: () => ({ listTriggers }),
  },
}));

// The handle's connected styling reads the node id from React Flow's internal
// node context, which only <ReactFlow> provides. The rail's content is what is
// under test here, so report "no connections" and keep the real Handle.
vi.mock("@xyflow/react", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@xyflow/react")>()),
  useNodeConnections: () => [],
}));

import { TriggerRailNode } from "../TriggerRailNode";

function trigger(
  id: string,
  name: string,
  opts: {
    workflow?: string;
    projectId?: string;
    enabled?: boolean;
    health?: TriggerHealthStatus;
    cron?: string[];
    interval?: string;
  } = {},
) {
  return create(TriggerSchema, {
    id,
    name,
    projectId: opts.projectId ?? "proj-1",
    enabled: opts.enabled ?? true,
    workflow: opts.workflow ?? "nightly-triage",
    daemonId: "d-1",
    source: {
      case: "schedule",
      value: create(ScheduleSourceSchema, {
        cron: opts.cron ?? [],
        interval: opts.interval ?? "",
        timezone: "UTC",
      }),
    },
    health: create(TriggerHealthSchema, {
      status: opts.health ?? TriggerHealthStatus.HEALTHY,
      consecutiveFailures: opts.health === TriggerHealthStatus.FAILING ? 3 : 0,
    }),
  });
}

function renderRail(context: Partial<TriggerRailContextValue> = {}) {
  const value: TriggerRailContextValue = {
    workflowRef: "nightly-triage",
    projectId: "proj-1",
    canAddTrigger: true,
    onEditTrigger: vi.fn(),
    onAddTrigger: vi.fn(),
    ...context,
  };
  const result = renderWithQuery(
    <ReactFlowProvider>
      <TriggerRailProvider value={value}>
        <TriggerRailNode data={{ eventType: "started", label: "Workflow Start" }} />
      </TriggerRailProvider>
    </ReactFlowProvider>,
  );
  return { ...result, context: value };
}

describe("TriggerRailNode", () => {
  beforeEach(() => {
    listTriggers.mockReset();
  });

  it("lists only this project's triggers for this workflow, with the schedule in words", async () => {
    listTriggers.mockResolvedValue(
      create(ListTriggersResponseSchema, {
        triggers: [
          trigger("t-1", "Nightly triage", { cron: ["0 9 * * 1-5"] }),
          trigger("t-2", "Hourly sweep", { interval: "1h" }),
          trigger("t-3", "Other workflow", { workflow: "release-notes" }),
          trigger("t-4", "Other project", { projectId: "proj-2" }),
          trigger("t-5", "Builtin form", { workflow: "workflow://nightly-triage", interval: "15m" }),
        ],
      }),
    );

    renderRail();

    const list = await screen.findByRole("list", { name: "How this workflow starts" });
    await within(list).findByText("Nightly triage");
    expect(within(list).getByText("Hourly sweep")).toBeInTheDocument();
    expect(within(list).getByText("Builtin form")).toBeInTheDocument();
    expect(within(list).queryByText("Other workflow")).not.toBeInTheDocument();
    expect(within(list).queryByText("Other project")).not.toBeInTheDocument();
    expect(within(list).getByText("Every hour")).toBeInTheDocument();
    expect(listTriggers).toHaveBeenCalledWith(expect.objectContaining({ projectId: "proj-1" }));
  });

  it('always shows "Someone starts a chat", even with no triggers or no provider data', async () => {
    listTriggers.mockResolvedValue(create(ListTriggersResponseSchema, { triggers: [] }));
    renderRail();

    expect(screen.getByText("Starts when")).toBeInTheDocument();
    expect(screen.getByText("Someone starts a chat")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Add trigger" })).toBeInTheDocument();
  });

  it("dims a disabled trigger and marks a failing one with a dot and text", async () => {
    listTriggers.mockResolvedValue(
      create(ListTriggersResponseSchema, {
        triggers: [
          trigger("t-1", "Paused one", { enabled: false, interval: "1h" }),
          trigger("t-2", "Broken one", { health: TriggerHealthStatus.FAILING, interval: "1h" }),
          trigger("t-3", "Fine one", { interval: "1h" }),
        ],
      }),
    );

    renderRail();

    const paused = await screen.findByRole("button", { name: /Paused one/ });
    const broken = screen.getByRole("button", { name: /Broken one/ });
    const fine = screen.getByRole("button", { name: /Fine one/ });

    expect(paused).toHaveAttribute("data-paused", "true");
    expect(paused.className).toMatch(/opacity-/);
    expect(paused).toHaveAccessibleName(/paused/i);
    expect(fine).not.toHaveAttribute("data-paused");

    expect(within(broken).getByTestId("trigger-rail-failing-dot")).toBeInTheDocument();
    expect(broken).toHaveAccessibleName(/3 failed/);
    expect(within(fine).queryByTestId("trigger-rail-failing-dot")).not.toBeInTheDocument();
  });

  it("opens edit for a clicked line and create for Add trigger", async () => {
    const user = userEvent.setup();
    listTriggers.mockResolvedValue(
      create(ListTriggersResponseSchema, {
        triggers: [trigger("t-1", "Nightly triage", { interval: "1h" })],
      }),
    );

    const { context } = renderRail();

    await user.click(await screen.findByRole("button", { name: /Nightly triage/ }));
    expect(context.onEditTrigger).toHaveBeenCalledTimes(1);
    expect((vi.mocked(context.onEditTrigger).mock.calls[0]![0] as Trigger).id).toBe("t-1");

    await user.click(screen.getByRole("button", { name: "Add trigger" }));
    expect(context.onAddTrigger).toHaveBeenCalledTimes(1);
  });

  it("explains why a trigger cannot be added to an unsaved workflow", async () => {
    listTriggers.mockResolvedValue(create(ListTriggersResponseSchema, { triggers: [] }));
    renderRail({ canAddTrigger: false });

    expect(await screen.findByRole("button", { name: "Add trigger" })).toBeDisabled();
    expect(screen.getByText(/Save the workflow to add a trigger/)).toBeInTheDocument();
  });

  it("shows a retry line when triggers fail to load, without hiding the chat line", async () => {
    const user = userEvent.setup();
    listTriggers.mockRejectedValueOnce(new Error("boom"));
    listTriggers.mockResolvedValue(
      create(ListTriggersResponseSchema, {
        triggers: [trigger("t-1", "Nightly triage", { interval: "1h" })],
      }),
    );

    renderRail();

    expect(await screen.findByText("Couldn't load triggers")).toBeInTheDocument();
    expect(screen.getByText("Someone starts a chat")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("Nightly triage")).toBeInTheDocument();
  });
});
