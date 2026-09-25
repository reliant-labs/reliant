// Copyright (c) 2025 Reliant Labs

/**
 * Hosted rows rendered from REAL forge output (see
 * services/forge/__tests__/hostedFixtures.test.ts for where the fixtures come
 * from). Pins: the URL is a link, the verdict uses the console's certainty
 * vocabulary (converging is NOT green), drift and a degraded workload's
 * last_error are shown, and the status panel reads `hosted_workloads`.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";

import { ForgeReachability } from "@/gen/reliant/v1/forge_pb";
import type { ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";
import { classifyForgeResponse, type ForgeTopologyReport } from "@/services/forge/topology";
import type { ForgeEnvStatusReport } from "@/services/forge/status";

import { TopologyView } from "../TopologyView";
import { EnvironmentCard } from "../Environments/EnvironmentCard";
import { EnvStatusPanel } from "../Status/EnvStatusPanel";
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

function renderTopology(report: ForgeTopologyReport) {
  return render(
    <TopologyView
      outcome={{ kind: "report", meta: meta(), report }}
      isLoading={false}
      onVerify={vi.fn()}
      projectName="acme"
    />
  );
}

/**
 * The per-workload list lives on the Environments card (the matrix row shows a
 * one-line summary — see "the topology matrix keeps a hosted env to one row").
 * Same topology env object, so the same fixture drives it.
 */
function renderCard(report: ForgeTopologyReport) {
  return render(<EnvironmentCard env={report.environments![0]} active onSelect={vi.fn()} />);
}

function classes(el: Element | null | undefined): string {
  return el?.getAttribute("class") ?? "";
}

describe("hosted environment card, from forge's own topology --json", () => {
  it("links the workload URL and paints converging with the UNKNOWN certainty treatment", () => {
    renderCard(topology());
    const row = screen.getByTestId("environment-card-hosted");

    const link = within(row).getByTestId("hosted-url-hosted-api");
    expect(link.tagName).toBe("A");
    expect(link.getAttribute("href")).toBe("https://api-acme.reliantapps.dev");

    const chip = within(row).getByTestId("hosted-workload-hosted-api").querySelector("[data-verdict]");
    expect(chip?.getAttribute("data-verdict")).toBe("converging");
    expect(chip?.textContent).toBe("Settling");
    // The stateVocabulary treatment, not a bespoke colour: dashed + unfilled.
    for (const cls of CERTAINTY_STYLES.unknown.container.split(" ")) expect(classes(chip)).toContain(cls);
    expect(classes(chip)).not.toContain("bg-success");

    expect(within(row).queryByTestId("hosted-error-hosted-api")).toBeNull();
    expect(row.querySelector("[data-drifted]")).toBeNull();
  });

  it("shows drift and last_error for a degraded workload", () => {
    const report = topology((r) => {
      const w = r.environments![0].workloads![0];
      w.verdict = "degraded";
      w.observed_state = "degraded";
      w.drifted = true;
      w.observed_digest = "sha256:2222222222222222222222222222222222222222222222222222222222222222";
      w.last_error = "CrashLoopBackOff: exit 137";
    });
    renderCard(report);
    const row = screen.getByTestId("environment-card-hosted");

    const chip = within(row).getByTestId("hosted-workload-hosted-api").querySelector("[data-verdict]");
    expect(chip?.getAttribute("data-certainty")).toBe("known-bad");
    for (const cls of CERTAINTY_STYLES["known-bad"].container.split(" ")) expect(classes(chip)).toContain(cls);

    expect(row.querySelector("[data-drifted]")?.textContent).toBe("Wrong version");
    // The visible text is forge's error verbatim; a screen reader also hears
    // a "Last error:" prefix so the line is not read as a bare fragment.
    expect(within(row).getByTestId("hosted-error-hosted-api").textContent).toBe(
      "Last error: CrashLoopBackOff: exit 137"
    );
  });

  it("does NOT show a stale last_error once the workload has converged", () => {
    renderCard(
      topology((r) => {
        const w = r.environments![0].workloads![0];
        w.verdict = "converged";
        w.last_error = "an error from before the last rollout";
      })
    );
    expect(screen.queryByTestId("hosted-error-hosted-api")).toBeNull();
  });
});

describe("the topology matrix keeps a hosted env to one row", () => {
  it("shows the env's health chip and ONE workload URL, not the whole list", () => {
    renderTopology(topology());
    const row = screen.getByTestId("env-row-hosted");
    // forge's env-level verdict ("converging"), in the certainty vocabulary.
    const chip = within(row).getByTestId("hosted-verdict-hosted");
    expect(chip.getAttribute("data-verdict")).toBe("converging");
    expect(chip.textContent).toBe("Settling");
    expect(within(row).getByTestId("hosted-url-hosted-api").getAttribute("href")).toBe(
      "https://api-acme.reliantapps.dev"
    );
    // The per-workload list is the Environments screen's job.
    expect(within(row).queryByTestId("hosted-workload-hosted-api")).toBeNull();
  });

  it("rolls a not-serving workload up into the env chip and an 'N not healthy' count", () => {
    renderTopology(
      topology((r) => {
        const env = r.environments![0];
        env.verdict = "";
        env.workloads!.push({ name: "web", verdict: "degraded", observed_state: "degraded" });
      })
    );
    const row = screen.getByTestId("env-row-hosted");
    // No env-level verdict from forge: the worst workload decides.
    expect(within(row).getByTestId("hosted-verdict-hosted").getAttribute("data-verdict")).toBe("degraded");
    expect(row.textContent).toContain("1 not healthy");
  });

  it("says a never-deployed hosted env is not deployed, never healthy or unknown-with-a-blank", () => {
    renderTopology(
      topology((r) => {
        r.environments![0].environment_id = "";
      })
    );
    const chip = within(screen.getByTestId("env-row-hosted")).getByTestId("hosted-verdict-hosted");
    expect(chip.getAttribute("data-verdict")).toBe("not-deployed");
    expect(chip.textContent).toBe("Not deployed yet");
    expect(chip.getAttribute("data-certainty")).toBe("unknown");
  });
});

describe("env status panel, from forge's own status --json", () => {
  it("renders hosted_workloads with URL and verdict", () => {
    render(
      <EnvStatusPanel
        outcome={classifyForgeResponse<ForgeEnvStatusReport>(meta(), statusJson)}
        isLoading={false}
        env="hosted"
      />
    );
    const section = screen.getByTestId("forge-status-hosted");
    expect(section.textContent).toContain("127.0.0.1:56171");
    const link = within(section).getByTestId("hosted-url-hosted-api");
    expect(link.getAttribute("href")).toBe("https://api-acme.reliantapps.dev");
    expect(
      within(section).getByTestId("hosted-workload-hosted-api").querySelector("[data-verdict]")?.getAttribute("data-verdict")
    ).toBe("converging");
  });
});
