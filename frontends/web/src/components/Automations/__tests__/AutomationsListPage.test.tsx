// Copyright (c) 2025 Reliant Labs

/**
 * The automations list, against the real data layer (trigger-model +
 * trigger-queries) with only the RPC client mocked: grouped rows with the
 * server's health, the launched run's own status, names straight off the
 * trigger, the "Needs attention" group, the "Coming up" strip, and an enabled
 * switch that sends SetTriggerEnabled.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create, type MessageInitShape } from "@bufbuild/protobuf";

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
import { setTransport } from "@/lib/connect";
import { serviceHandlers } from "@/test/serviceTransport";
import { mockTransport } from "@/lib/test-utils";

const listTriggers = vi.fn();
const setTriggerEnabled = vi.fn();
const listDaemons = vi.fn(async () => ({ daemons: [] }));

// TriggerService is on its generated hooks, so it is answered at the transport.
// Integration events are named from the catalog's trigger types; none here.
setTransport(
  mockTransport({
    ...serviceHandlers("WorkflowService", { listWorkflows: vi.fn(async () => ({ workflows: [], invalidWorkflows: [] })) }),
    ...serviceHandlers("TriggerService", { listTriggers, setTriggerEnabled }),
    ...serviceHandlers("DaemonRegistryService", { listDaemons }),
    "CatalogService.SearchCatalog": { response: {} },
  }),
);

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
      projectStoreSelector(selector);
      return selector ? selector(snapshot()) : snapshot();
    },
    { getState: snapshot },
  );
  return { useProjectStore };
});

import { AutomationsListPage } from "../AutomationsListPage";

/** A fixture's overrides: any init fields but the message's own type tag. */
type TriggerInit = Omit<MessageInitShape<typeof TriggerSchema>, "$typeName">;

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

    const row = await screen.findByTestId("automation-unit-custom:trig-1");
    expect(within(row).getByRole("link", { name: "Morning triage" })).toHaveAttribute(
      "href",
      "/workflows/automations/trig-1",
    );
    expect(within(row).getByText("Reliant · MacBook")).toBeInTheDocument();
    expect(within(row).getByText("Every weekday at 9:00 AM ET")).toBeInTheDocument();
    expect(within(row).getByText("Healthy")).toBeInTheDocument();
    expect(within(row).getByText("Completed")).toBeInTheDocument();
    expect(within(row).getByText("3 hours ago")).toBeInTheDocument();
    expect(within(row).getByText("in 2 hours")).toBeInTheDocument();
    expect(within(row).getByRole("switch", { name: "Morning triage in Reliant on" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    // Listed across every project: no project filter on the request.
    expect(listTriggers.mock.calls[0]![0].projectId).toBeUndefined();
  });

  it("says No machine for an automation that runs with no machine, never a daemon", async () => {
    // A no-machine automation has an empty daemonId by design
    // (research/DAEMONLESS_RUNS.md), not a daemon whose name is missing.
    listTriggers.mockResolvedValue({
      triggers: [protoTrigger({ daemonId: "", daemonName: "", noMachine: true })],
    });

    renderAtRoute(<AutomationsListPage />);

    const row = await screen.findByTestId("automation-unit-custom:trig-1");
    expect(within(row).getByText("Reliant · No machine")).toBeInTheDocument();
    expect(within(row).queryByText(/daemon/i)).not.toBeInTheDocument();
  });

  it("names come from the trigger itself, with no store or daemon lookup", async () => {
    listTriggers.mockResolvedValue({ triggers: [protoTrigger()] });

    renderAtRoute(<AutomationsListPage />);

    const row = await screen.findByTestId("automation-unit-custom:trig-1");
    // On the very first render with data, not after some store fills in.
    expect(within(row).getByText("Reliant · MacBook")).toBeInTheDocument();
    // The page reads the store for the CURRENT project only (whose library
    // it lists not-yet-active triggers from), never for a row's names: the
    // store's project list is empty here, so a name taken from it would be
    // blank.
    for (const [selector] of projectStoreSelector.mock.calls) {
      expect(String(selector)).not.toMatch(/projects\b/);
    }
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

    const row = await screen.findByTestId("automation-unit-custom:trig-1");
    const failed = within(row).getByText("Failed");
    expect(failed.closest("[data-run-status]")).toHaveAttribute("data-run-status", "failed");
    expect(within(row).queryByText("Launched")).toBeNull();
    expect(row.querySelector('[class*="bg-success"]')).toBeNull();
  });

  it("a firing that never became a run is Can't start, under Needs you, with the reason and Open settings", async () => {
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

    const row = await screen.findByTestId("needs-you-custom:trig-1");
    expect(within(row).getByText("Can't start")).toBeInTheDocument();
    expect(within(row).getByText("model unavailable")).toBeInTheDocument();
    expect(within(row).getByRole("button", { name: "Open settings for Morning triage in Reliant" })).toBeInTheDocument();
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

    const row = await screen.findByTestId("automation-unit-custom:trig-1");
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

    const needsYou = await screen.findByTestId("automations-needs-you");
    expect(within(needsYou).getByRole("heading", { name: "Needs you" })).toBeInTheDocument();
    const ids = within(needsYou)
      .getAllByRole("listitem")
      .map((li) => li.getAttribute("data-testid"));
    expect(ids).toEqual(["needs-you-custom:fail", "needs-you-custom:wait", "needs-you-custom:skip"]);
    expect(within(needsYou).getByText("2 failed in a row")).toBeInTheDocument();
    // Each row carries its one call to action.
    expect(within(needsYou).getByRole("button", { name: "Open last run for Broken in Reliant" })).toBeInTheDocument();
    expect(within(needsYou).getByRole("button", { name: "Open run for Sleepy in Reliant" })).toBeInTheDocument();
    expect(within(needsYou).getByRole("button", { name: "See why for Skipper in Reliant" })).toBeInTheDocument();
    expect(within(needsYou).getByTestId("needs-you-custom:wait")).toHaveAttribute("data-health", "waiting_for_machine");
    // Each automation appears once on the page, and the counts say so.
    expect(screen.queryByTestId("automation-unit-custom:fail")).toBeNull();
    expect(within(screen.getByTestId("automation-group-workflow:builtin://agent")).getAllByRole("listitem")).toHaveLength(1);
    const counts = screen.getByTestId("automation-counts");
    expect(within(counts).getByRole("link", { name: /3\s*need you/ })).toHaveAttribute("href", "#needs-you");
  });

  it("shows a workflow turned on in a project as one automation, linking to its workflow page", async () => {
    listTriggers.mockResolvedValue({
      triggers: [
        protoTrigger({ id: "a1", name: "PR review · pr-opened", workflow: "builtin://autopilot-pr-review", workflowTrigger: "pr-opened" }),
        protoTrigger({ id: "a2", name: "PR review · pr-ready", workflow: "builtin://autopilot-pr-review", workflowTrigger: "pr-ready" }),
      ],
    });

    renderAtRoute(<AutomationsListPage />);

    const row = await screen.findByTestId("automation-unit-autopilot-pr-review::proj-1");
    expect(within(row).getByRole("link", { name: "Autopilot Pr Review" }).getAttribute("href")).toBe(
      "/workflows/library/builtin%3A%2F%2Fautopilot-pr-review?project=proj-1#automation-proj-1",
    );
    expect(within(row).getByText("2 triggers")).toBeInTheDocument();
    expect(screen.getAllByRole("switch")).toHaveLength(1);
  });

  it("has no Needs you section when nothing needs the user", async () => {
    listTriggers.mockResolvedValue({ triggers: [protoTrigger()] });

    renderAtRoute(<AutomationsListPage />);

    await screen.findByTestId("automation-unit-custom:trig-1");
    expect(screen.queryByTestId("automations-needs-you")).toBeNull();
    expect(screen.queryByText("Needs you")).toBeNull();
  });

  it("groups by workflow by default and by project on request", async () => {
    listTriggers.mockResolvedValue({
      triggers: [
        protoTrigger({ id: "a", name: "A", workflow: "builtin://agent", projectId: "p1", projectName: "Zeta" }),
        protoTrigger({ id: "b", name: "B", workflow: "triage", projectId: "p1", projectName: "Zeta" }),
        protoTrigger({ id: "c", name: "C", workflow: "builtin://agent", projectId: "p2", projectName: "Alpha" }),
      ],
    });

    renderAtRoute(<AutomationsListPage />);

    await screen.findByTestId("automation-unit-custom:a");
    expect(screen.getByRole("button", { name: "Workflow" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent)).toEqual([
      "Coming up",
      "On",
      "Agent",
      "Triage",
    ]);

    await userEvent.click(screen.getByRole("button", { name: "Project" }));
    expect(screen.getByRole("button", { name: "Project" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent)).toEqual([
      "Coming up",
      "On",
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
    await screen.findByTestId("automation-unit-custom:trig-1");
    expect(screen.queryByTestId("coming-up-timeline")).toBeNull();
  });

  it("sends SetTriggerEnabled when the switch is flipped", async () => {
    listTriggers.mockResolvedValueOnce({ triggers: [protoTrigger()] });
    // Hold the RPC open so the assertion below sees the optimistic state, not
    // the server's answer.
    let answer: (value: unknown) => void = () => undefined;
    setTriggerEnabled.mockReturnValue(new Promise((resolve) => (answer = resolve)));

    renderAtRoute(<AutomationsListPage />);
    const toggle = await screen.findByRole("switch", { name: "Morning triage in Reliant on" });
    await userEvent.click(toggle);

    await waitFor(() => expect(setTriggerEnabled).toHaveBeenCalledTimes(1));
    expect(setTriggerEnabled.mock.calls[0]![0]).toMatchObject({ id: "trig-1", enabled: false });
    // Optimistic: the switch moves before the server answers.
    expect(screen.getByRole("switch", { name: "Morning triage in Reliant on" })).toHaveAttribute(
      "aria-checked",
      "false",
    );

    // The server confirms; the list refetch then reflects the stored state.
    listTriggers.mockResolvedValue({ triggers: [protoTrigger({ enabled: false })] });
    answer(create(SetTriggerEnabledResponseSchema, { trigger: protoTrigger({ enabled: false }) }));
    await waitFor(() => expect(listTriggers).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("switch", { name: "Morning triage in Reliant on" })).toHaveAttribute(
      "aria-checked",
      "false",
    );
  });

  it("rolls the switch back when SetTriggerEnabled fails", async () => {
    listTriggers.mockResolvedValue({ triggers: [protoTrigger()] });
    setTriggerEnabled.mockRejectedValue(new Error("boom"));

    renderAtRoute(<AutomationsListPage />);
    await userEvent.click(await screen.findByRole("switch", { name: "Morning triage in Reliant on" }));

    await waitFor(() => expect(setTriggerEnabled).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(screen.getByRole("switch", { name: "Morning triage in Reliant on" })).toHaveAttribute(
        "aria-checked",
        "true",
      ),
    );
  });

  it("points to the Library when nothing runs by itself, and still offers a custom automation", async () => {
    listTriggers.mockResolvedValue({ triggers: [] });

    renderAtRoute(<AutomationsListPage />);

    expect(await screen.findByRole("heading", { name: "Nothing runs by itself yet." })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Browse the Library" })).toHaveAttribute("href", "/workflows/library");
    expect(screen.queryByTestId("coming-up-timeline")).toBeNull();
    expect(screen.queryByText("Not enabled yet")).toBeNull();
    await userEvent.click(within(screen.getByTestId("automations-empty")).getByRole("button", { name: "Custom automation" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });

  it("offers a retry when the list cannot load", async () => {
    listTriggers.mockRejectedValueOnce(new Error("unavailable"));

    renderAtRoute(<AutomationsListPage />);

    expect(await screen.findByRole("alert")).toHaveTextContent("Automations could not be loaded.");
    listTriggers.mockResolvedValue({ triggers: [protoTrigger()] });
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByTestId("automation-unit-custom:trig-1")).toBeInTheDocument();
  });
});
