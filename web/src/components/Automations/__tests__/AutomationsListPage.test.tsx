// Copyright (c) 2025 Reliant Labs

/**
 * The automations list, against the real data layer (trigger-grpc +
 * trigger-queries) with only the RPC client mocked: one row per trigger
 * showing its schedule in words, its last outcome and its next run, and an
 * enabled switch that sends SetTriggerEnabled.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";

import {
  ScheduleSourceSchema,
  SetTriggerEnabledResponseSchema,
  TriggerEventOutcome,
  TriggerEventSchema,
  TriggerSchema,
} from "@/gen/reliant/v1/trigger_pb";
import { HOUR, isoFromNow, renderAtRoute } from "./automationTestUtils";

const listTriggers = vi.fn();
const setTriggerEnabled = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    trigger: () => ({ listTriggers, setTriggerEnabled }),
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

vi.mock("@/store/projectStore", () => {
  const snapshot = () => ({
    projects: [{ id: "proj-1", name: "Reliant" }],
    currentProject: { id: "proj-1", name: "Reliant" },
    loadProjects: vi.fn(async () => undefined),
  });
  const useProjectStore = Object.assign(
    (selector?: (s: ReturnType<typeof snapshot>) => unknown) => (selector ? selector(snapshot()) : snapshot()),
    { getState: snapshot },
  );
  return { useProjectStore };
});

import { AutomationsListPage } from "../AutomationsListPage";

function protoTrigger(overrides: Partial<Parameters<typeof create<typeof TriggerSchema>>[1]> = {}) {
  return create(TriggerSchema, {
    id: "trig-1",
    name: "Morning triage",
    projectId: "proj-1",
    enabled: true,
    workflow: "builtin://agent",
    message: "Triage new issues",
    nextFireAt: isoFromNow(2 * HOUR + 60_000),
    lastEvent: create(TriggerEventSchema, {
      id: "ev-1",
      occurredAt: isoFromNow(-3 * HOUR),
      outcome: TriggerEventOutcome.FAILED,
      outcomeDetail: "model unavailable",
    }),
    source: {
      case: "schedule",
      value: create(ScheduleSourceSchema, { cron: ["0 9 * * 1-5"], timezone: "America/New_York" }),
    },
    ...overrides,
  });
}

describe("AutomationsListPage", () => {
  beforeEach(() => {
    listTriggers.mockReset();
    setTriggerEnabled.mockReset();
  });

  it("renders a trigger with its schedule, last outcome and next run", async () => {
    listTriggers.mockResolvedValue({ triggers: [protoTrigger()] });

    renderAtRoute(<AutomationsListPage />);

    const row = await screen.findByTestId("automation-row-trig-1");
    expect(within(row).getByRole("link", { name: "Morning triage" })).toHaveAttribute(
      "href",
      "/automations/trig-1",
    );
    expect(within(row).getByText("Reliant · Every weekday at 9:00 AM ET")).toBeInTheDocument();
    expect(within(row).getByText("Failed")).toBeInTheDocument();
    expect(within(row).getByText("3 hours ago")).toBeInTheDocument();
    expect(within(row).getByText("in 2 hours")).toBeInTheDocument();
    expect(within(row).getByRole("switch", { name: "Morning triage enabled" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    // Listed across every project: no project filter on the request.
    expect(listTriggers.mock.calls[0]![0].projectId).toBeUndefined();
  });

  it("shows a never-run, paused trigger as such", async () => {
    listTriggers.mockResolvedValue({
      triggers: [protoTrigger({ enabled: false, lastEvent: undefined, nextFireAt: undefined })],
    });

    renderAtRoute(<AutomationsListPage />);

    const row = await screen.findByTestId("automation-row-trig-1");
    expect(within(row).getByText("Never run")).toBeInTheDocument();
    expect(within(row).getByText("Paused")).toBeInTheDocument();
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

    expect(await screen.findByRole("heading", { name: "No automations yet" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "New automation" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });
});
