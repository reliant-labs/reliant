/**
 * The builder's trigger lane (research/WORKFLOW_EDITOR_UX_REVIEW.md §2 Q1): a
 * column of cards on the left of the canvas — the Chat card, one card per
 * DECLARED trigger with the caller's activation state, a PERSONAL card per
 * inline-source row in THIS project running THIS workflow — and "+ Add
 * trigger". The RPC client is the only mock, so states, filtering, dimming
 * and health are asserted on what ListTriggers returned.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
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

import { COMPACT_AFTER, TriggerRailNode } from "../TriggerRailNode";

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
    canEditDefinition: true,
    declared: [],
    findingsFor: () => [],
    unsavedDeclared: new Set(),
    chatEnabled: true,
    onSetChatEnabled: vi.fn(),
    onEditTrigger: vi.fn(),
    onAddTrigger: vi.fn(),
    onEditDeclared: vi.fn(),
    onActivateDeclared: vi.fn(),
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

const declared = [
  {
    name: "new-issue",
    filter: "",
    inputs: {},
    source: { case: "integration", value: { integration: "github", events: ["issues.opened"], match: {} } },
  },
  { name: "nightly", filter: "", inputs: {}, source: { case: "schedule", value: { cron: ["0 9 * * 1-5"], timezone: "UTC" } } },
] as unknown as TriggerRailContextValue["declared"];

function activation(id: string, name: string, opts: { enabled?: boolean; health?: TriggerHealthStatus; detail?: string; projectId?: string } = {}) {
  return create(TriggerSchema, {
    id,
    name: `activation ${id}`,
    projectId: opts.projectId ?? "proj-2",
    enabled: opts.enabled ?? true,
    workflow: "nightly-triage",
    workflowTrigger: name,
    daemonId: "d-1",
    health: create(TriggerHealthSchema, { status: opts.health ?? TriggerHealthStatus.HEALTHY, lastFailureDetail: opts.detail ?? "" }),
  });
}

describe("TriggerRailNode: the lane", () => {
  beforeEach(() => {
    listTriggers.mockReset();
  });

  it("always starts with the Chat card and an Add trigger card, even with no triggers or no provider", async () => {
    listTriggers.mockResolvedValue(create(ListTriggersResponseSchema, { triggers: [] }));
    renderWithQuery(
      <ReactFlowProvider>
        <TriggerRailNode data={{ eventType: "started", label: "Workflow Start" }} />
      </ReactFlowProvider>,
    );
    expect(screen.getByText("Triggers")).toBeInTheDocument();
    expect(screen.getByTestId("trigger-card-chat")).toHaveAttribute("data-enabled", "true");
    expect(screen.getByText("Starts from a chat")).toBeInTheDocument();

    renderRail();
    expect(await screen.findByRole("button", { name: "Add trigger" })).toBeEnabled();
  });

  it("renders one card per declared trigger, with the caller's activation state, and personal cards for inline rows", async () => {
    const user = userEvent.setup();
    listTriggers.mockResolvedValue(
      create(ListTriggersResponseSchema, {
        // nightly is active in ANOTHER project; still shown on its declaration.
        triggers: [
          activation("a-1", "nightly"),
          trigger("t-9", "Ad hoc sweep", { interval: "1h" }),
          trigger("t-3", "Other workflow", { workflow: "release-notes" }),
          trigger("t-4", "Other project", { projectId: "proj-2" }),
        ],
      }),
    );
    const { context } = renderRail({ declared });

    const issue = await screen.findByTestId("trigger-card-new-issue");
    const nightly = screen.getByTestId("trigger-card-nightly");
    await waitFor(() => expect(nightly).toHaveAttribute("data-state", "active"));
    expect(issue).toHaveAttribute("data-state", "inactive");
    expect(within(issue).getByText("github: issues.opened")).toBeInTheDocument();
    expect(within(issue).getByText("Not active")).toBeInTheDocument();
    expect(within(nightly).getByText("Every weekday at 9:00 AM UTC")).toBeInTheDocument();
    // "you": the state is the caller's, never implied to be global.
    expect(within(nightly).getByText(/· you/)).toBeInTheDocument();
    // The activation is not ALSO a personal card.
    expect(screen.queryByText("activation a-1")).not.toBeInTheDocument();
    const personal = screen.getByTestId("trigger-card-personal-t-9");
    expect(within(personal).getByText("Personal")).toBeInTheDocument();
    expect(within(personal).getByText("Every hour")).toBeInTheDocument();
    expect(screen.queryByText("Other workflow")).not.toBeInTheDocument();
    expect(screen.queryByText("Other project")).not.toBeInTheDocument();

    await user.click(within(issue).getByRole("button", { name: "Activate new-issue" }));
    expect(context.onActivateDeclared).toHaveBeenCalledWith(0);
    await user.click(within(issue).getByRole("button", { name: /new-issue, github: issues.opened, not active. Open trigger/ }));
    expect(context.onEditDeclared).toHaveBeenCalledWith(0);
    expect(personal).toHaveAccessibleName(/Ad hoc sweep, personal/);
    await user.click(personal);
    expect(context.onEditTrigger).toHaveBeenCalledWith(expect.objectContaining({ id: "t-9" }));
  });

  it("switches the Chat trigger off through the definition", async () => {
    const user = userEvent.setup();
    listTriggers.mockResolvedValue(create(ListTriggersResponseSchema, { triggers: [] }));
    const { context } = renderRail({ chatEnabled: true });
    await user.click(screen.getByRole("switch", { name: "Start from chat" }));
    expect(context.onSetChatEnabled).toHaveBeenCalledWith(false);
  });

  it("an automation-only workflow shows its Chat card off, with a warning when it has no other trigger", async () => {
    listTriggers.mockResolvedValue(create(ListTriggersResponseSchema, { triggers: [] }));
    renderRail({ chatEnabled: false });
    const chat = screen.getByTestId("trigger-card-chat");
    expect(chat).toHaveAttribute("data-enabled", "false");
    expect(within(chat).getByRole("switch", { name: "Start from chat" })).toHaveAttribute("aria-checked", "false");
    expect(within(chat).getByText("Off: chats can't start it")).toBeInTheDocument();
    expect(within(chat).getByRole("note")).toHaveTextContent("Nothing starts it now");
  });

  it("a read-only definition cannot switch Chat, but can still add a (personal) trigger", async () => {
    listTriggers.mockResolvedValue(create(ListTriggersResponseSchema, { triggers: [] }));
    renderRail({ canEditDefinition: false, canAddTrigger: true });
    expect(screen.getByRole("switch", { name: "Start from chat" })).toBeDisabled();
    expect(await screen.findByRole("button", { name: "Add trigger" })).toBeEnabled();
  });

  it("explains why a trigger cannot be added to an unsaved workflow", async () => {
    listTriggers.mockResolvedValue(create(ListTriggersResponseSchema, { triggers: [] }));
    renderRail({ canAddTrigger: false, canEditDefinition: false });
    expect(await screen.findByRole("button", { name: "Add trigger" })).toBeDisabled();
    expect(screen.getByText(/Save the workflow to add a trigger/)).toBeInTheDocument();
  });

  it("dims a paused personal trigger and marks a failing one", async () => {
    listTriggers.mockResolvedValue(
      create(ListTriggersResponseSchema, {
        triggers: [
          trigger("t-1", "Paused one", { enabled: false, interval: "1h" }),
          trigger("t-2", "Broken one", { health: TriggerHealthStatus.FAILING, interval: "1h" }),
        ],
      }),
    );
    renderRail();
    const paused = await screen.findByTestId("trigger-card-personal-t-1");
    expect(paused).toHaveAttribute("data-paused", "true");
    expect(within(screen.getByTestId("trigger-card-personal-t-2")).getByTestId("trigger-rail-failing-dot")).toBeInTheDocument();
  });

  it("shows a broken activation and an orphan whose declaration was removed", async () => {
    listTriggers.mockResolvedValue(
      create(ListTriggersResponseSchema, {
        triggers: [
          activation("a-1", "nightly", { health: TriggerHealthStatus.BROKEN, detail: "the declaration changed kind" }),
          activation("a-2", "renamed-away", { health: TriggerHealthStatus.BROKEN, detail: 'no longer declares "renamed-away"' }),
        ],
      }),
    );
    renderRail({ declared });
    const nightly = await screen.findByTestId("trigger-card-nightly");
    await waitFor(() => expect(nightly).toHaveAttribute("data-state", "broken"));
    expect(within(nightly).getByText("Broken")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /activation a-2, broken: no longer declares "renamed-away". Fix/ })).toBeInTheDocument();
  });

  it("maps validation findings onto their trigger's card, and blocks activating an unsaved trigger", async () => {
    listTriggers.mockResolvedValue(create(ListTriggersResponseSchema, { triggers: [] }));
    renderRail({
      declared,
      unsavedDeclared: new Set(["new-issue"]),
      findingsFor: (index, name) => (index === 1 && name === "nightly" ? [{ field: "schedule.cron", message: '"0 25 * * *" is not a valid cron expression' }] : []),
    });
    const nightly = await screen.findByTestId("trigger-card-nightly");
    expect(within(nightly).getByRole("note")).toHaveTextContent('Schedule cron: "0 25 * * *" is not a valid cron expression');
    expect(within(nightly).getByRole("button", { name: /1 problem/ })).toBeInTheDocument();
    const issue = screen.getByTestId("trigger-card-new-issue");
    expect(within(issue).getByRole("button", { name: "Activate new-issue" })).toBeDisabled();
    expect(within(issue).getByText("Save the workflow to activate it.")).toBeInTheDocument();
  });

  it(`collapses to one line per card past ${COMPACT_AFTER} triggers`, async () => {
    const many = Array.from({ length: COMPACT_AFTER + 1 }, (_, i) => ({
      name: `hook-${i}`,
      filter: "",
      inputs: {},
      source: { case: "webhook", value: {} },
    })) as unknown as TriggerRailContextValue["declared"];
    listTriggers.mockResolvedValue(create(ListTriggersResponseSchema, { triggers: [] }));
    renderRail({ declared: many });
    expect(await screen.findByTestId("trigger-lane")).toHaveAttribute("data-compact", "true");
    // Compact cards drop the source line and the inline Activate.
    expect(screen.queryByText("Webhook")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Activate hook-0" })).not.toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /hook-\d, Webhook, not active/ })).toHaveLength(COMPACT_AFTER + 1);
  });

  it("shows a retry line when activations fail to load, without hiding the cards", async () => {
    const user = userEvent.setup();
    listTriggers.mockRejectedValueOnce(new Error("boom"));
    listTriggers.mockResolvedValue(create(ListTriggersResponseSchema, { triggers: [trigger("t-1", "Nightly triage", { cron: ["0 9 * * 1-5"] })] }));
    renderRail({ declared });
    expect(await screen.findByText("Couldn't load your activations")).toBeInTheDocument();
    expect(screen.getByTestId("trigger-card-chat")).toBeInTheDocument();
    expect(screen.getByTestId("trigger-card-nightly")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("Nightly triage")).toBeInTheDocument();
  });
});
