// Copyright (c) 2025 Reliant Labs

/**
 * The Automations page lists the workflows that DECLARE triggers nothing runs
 * yet ("Not enabled yet", one Enable per workflow), even when no automation
 * exists at all — and a draft's declarations are left out, since a draft
 * cannot be enabled.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";

import { WorkflowDraftStatus } from "@/gen/reliant/v1/workflow_pb";
import { renderAtRoute } from "./automationTestUtils";

const listTriggers = vi.fn();
const listWorkflows = vi.fn();

vi.mock("@/api/grpc-client", () => ({
  grpcClient: {
    trigger: () => ({ listTriggers }),
    workflow: () => ({ listWorkflows }),
    daemonRegistry: () => ({ listDaemons: vi.fn(async () => ({ daemons: [] })) }),
  },
}));

vi.mock("@/hooks/useTitleBarChrome", () => ({
  useTitleBarChrome: () => ({ isElectron: false, isMac: false, isFullscreen: false, trafficLightPadding: "8px", dragRegionStyle: {}, noDragRegionStyle: {} }),
}));

vi.mock("@/store/projectStore", () => {
  const snapshot = () => ({ projects: [], currentProject: { id: "proj-1", name: "Reliant" }, loadProjects: vi.fn(async () => undefined) });
  const useProjectStore = Object.assign(
    (selector?: (s: ReturnType<typeof snapshot>) => unknown) => (selector ? selector(snapshot()) : snapshot()),
    { getState: snapshot },
  );
  return { useProjectStore };
});

import { AutomationsListPage } from "../AutomationsListPage";

const schedule = { name: "nightly", filter: "", inputs: {}, prompt: "", source: { case: "schedule", value: { cron: ["0 9 * * 1-5"], timezone: "UTC" } } };

beforeEach(() => {
  listTriggers.mockReset();
  listWorkflows.mockReset();
  listTriggers.mockResolvedValue({ triggers: [] });
  listWorkflows.mockResolvedValue({
    workflows: [
      { name: "digest", title: "Digest", filename: "digest", source: "user", stepCount: 1, nodes: [], edges: [], status: WorkflowDraftStatus.COMPLETE, validationErrors: [], triggers: [schedule] },
      { name: "half-done", filename: "half-done", source: "user", stepCount: 1, nodes: [], edges: [], status: WorkflowDraftStatus.DRAFT, validationErrors: [], triggers: [schedule] },
    ],
    invalidWorkflows: [],
  });
});

describe("AutomationsListPage: declared but not enabled", () => {
  it("lists a complete workflow with Enable, beside the empty state", async () => {
    renderAtRoute(<AutomationsListPage />);
    const section = await screen.findByRole("region", { name: "Not enabled yet" });
    expect(within(section).getByText("Digest")).toBeInTheDocument();
    expect(within(section).getByText("Every weekday at 9:00 AM UTC · Not enabled")).toBeInTheDocument();
    expect(within(section).getByRole("button", { name: "Enable Digest" })).toBeInTheDocument();
    expect(within(section).queryByRole("button", { name: /Activate/ })).toBeNull();
    expect(within(section).queryByText(/half-done/)).toBeNull();
    // Still the empty state: nothing runs on its own yet.
    expect(screen.getByText("Nothing runs on its own yet")).toBeInTheDocument();
    expect(screen.getAllByText(/on a schedule, a webhook or an app event/).length).toBeGreaterThan(0);
  });
});
