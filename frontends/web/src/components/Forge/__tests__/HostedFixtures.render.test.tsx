// Copyright (c) 2025 Reliant Labs

/**
 * A hosted environment's row on the Environments table. Pins: the verdict
 * uses the console's certainty vocabulary, and a hosted env is ONE row.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";

import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import type { LiveEnv } from "@/services/forge/live";

import { EnvironmentTable } from "../Overview/EnvironmentTable";

/**
 * The Overview's single row.
 *
 * Its source is now the control plane, not forge's topology (R-LIVE), so the
 * row is built from a LiveEnv.
 */
function renderOverviewRow(env: LiveEnv, status?: CloudEnvStatus) {
  return render(
    <EnvironmentTable
      rows={[{ env, status, statusLoading: false }]}
      onOpen={vi.fn()}
    />
  );
}

function liveEnv(overrides: Partial<LiveEnv> = {}): LiveEnv {
  return {
    id: "denv_hosted",
    name: "hosted",
    project: "acme",
    kind: "persistent",
    declaredShape: null,
    declaredBy: null,
    release: "v1.5.15",
    releaseProvenance: null,
    promotedByActor: "",
    promotedByUserId: "",
    phase: "unspecified",
    provenance: "v1.5.15 · main@abc1234",
    holds: [],
    observed: { state: "not-reported" },
    drift: { state: "not-reported" },
    driftDetail: "",
    ...overrides,
  };
}

describe("the Overview keeps a hosted env to one row", () => {
  it("shows the kind and the platform's health chip, and lists no workloads", () => {
    renderOverviewRow(liveEnv(), {
      verdict: "converging",
      workloads: [],
      currentPromotion: null,
    });
    const row = screen.getByTestId("env-row-hosted");

    expect(row.textContent).toContain("Reliant cloud");
    // The platform's verdict, in the console's certainty vocabulary.
    const chip = within(row).getByTestId("health-hosted");
    expect(chip.getAttribute("data-verdict")).toBe("converging");
    expect(chip.textContent).toBe("Settling");
    // The per-workload list is the Environment page's job.
    expect(within(row).queryByTestId("hosted-workload-hosted-api")).toBeNull();

    // And NOT our own infrastructure (#366). This line used to assert the
    // opposite — that the row contained "127.0.0.1:56171", the control
    // plane's host, standing where a cluster env shows its kube context. The
    // customer did not choose it and cannot visit it.
    expect(row.textContent).not.toContain("127.0.0.1:56171");
    expect(row.textContent).not.toContain("denv_hosted");
    expect(row.textContent).not.toMatch(/control plane|endpoint/i);
  });

  it("shows where the release came from in the space the host used to take", () => {
    renderOverviewRow(liveEnv());
    expect(screen.getByTestId("provenance-hosted").textContent).toBe("v1.5.15 · main@abc1234");
  });

  it("takes the health verdict from the platform, not from a workload roll-up", () => {
    // The roll-up moved server-side: GetStatus returns one environment
    // verdict already computed over its deployments, so the row no longer
    // re-derives it from a workload array — which is how the two could
    // disagree, with the row's version winning on screen.
    renderOverviewRow(liveEnv(), { verdict: "degraded", workloads: [], currentPromotion: null });
    expect(screen.getByTestId("health-hosted").getAttribute("data-verdict")).toBe("degraded");
  });

  it("says a never-promoted env is just that, with no health claim pending", () => {
    renderOverviewRow(liveEnv({ release: "", provenance: "" }));
    const row = screen.getByTestId("env-row-hosted");
    expect(row.textContent).toMatch(/never promoted/i);
    // Unknown, not healthy and not a blank: nothing has reported on it.
    expect(within(row).getByTestId("health-hosted").getAttribute("data-certainty")).toBe("unknown");
  });
});
