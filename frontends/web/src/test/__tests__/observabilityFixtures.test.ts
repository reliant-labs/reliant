// Copyright (c) 2025 Reliant Labs

/**
 * The test harness's fixture transport (observabilityFixtures.tsx) answers
 * every procedure the shop pack's observability handlers serve from the
 * test's fake, whatever its service (a generated ObservabilityService read,
 * an AlertingService read), and sends only the procedures they do not serve
 * to the app's transport, where a test's setTransport answers them. So a test
 * on the harness never reaches a real backend for a read the fake owns.
 */

import { createClient } from "@connectrpc/connect";
import { afterEach, describe, expect, it, vi } from "vitest";

import { createFixtureApi } from "@/components/Forge/Observability/__fixtures__/fixtureApi";
import { AlertingService } from "@/gen/controlplane/services/alerting/v1/alerting_pb";
import { ObservabilityService } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { relativeWindow, serviceOverviewRequest } from "@/services/controlPlane/observability/api";

import { observabilityFixtureTransport } from "../observabilityFixtures";

const AT = Date.UTC(2026, 10, 10, 15, 0, 0);
const HOUR = 3_600_000;

/** The app's transport, standing in for the backend: it records what reaches it. */
function appBackend() {
  const reached: string[] = [];
  setTransport(
    mockTransport({
      "ObservabilityService.GetDashboards": () => (reached.push("GetDashboards"), {}),
      "ObservabilityService.GetServiceOverview": () => (reached.push("GetServiceOverview"), {}),
      "AlertingService.ListAlerts": () => (reached.push("ListAlerts"), {}),
    })
  );
  return reached;
}

afterEach(() => {
  setTransport(null);
});

describe("observabilityFixtureTransport", () => {
  it("answers a generated ObservabilityService read from the fake, with its relative window resolved at the scope's clock", async () => {
    const reached = appBackend();
    const api = createFixtureApi();
    const getServiceOverview = vi.spyOn(api, "getServiceOverview");
    const client = createClient(ObservabilityService, observabilityFixtureTransport(() => api, AT));
    await client.getServiceOverview(serviceOverviewRequest({ orgId: "org", envIds: ["env-prod"], filters: [], window: relativeWindow(HOUR) }));
    expect(getServiceOverview).toHaveBeenCalledWith(expect.objectContaining({ envIds: ["env-prod"], window: { startMs: AT - HOUR, endMs: AT } }));
    expect(reached).toEqual([]);
  });

  it("answers another service's read the handlers serve (AlertingService) from the fake, not the app's transport", async () => {
    const reached = appBackend();
    const api = createFixtureApi();
    const listAlerts = vi.spyOn(api, "listAlerts");
    const client = createClient(AlertingService, observabilityFixtureTransport(() => api, AT));
    await client.listAlerts({ orgId: "org" });
    expect(listAlerts).toHaveBeenCalledWith("org", undefined);
    expect(reached).toEqual([]);
  });

  it("sends a procedure the handlers do not serve to the app's transport", async () => {
    const reached = appBackend();
    const client = createClient(ObservabilityService, observabilityFixtureTransport(() => createFixtureApi(), AT));
    await client.getDashboards({});
    expect(reached).toEqual(["GetDashboards"]);
  });
});
