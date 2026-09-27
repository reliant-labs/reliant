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
import { envFacts, joinEnvironments } from "@/services/forge/environments";
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

/** The Overview row, with no control plane row — forge's report is the only source. */
function renderOverview(report: ForgeTopologyReport) {
  const rows = joinEnvironments(report.environments ?? [], []).map((summary) => ({
    summary,
    facts: envFacts(summary, undefined),
  }));
  return render(
    <EnvironmentTable
      rows={rows}
      promoteRelease={report.latest_release ?? null}
      canShip
      onOpen={vi.fn()}
      onPromote={vi.fn()}
      onDeploy={vi.fn()}
    />
  );
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
  it("says where it runs, shows the env's health chip, and lists no workloads", () => {
    renderOverview(topology());
    const row = screen.getByTestId("env-row-hosted");
    expect(within(row).getByTestId("where-hosted").getAttribute("data-where")).toBe("cloud");
    expect(within(row).getByTestId("where-hosted").textContent).toBe("Reliant cloud");
    // forge's env-level verdict ("converging"), in the certainty vocabulary.
    const chip = within(row).getByTestId("health-hosted");
    expect(chip.getAttribute("data-verdict")).toBe("converging");
    expect(chip.textContent).toBe("Settling");
    // The per-workload list is the Environment page's job.
    expect(within(row).queryByTestId("hosted-workload-hosted-api")).toBeNull();
    // The control plane's host stands where a cluster env shows its kube context.
    expect(row.textContent).toContain("127.0.0.1:56171");
  });

  it("rolls a not-serving workload up into the env's health", () => {
    renderOverview(
      topology((r) => {
        const env = r.environments![0];
        env.verdict = "";
        env.workloads!.push({ name: "web", verdict: "degraded", observed_state: "degraded" });
      })
    );
    // No env-level verdict from forge: the worst workload decides.
    expect(screen.getByTestId("health-hosted").getAttribute("data-verdict")).toBe("degraded");
  });

  it("says a never-deployed hosted env is not deployed, never healthy or unknown-with-a-blank", () => {
    renderOverview(
      topology((r) => {
        r.environments![0].environment_id = "";
      })
    );
    const chip = screen.getByTestId("health-hosted");
    expect(chip.textContent).toBe("Not deployed yet");
    expect(chip.getAttribute("data-certainty")).toBe("unknown");
  });
});
