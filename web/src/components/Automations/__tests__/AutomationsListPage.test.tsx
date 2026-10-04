// Copyright (c) 2025 Reliant Labs

/**
 * The automations list, against the real data layer (trigger-grpc +
 * trigger-queries) with only the RPC client mocked: grouped rows with the
 * server's health, the launched run's own status, names straight off the
 * trigger, the "Needs attention" group, the "Coming up" strip, and an enabled
 * switch that sends SetTriggerEnabled.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

import {
  ScheduleSourceSchema,
  SetTriggerEnabledResponseSchema,
  TriggerEventOutcome,
  TriggerEventRunSchema,
  TriggerEventSchema,
  TriggerHealthSchema,
  TriggerHealthStatus,
  TriggerSchema,
} from "@/gen/reliant/v1/trigger_pb";
import { RunDisplayState } from "@/gen/reliant/v1/run_pb";
import { HOUR, isoFromNow, renderAtRoute } from "./automationTestUtils";

const listTriggers = vi.fn();
const setTriggerEnabled = vi.fn();
const listDaemons = vi.fn(async () => ({ daemons: [] }));

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    trigger: () => ({ listTriggers, setTriggerEnabled }),
    daemonRegistry: () => ({ listDaemons }),
    workflow: () => ({ listWorkflows: vi.fn(async () => ({ workflows: [], invalidWorkflows: [] })) }),
  },
}));

vi.mock("@/hooks/useTitleBarChrome", () => ({
  useTitleBarChrome: () => ({
    isElectron: false,
    isMac: false,
    isFullscreen: false,
    trafficLightPadding: "8px",
    dragRegionStyle: {},
    noDragRegionStyle: {},
  }),
}));

// The list must not need the project store for names. It is left EMPTY, so a
// row that rendered a name from it would render nothing.
const projectStoreSelector = vi.fn();
vi.mock("@/store/projectStore", () => {
  const snapshot = () => ({
    projects: [],
    currentProject: null,
    loadProjects: vi.fn(async () => undefined),
  });
  const useProjectStore = Object.assign(
    (selector?: (s: ReturnType<typeof snapshot>) => unknown) => {
      projectStoreSelector();
      return selector ? selector(snapshot()) : snapshot();
    },
    { getState: snapshot },
  );
  return { useProjectStore };
});

import { AutomationsListPage } from "../AutomationsListPage";

type TriggerInit = Parameters<typeof create<typeof TriggerSchema>>[1];

function protoTrigger(overrides: Partial<TriggerInit> = {}) {
  return create(TriggerSchema, {
    id: "trig-1",
    name: "Morning triage",
    projectId: "proj-1",
    projectName: "Reliant",
    daemonId: "daemon-1",
    daemonName: "MacBook",
    enabled: true,
    workflow: "builtin://agent",
    message: "Triage new issues",
    nextFireAt: isoFromNow(2 * HOUR + 60_000),
    health: create(TriggerHealthSchema, { status: TriggerHealthStatus.HEALTHY }),
    lastEvent: create(TriggerEventSchema, {
      id: "ev-1",
      occurredAt: isoFromNow(-3 * HOUR),
      outcome: TriggerEventOutcome.LAUNCHED,
      chatId: "chat-1",
      run: create(TriggerEventRunSchema, { displayState: RunDisplayState.COMPLETED }),
    }),
    source: {
      case: "schedule",
      value: create(ScheduleSourceSchema, { cron: ["0 9 * * 1-5"], timezone: "America/New_York" }),
    },
    ...overrides,
  });
}

function launched(displayState: RunDisplayState) {
  return create(TriggerEventSchema, {
    id: "ev-x",
    occurredAt: isoFromNow(-HOUR),
    outcome: TriggerEventOutcome.LAUNCHED,
    chatId: "chat-x",
    run: create(TriggerEventRunSchema, { displayState }),
  });
}

describe("AutomationsListPage", () => {
  beforeEach(() => {
    listTriggers.mockReset();
    setTriggerEnabled.mockReset();
    projectStoreSelector.mockReset();
    listDaemons.mockClear();
  });

  it("renders a row with its names, schedule, health, last run and next fire", async () => {
    listTriggers.mockResolvedValue({ triggers: [protoTrigger()] });

    renderAtRoute(<AutomationsListPage />);

    const row = await screen.findByTestId("automation-row-trig-1");
    expect(within(row).getByRole("link", { name: "Morning triage" })).toHaveAttribute(
      "href",
      "/automations/trig-1",
    );
    expect(within(row).getByText("Reliant · MacBook")).toBeInTheDocument();
    expect(within(row).getByText("Every weekday at 9:00 AM ET")).toBeInTheDocument();
    expect(within(row).getByText("Healthy")).toBeInTheDocument();
    expect(within(row).getByText("Completed")).toBeInTheDocument();
    expect(within(row).getByText("3 hours ago")).toBeInTheDocument();
    expect(within(row).getByText("in 2 hours")).toBeInTheDocument();
    expect(within(row).getByRole("switch", { name: "Morning triage enabled" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    // Listed across every project: no project filter on the request.
    expect(listTriggers.mock.calls[0]![0].projectId).toBeUndefined();
  });

  it("names come from the trigger itself, with no store or daemon lookup", async () => {
    listTriggers.mockResolvedValue({ triggers: [protoTrigger()] });

    renderAtRoute(<AutomationsListPage />);

    const row = await screen.findByTestId("automation-row-trig-1");
    // On the very first render with data, not after some store fills in.
    expect(within(row).getByText("Reliant · MacBook")).toBeInTheDocument();
    expect(projectStoreSelector).not.toHaveBeenCalled();
    expect(listDaemons).not.toHaveBeenCalled();
  });

  it("a launched run that then failed reads Failed, not a neutral Launched or green", async () => {
    listTriggers.mockResolvedValue({
      triggers: [
        protoTrigger({
          health: create(TriggerHealthSchema, {
            status: TriggerHealthStatus.DEGRADED,
            consecutiveFailures: 1,
            lastFailureDetail: "run failed",
          }),
          lastEvent: launched(RunDisplayState.FAILED),
        }),
      ],
    });

    renderAtRoute(<AutomationsListPage />);

    const row = await screen.findByTestId("automation-row-trig-1");
    const failed = within(row).getByText("Failed");
    expect(failed.closest("[data-run-status]")).toHaveAttribute("data-run-status", "failed");
    expect(within(row).queryByText("Launched")).toBeNull();
    expect(row.querySelector('[class*="bg-success"]')).toBeNull();
  });

  it("a firing that never became a run keeps the event's own words", async () => {
    listTriggers.mockResolvedValue({
      triggers: [
        protoTrigger({
          lastEvent: create(TriggerEventSchema, {
            id: "ev-1",
            occurredAt: isoFromNow(-HOUR),
            outcome: TriggerEventOutcome.FAILED,
            outcomeDetail: "model unavailable",
          }),
        }),
      ],
    });

    renderAtRoute(<AutomationsListPage />);

    const row = await screen.findByTestId("automation-row-trig-1");
    expect(within(row).getByText("Failed to launch")).toBeInTheDocument();
  });

  it("shows a never-run, paused trigger as Paused with no next fire", async () => {
    listTriggers.mockResolvedValue({
      triggers: [
        protoTrigger({
          enabled: false,
          lastEvent: undefined,
          nextFireAt: undefined,
          health: create(TriggerHealthSchema, { status: TriggerHealthStatus.UNKNOWN }),
        }),
      ],
    });

    renderAtRoute(<AutomationsListPage />);

    const row = await screen.findByTestId("automation-row-trig-1");
    expect(within(row).getByText("Never run")).toBeInTheDocument();
    expect(within(row).getByText("Paused")).toBeInTheDocument();
    expect(within(row).getByText("Next run: none")).toBeInTheDocument();
  });

  it("pins failing, waiting and skipping rows under Needs attention, worst first", async () => {
    listTriggers.mockResolvedValue({
      triggers: [
        protoTrigger({ id: "ok", name: "All good" }),
        protoTrigger({
          id: "skip",
          name: "Skipper",
          health: create(TriggerHealthSchema, { status: TriggerHealthStatus.DEGRADED, consecutiveSkips: 4 }),
        }),
        protoTrigger({
          id: "fail",
          name: "Broken",
          health: create(TriggerHealthSchema, { status: TriggerHealthStatus.FAILING, consecutiveFailures: 2 }),
        }),
        protoTrigger({
          id: "wait",
          name: "Sleepy",
          health: create(TriggerHealthSchema, { status: TriggerHealthStatus.HEALTHY }),
          lastEvent: launched(RunDisplayState.WAITING_FOR_MACHINE),
        }),
      ],
    });

    renderAtRoute(<AutomationsListPage />);

    const attention = await screen.findByTestId("automation-group-needs-attention");
    expect(within(attention).getByRole("heading", { name: "Needs attention" })).toBeInTheDocument();
    const ids = within(attention)
      .getAllByRole("listitem")
      .map((li) => li.getAttribute("data-testid"));
    expect(ids).toEqual(["automation-row-fail", "automation-row-wait", "automation-row-skip"]);
    expect(within(attention).getByText("2 failed")).toBeInTheDocument();
    // Health and the last run both say it: the run is blocked, so the
    // automation is too.
    const waitingRow = within(attention).getByTestId("automation-row-wait");
    expect(waitingRow).toHaveAttribute("data-health", "waiting_for_machine");
    expect(within(waitingRow).getAllByText("Waiting for machine")).toHaveLength(2);
    expect(within(attention).getByText("Skipping")).toBeInTheDocument();
    // Each automation appears once on the page.
    expect(screen.getAllByTestId("automation-row-fail")).toHaveLength(1);
    expect(within(screen.getByTestId("automation-group-workflow:builtin://agent")).getAllByRole("listitem")).toHaveLength(1);
  });

  it("has no Needs attention group when nothing needs attention", async () => {
    listTriggers.mockResolvedValue({ triggers: [protoTrigger()] });

    renderAtRoute(<AutomationsListPage />);

    await screen.findByTestId("automation-row-trig-1");
    expect(screen.queryByTestId("automation-group-needs-attention")).toBeNull();
    expect(screen.queryByText("Needs attention")).toBeNull();
  });

  it("groups by workflow by default and by project on request", async () => {
    listTriggers.mockResolvedValue({
      triggers: [
        protoTrigger({ id: "a", name: "A", workflow: "builtin://agent", projectId: "p1", projectName: "Zeta" }),
        protoTrigger({ id: "b", name: "B", workflow: "workflow://triage", projectId: "p1", projectName: "Zeta" }),
        protoTrigger({ id: "c", name: "C", workflow: "builtin://agent", projectId: "p2", projectName: "Alpha" }),
      ],
    });

    renderAtRoute(<AutomationsListPage />);

    await screen.findByTestId("automation-row-a");
    expect(screen.getByRole("button", { name: "Workflow" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent)).toEqual([
      "Coming up",
      "Agent",
      "Triage",
    ]);

    await userEvent.click(screen.getByRole("button", { name: "Project" }));
    expect(screen.getByRole("button", { name: "Project" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent)).toEqual([
      "Coming up",
      "Alpha",
      "Zeta",
    ]);
    expect(within(screen.getByTestId("automation-group-project:p1")).getByText("2 automations · 2 on")).toBeInTheDocument();
  });

  it("shows the Coming up strip for enabled schedules and hides it when none are on", async () => {
    listTriggers.mockResolvedValueOnce({ triggers: [protoTrigger()] });
    const first = renderAtRoute(<AutomationsListPage />);
    const strip = await screen.findByTestId("coming-up-timeline");
    expect(within(strip).getByTestId("coming-up-lane-trig-1")).toBeInTheDocument();
    first.unmount();

    listTriggers.mockResolvedValueOnce({ triggers: [protoTrigger({ enabled: false })] });
    renderAtRoute(<AutomationsListPage />);
    await screen.findByTestId("automation-row-trig-1");
    expect(screen.queryByTestId("coming-up-timeline")).toBeNull();
  });

  it("sends SetTriggerEnabled when the switch is flipped", async () => {
    listTriggers.mockResolvedValueOnce({ triggers: [protoTrigger()] });
    // Hold the RPC open so the assertion below sees the optimistic state, not
    // the server's answer.
    let answer: (value: unknown) => void = () => undefined;
    setTriggerEnabled.mockReturnValue(new Promise((resolve) => (answer = resolve)));

    renderAtRoute(<AutomationsListPage />);
    const toggle = await screen.findByRole("switch", { name: "Morning triage enabled" });
    await userEvent.click(toggle);

    await waitFor(() => expect(setTriggerEnabled).toHaveBeenCalledTimes(1));
    expect(setTriggerEnabled.mock.calls[0]![0]).toMatchObject({ id: "trig-1", enabled: false });
    // Optimistic: the switch moves before the server answers.
    expect(screen.getByRole("switch", { name: "Morning triage enabled" })).toHaveAttribute(
      "aria-checked",
      "false",
    );

    // The server confirms; the list refetch then reflects the stored state.
    listTriggers.mockResolvedValue({ triggers: [protoTrigger({ enabled: false })] });
    answer(create(SetTriggerEnabledResponseSchema, { trigger: protoTrigger({ enabled: false }) }));
    await waitFor(() => expect(listTriggers).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("switch", { name: "Morning triage enabled" })).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  it("rolls the switch back when SetTriggerEnabled fails", async () => {
    listTriggers.mockResolvedValue({ triggers: [protoTrigger()] });
    setTriggerEnabled.mockRejectedValue(new Error("boom"));

    renderAtRoute(<AutomationsListPage />);
    await userEvent.click(await screen.findByRole("switch", { name: "Morning triage enabled" }));

    await waitFor(() => expect(setTriggerEnabled).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: "Morning triage enabled" })).toHaveAttribute(
        "aria-checked",
        "true",
      ),
    );
  });

  it("explains automations and offers to create one when there are none", async () => {
    listTriggers.mockResolvedValue({ triggers: [] });

    renderAtRoute(<AutomationsListPage />);

    expect(await screen.findByRole("heading", { name: "Nothing runs on its own yet" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Browse workflows" })).toHaveAttribute("href", "/workflow");
    expect(screen.queryByTestId("coming-up-timeline")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "New automation" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });

  it("offers a retry when the list cannot load", async () => {
    listTriggers.mockRejectedValueOnce(new Error("unavailable"));

    renderAtRoute(<AutomationsListPage />);

    expect(await screen.findByRole("alert")).toHaveTextContent("Automations could not be loaded.");
    listTriggers.mockResolvedValue({ triggers: [protoTrigger()] });
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByTestId("automation-row-trig-1")).toBeInTheDocument();
  });
});
