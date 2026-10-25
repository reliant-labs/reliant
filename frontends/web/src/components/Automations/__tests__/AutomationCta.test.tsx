// Copyright (c) 2025 Reliant Labs

/**
 * The one call to action per state does what it says, in every place it is
 * shown: resume the paused triggers, remove a broken one after confirming,
 * open the newest run, and open setup (finish / settings) for the workflow.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { Trigger, TriggerHealth } from "@/hooks/trigger-model";
import { renderAtRoute } from "./automationTestUtils";
import { create } from "@bufbuild/protobuf";
import { SearchCatalogResponseSchema } from "@/gen/reliant/v1/catalog_pb";
import { setTransport } from "@/lib/connect";
import { serviceHandlers } from "@/test/serviceTransport";
import { mockTransport } from "@/lib/test-utils";
import { serviceHandlers } from "@/test/serviceTransport";

const setTriggerEnabled = vi.fn();
const deleteTrigger = vi.fn();
const getWorkflow = vi.fn();
const listWorkflows = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    daemonRegistry: () => ({ listDaemons: vi.fn(async () => ({ daemons: [] })) }),
  },
}));

// TriggerService, CatalogService and ConnectionService are on their generated hooks, so they are answered at the transport.
setTransport(
  mockTransport({
    ...serviceHandlers("TriggerService", { setTriggerEnabled, deleteTrigger, listTriggers: vi.fn(async () => ({ triggers: [] })) }),
    // The run form lists workspaces through the generated WorktreeService hooks.
    "WorktreeService.ListWorktrees": { response: { worktrees: [] } },
    ...serviceHandlers("WorkflowService", { getWorkflow, listWorkflows }),
    "CatalogService.SearchCatalog": { response: create(SearchCatalogResponseSchema) },
    "ConnectionService.ListConnections": { response: {} },
    "ProjectService.ListProjectDaemons": { response: {} },
  }),
);

vi.mock("@/store/projectStore", () => {
  const state = { projects: [{ id: "p1", name: "Reliant" }], currentProject: { id: "p1", name: "Reliant" }, loadProjects: vi.fn() };
  const useProjectStore = Object.assign((selector?: (s: typeof state) => unknown) => (selector ? selector(state) : state), { getState: () => state });
  return { useProjectStore };
});

import { AutomationCta } from "../AutomationCta";

const health = (status: TriggerHealth["status"]): TriggerHealth => ({ status, consecutiveFailures: 0, consecutiveSkips: 0, lastFailureDetail: "" });

function trigger(overrides: Partial<Trigger> & { id: string }): Trigger {
  return {
    name: `Triage · ${overrides.id}`,
    projectId: "p1",
    enabled: true,
    workflow: "triage",
    workflowTrigger: overrides.id,
    presets: {},
    params: {},
    message: "",
    daemonId: "",
    noMachine: true,
    notifyOnComplete: false,
    actingIdentity: "owner",
    health: health("healthy"),
    createdAt: "",
    updatedAt: "",
    source: { kind: "unknown" } as Trigger["source"],
    ...overrides,
  };
}

beforeEach(() => {
  for (const m of [setTriggerEnabled, deleteTrigger, getWorkflow, listWorkflows]) m.mockReset();
  listWorkflows.mockResolvedValue({ workflows: [], invalidWorkflows: [] });
});

describe("AutomationCta", () => {
  it("Resume turns every paused trigger back on, and only those", async () => {
    setTriggerEnabled.mockResolvedValue({});
    renderAtRoute(
      <AutomationCta
        cta={{ kind: "resume", label: "Resume" }}
        triggers={[trigger({ id: "a", enabled: false }), trigger({ id: "b" }), trigger({ id: "c", enabled: false })]}
        workflowRef="triage"
        projectId="p1"
        subject="Triage"
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Resume for Triage" }));
    await waitFor(() => expect(setTriggerEnabled).toHaveBeenCalledTimes(2));
    expect(setTriggerEnabled.mock.calls.map(([req]) => [req.id, req.enabled])).toEqual([
      ["a", true],
      ["c", true],
    ]);
  });

  it("Remove trigger removes the broken one after confirming", async () => {
    deleteTrigger.mockResolvedValue({});
    renderAtRoute(
      <AutomationCta
        cta={{ kind: "remove_trigger", label: "Remove trigger" }}
        triggers={[trigger({ id: "ok" }), trigger({ id: "gone", health: health("broken") })]}
        workflowRef="builtin://autopilot-pr-review"
        projectId="p1"
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Remove trigger" }));
    const dialog = await screen.findByRole("dialog", { name: "Remove “gone”?" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Remove trigger" }));
    await waitFor(() => expect(deleteTrigger).toHaveBeenCalledTimes(1));
    expect(deleteTrigger.mock.calls[0]![0]).toMatchObject({ id: "gone" });
  });

  it("Open last run goes to the newest run of any trigger", async () => {
    const { router } = renderAtRoute(
      <AutomationCta
        cta={{ kind: "open_last_run", label: "Open last run" }}
        triggers={[
          trigger({ id: "a", lastEvent: { id: "e1", occurredAt: "2026-10-01T09:00:00Z", outcome: "launched", outcomeDetail: "", chatId: "older", manual: false } }),
          trigger({ id: "b", lastEvent: { id: "e2", occurredAt: "2026-10-02T09:00:00Z", outcome: "launched", outcomeDetail: "", chatId: "newer", manual: false } }),
        ]}
        workflowRef="triage"
        projectId="p1"
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Open last run" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/workflows/runs/newer"));
  });
});
