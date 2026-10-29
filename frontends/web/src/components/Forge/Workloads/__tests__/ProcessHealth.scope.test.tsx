// Copyright (c) 2025 Reliant Labs
import { render, screen, within } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { createTestQueryClient } from "@/test/renderWithQuery";
import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { AlertInstance } from "@/services/controlPlane/observability/alertingTypes";
import type { Workload } from "@/services/controlPlane/observability/types";
import { createFixtureApi } from "../../Observability/__fixtures__/fixtureApi";
import { ProcessHealth } from "../ProcessHealth";

vi.mock("@tanstack/react-router", async (original) => ({ ...await original<typeof import("@tanstack/react-router")>(), useNavigate: () => vi.fn() }));

describe("process health scope", () => {
  it("never joins prod telemetry to dev workload or another project's alert with the same process name", async () => {
    const base = createFixtureApi();
    const workload = (envId: string, envName: string, health: Workload["health"], cores: number): Workload => ({
      envId, envName, workload: "api", health, healthReason: envName,
      namespace: "", serviceVersion: "", replicasObserved: 1, restartsInWindow: 0,
      latestWarning: null, kind: "deployment", healthCause: "none", healthDetail: "",
      readyReplicas: 1, desiredReplicas: 1, releaseVersion: "v1", releaseBasis: "promotion", pods: [], declared: null,
      containers: [{ container: "api", imageName: "api", imageTag: "v1", restartsInWindow: 0, lastTerminatedReason: "", lastTerminatedAtMs: null,
        cpu: { latest: cores, p95: cores, limit: 10, request: null, limitUtilizationLatest: cores / 10, limitUtilizationP95: cores / 10, requestUtilizationLatest: null, requestUtilizationP95: null }, memory: null }],
    });
    const alert: AlertInstance = { envId: "other-project-prod", labels: { service: "api" }, state: "firing", rule: "X", fingerprint: "foreign", annotations: {}, activeAtMs: null, firingAtMs: null, lastEvalAtMs: null, lastValue: 1 };
    render(<QueryClientProvider client={createTestQueryClient()}><ObservabilityApiProvider api={{
      ...base, getServiceOverview: async (req) => ({ ...await base.getServiceOverview(req), services: (await base.getServiceOverview(req)).services.filter(row => row.name === "api") }),
    }}><ProcessHealth scope={{ orgId: "org", projectId: "shop", envIds: ["prod-id"], filters: [] }} range="1h"
      workloads={[workload("dev-id", "dev", "crash_looping", 9), workload("prod-id", "prod", "healthy", 1)]}
      alerts={[alert]} envNameOf={id => ({ "dev-id": "dev", "prod-id": "prod", "other-project-prod": "prod" })[id]}
      workloadHref={name => `/workloads/${name}`} logsHref={name => `/logs/${name}`} />
    </ObservabilityApiProvider></QueryClientProvider>);
    const row = await screen.findByTestId("observability-service-api");
    expect(within(row).getByText("Healthy")).toBeInTheDocument();
    expect(within(row).queryByText("Crash-looping")).not.toBeInTheDocument();
    expect(within(row).queryByText("1 firing")).not.toBeInTheDocument();
    const cpu = row.querySelector('td[data-label="CPU"]')!;
    expect(cpu).toHaveTextContent("10%");
    expect(cpu).not.toHaveTextContent("90%");
  });
});
