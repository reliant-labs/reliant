// Copyright (c) 2025 Reliant Labs

/**
 * Hosted rows rendered from REAL forge output (see
 * services/forge/__tests__/fixtures/, captured from the hosted-deploy e2e this
 * was built from). Pins: the URL is a link, the verdict uses the console's
 * certainty vocabulary, drift and last_error show only while it matters — and
 * the Overview keeps a hosted env to ONE row.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";

import { ForgeReachability } from "@/gen/reliant/v1/forge_pb";
import type { ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";
import { classifyForgeResponse, type ForgeTopologyReport } from "@/services/forge/topology";
import type { ForgeEnvStatusReport } from "@/services/forge/status";
import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import type { LiveEnv } from "@/services/forge/live";
import { hostedWorkloadsOfStatus } from "@/services/forge/status";

import { HostedWorkloadList } from "../HostedWorkloads";
import { EnvironmentTable } from "../Overview/EnvironmentTable";
import { CERTAINTY_STYLES } from "../stateVocabulary";

import topologyJson from "@/services/forge/__tests__/fixtures/hosted-topology.json?raw";
import statusJson from "@/services/forge/__tests__/fixtures/hosted-status.json?raw";

function meta(): ForgeReportMeta {
  return {
    isForgeProject: true,
    supported: true,
    forgeVersion: "v0.1.18",
    unsupportedReason: "",
    exitCode: 0,
    reachability: ForgeReachability.UNSPECIFIED,
    unreachableReason: "",
  } as ForgeReportMeta;
}

function topology(mutate?: (r: ForgeTopologyReport) => void): ForgeTopologyReport {
  const outcome = classifyForgeResponse<ForgeTopologyReport>(meta(), topologyJson);
  if (outcome.kind !== "report") throw new Error(outcome.kind);
  const report = structuredClone(outcome.report);
  mutate?.(report);
  return report;
}

/** The env's workload list, as the Environment page's Workloads section draws it. */
function renderWorkloads(report: ForgeTopologyReport) {
  const env = report.environments![0];
  return render(<HostedWorkloadList envName={env.env} workloads={env.workloads ?? []} />);
}

/**
 * The Overview's single row.
 *
 * Its source is now the control plane, not forge's topology (R-LIVE), so the
 * row is built from a LiveEnv. The fixture above still drives the per-workload
 * list, which IS forge's — it is the Environment page's Preview surface.
 */
function renderOverviewRow(env: LiveEnv, status?: CloudEnvStatus) {
  return render(
    <EnvironmentTable
      rows={[{ env, status, statusLoading: false }]}
      onOpen={vi.fn()}
      onPreview={vi.fn()}
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
    ...overrides,
  };
}

function classes(el: Element | null | undefined): string {
  return el?.getAttribute("class") ?? "";
}

describe("hosted workloads, from forge's own topology --json", () => {
  it("links the workload URL and paints converging with the UNKNOWN certainty treatment", () => {
    renderWorkloads(topology());

    const link = screen.getByTestId("hosted-url-hosted-api");
    expect(link.tagName).toBe("A");
    expect(link.getAttribute("href")).toBe("https://api-acme.reliantapps.dev");

    const chip = screen.getByTestId("hosted-workload-hosted-api").querySelector("[data-verdict]");
    expect(chip?.getAttribute("data-verdict")).toBe("converging");
    expect(chip?.textContent).toBe("Settling");
    // The stateVocabulary treatment, not a bespoke colour: dashed + unfilled.
    for (const cls of CERTAINTY_STYLES.unknown.container.split(" ")) expect(classes(chip)).toContain(cls);
    expect(classes(chip)).not.toContain("bg-success");

    expect(screen.queryByTestId("hosted-error-hosted-api")).toBeNull();
    expect(document.querySelector("[data-drifted]")).toBeNull();
  });

  it("shows drift and last_error for a degraded workload", () => {
    renderWorkloads(
      topology((r) => {
        const w = r.environments![0].workloads![0];
        w.verdict = "degraded";
        w.observed_state = "degraded";
        w.drifted = true;
        w.observed_digest = "sha256:2222222222222222222222222222222222222222222222222222222222222222";
        w.last_error = "CrashLoopBackOff: exit 137";
      })
    );

    const chip = screen.getByTestId("hosted-workload-hosted-api").querySelector("[data-verdict]");
    expect(chip?.getAttribute("data-certainty")).toBe("known-bad");
    for (const cls of CERTAINTY_STYLES["known-bad"].container.split(" ")) expect(classes(chip)).toContain(cls);

    expect(document.querySelector("[data-drifted]")?.textContent).toBe("Wrong version");
    // The visible text is forge's error verbatim; a screen reader also hears
    // a "Last error:" prefix so the line is not read as a bare fragment.
    expect(screen.getByTestId("hosted-error-hosted-api").textContent).toBe(
      "Last error: CrashLoopBackOff: exit 137"
    );
  });

  it("does NOT show a stale last_error once the workload has converged", () => {
    renderWorkloads(
      topology((r) => {
        const w = r.environments![0].workloads![0];
        w.verdict = "converged";
        w.last_error = "an error from before the last rollout";
      })
    );
    expect(screen.queryByTestId("hosted-error-hosted-api")).toBeNull();
  });

  it("reads env status's hosted_workloads — the same object under a different key", () => {
    const outcome = classifyForgeResponse<ForgeEnvStatusReport>(meta(), statusJson);
    if (outcome.kind !== "report") throw new Error(outcome.kind);
    render(<HostedWorkloadList envName="hosted" workloads={hostedWorkloadsOfStatus(outcome.report)} />);
    expect(screen.getByTestId("hosted-url-hosted-api").getAttribute("href")).toBe(
      "https://api-acme.reliantapps.dev"
    );
  });
});

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
