// Copyright (c) 2025 Reliant Labs

// Main's process ranking assertions, moved with the surviving Overview table.
import { describe, expect, it } from "vitest";
import type { AlertInstance } from "@/services/controlPlane/observability/alertingTypes";
import type { ServiceHealth, Workload } from "@/services/controlPlane/observability/types";
import { byAttention, serviceRows } from "../homeModel";

describe("process health attention order", () => {
  const health = (name: string, over: Partial<ServiceHealth> = {}): ServiceHealth => ({
    name, group: {}, requestCount: 100, requestRate: 1, errorCount: 0, errorRate: 0,
    p50Ms: 1, p95Ms: 2, p99Ms: 3, errorLogCount: 0, timestamps: [], rateSeries: [],
    errorRateSeries: [], requestTrend: [], errorTrend: [], source: "", operatorData: false, ...over,
  });
  const workload = (name: string, health: Workload["health"], healthReason = ""): Workload =>
    ({ envId: "env-prod", envName: "prod", workload: name, namespace: "", serviceVersion: "", replicasObserved: 1, restartsInWindow: 5, containers: [], latestWarning: null, health, healthReason }) as unknown as Workload;
  const alert = (service: string): AlertInstance => ({ envId: "env-prod", rule: "X", fingerprint: service, labels: { service }, annotations: {}, state: "firing", activeAtMs: null, firingAtMs: null, lastEvalAtMs: null, lastValue: 1 });

  it("puts crash-looping first, then signals, without inventing workload verdicts", () => {
    const rows = serviceRows(
      [health("quiet", { requestCount: 0 }), health("ok"), health("noisy", { errorRate: 0.2 }), health("paged"), health("looping")],
      [workload("looping", "crash_looping", "restarted 5x, last exit OOMKilled")],
      [alert("paged")], () => "prod"
    ).sort(byAttention);
    expect(rows.map(r => [r.name, r.status, r.tone])).toEqual([
      ["looping", "Crash-looping", "danger"], ["paged", "Alert firing", "neutral"],
      ["noisy", "Errors elevated", "neutral"], ["ok", "No workload data", "neutral"],
      ["quiet", "No workload data", "neutral"],
    ]);
    expect(rows[1].reason).toBe("1 alert firing");
    expect(rows[0].reason).toBe("restarted 5x, last exit OOMKilled");
  });

  it("lists a platform-sourced process of the project's own environment (tenant zero's control-plane)", () => {
    // Every control-plane process stamps reliant.source=platform, so the server marks it
    // operator data; scoped to control-plane's environments, it is still one of its processes.
    const rows = serviceRows([health("admin-server", { source: "platform", operatorData: true })], [], [], () => "prod");
    expect(rows.map((r) => r.name)).toEqual(["admin-server"]);
  });

  it("takes the server workload verdict even when request errors are elevated", () => {
    const [row] = serviceRows([health("api", { errorRate: 0.2 })], [workload("api", "healthy")], [], () => "prod");
    expect(row).toMatchObject({ status: "Healthy", tone: "success", reason: "high error rate" });
  });
});
