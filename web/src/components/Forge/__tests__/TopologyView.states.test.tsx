// Copyright (c) 2025 Reliant Labs

/**
 * The four non-report outcomes.
 *
 * Every one of these is a SUCCESSFUL RPC carrying data, and each has to render as
 * itself. The failure these tests guard against is the generic one: any of them
 * silently producing an empty screen, which a user reads as "I have no
 * environments" — a false statement in all four cases.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

import { TopologyView } from "../TopologyView";
import { classifyForgeResponse } from "@/services/forge/topology";
import type { ForgeOutcome, ForgeTopologyReport } from "@/services/forge/topology";
import { ForgeReachability } from "@/gen/reliant/v1/forge_pb";
import type { ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";

function meta(overrides: Partial<ForgeReportMeta> = {}): ForgeReportMeta {
  return {
    isForgeProject: true,
    supported: true,
    forgeVersion: "v0.9.1",
    unsupportedReason: "",
    exitCode: 0,
    reachability: ForgeReachability.UNSPECIFIED,
    unreachableReason: "",
    ...overrides,
  } as ForgeReportMeta;
}

function renderOutcome(outcome: ForgeOutcome<ForgeTopologyReport> | undefined, isLoading = false) {
  return render(
    <TopologyView
      outcome={outcome}
      isLoading={isLoading}
      onVerify={vi.fn()}
      projectName="some-project"
    />
  );
}

describe("is_forge_project: false", () => {
  it("renders a sensible non-error empty state, not an error and not a blank screen", () => {
    // Classified from a raw response, so the test covers the mapping too.
    const outcome = classifyForgeResponse<ForgeTopologyReport>(
      meta({ isForgeProject: false }),
      ""
    );
    expect(outcome.kind).toBe("not-forge-project");

    renderOutcome(outcome);
    const panel = screen.getByTestId("forge-not-project");

    expect(panel.textContent).toContain("forge.yaml");
    // Says this is expected, so it does not read as a fault.
    expect(panel.textContent).toContain("expected");
    // Not styled as an error.
    expect(panel.className).not.toMatch(/destructive/);
    // And definitely not an empty screen.
    expect(panel.textContent?.trim().length ?? 0).toBeGreaterThan(40);
    expect(screen.queryByTestId("forge-topology")).toBeNull();
  });
});

describe("supported: false", () => {
  it("says forge is too old AND names the version, rather than rendering an empty screen", () => {
    const outcome = classifyForgeResponse<ForgeTopologyReport>(
      meta({ supported: false, forgeVersion: "v0.4.2", unsupportedReason: "unknown flag: --json" }),
      ""
    );
    expect(outcome.kind).toBe("unsupported");

    renderOutcome(outcome);
    const panel = screen.getByTestId("forge-unsupported");

    expect(panel.textContent).toContain("too old");
    // The version is the actionable part — without it the message is untriageable.
    expect(panel.textContent).toContain("v0.4.2");
    // Forge's own complaint is carried verbatim.
    expect(panel.textContent).toContain("unknown flag: --json");
    // It explicitly denies the "you have no environments" reading.
    expect(panel.textContent).toContain("have not gone away");
    expect(screen.queryByTestId("forge-topology")).toBeNull();
  });
});

describe("reachability: UNREACHABLE with no report", () => {
  it("renders unknown — neither a red banner nor a green screen", () => {
    const outcome = classifyForgeResponse<ForgeTopologyReport>(
      meta({
        reachability: ForgeReachability.UNREACHABLE,
        exitCode: 2,
        unreachableReason: "kubectl: dial tcp: i/o timeout",
      }),
      ""
    );
    expect(outcome.kind).toBe("unreachable");

    renderOutcome(outcome);
    const panel = screen.getByTestId("forge-unreachable");

    expect(panel.textContent).toContain("unknown");
    expect(panel.textContent).toContain("kubectl: dial tcp: i/o timeout");
    // Explicitly not a release verdict — this is the sentence that keeps the
    // check from being switched off after the first VPN blip.
    expect(panel.textContent).toContain("not");
    expect(panel.textContent).toContain("evidence of a problem");
    // Uses the unknown treatment, not the error one.
    expect(panel.className).toContain("border-dashed");
    expect(panel.className).not.toMatch(/destructive/);
  });
});

describe("malformed or empty report_json", () => {
  it("does not crash the view when report_json is not JSON", () => {
    const outcome = classifyForgeResponse<ForgeTopologyReport>(meta(), "this is not json {{{");
    expect(outcome.kind).toBe("malformed");
    renderOutcome(outcome);
    expect(screen.getByTestId("forge-malformed")).toBeTruthy();
  });

  it("does not crash when report_json is empty but the project IS a forge project", () => {
    const outcome = classifyForgeResponse<ForgeTopologyReport>(meta(), "");
    expect(outcome.kind).toBe("malformed");
    renderOutcome(outcome);
    expect(screen.getByTestId("forge-malformed")).toBeTruthy();
  });

  it("does not crash on a JSON document of the wrong kind", () => {
    // A bare array or scalar is parseable but is not a report.
    for (const body of ["[]", '"a string"', "42", "null"]) {
      const outcome = classifyForgeResponse<ForgeTopologyReport>(meta(), body);
      expect(outcome.kind, `body ${body} should classify as malformed`).toBe("malformed");
    }
  });

  it("renders a report whose fields are all missing without throwing", () => {
    // A valid-but-empty object: no environments, no images, no tally. This is
    // what a much older or much newer forge could plausibly return, and it must
    // produce a screen rather than an exception.
    const outcome = classifyForgeResponse<ForgeTopologyReport>(meta(), "{}");
    expect(outcome.kind).toBe("report");
    renderOutcome(outcome);
    expect(screen.getByTestId("forge-topology")).toBeTruthy();
    expect(screen.getByTestId("forge-topology-no-envs")).toBeTruthy();
  });

  it("renders environments whose image arrays are missing entirely", () => {
    const outcome: ForgeOutcome<ForgeTopologyReport> = {
      kind: "report",
      meta: meta(),
      report: {
        images: ["control-plane"],
        environments: [{ env: "prod", declared: true, bound: true, release: "v1.0.0" }],
      },
    };
    renderOutcome(outcome);
    // The cell exists and is rendered as absent, not as a crash or a blank.
    expect(screen.getByTestId("cell-prod-control-plane").dataset.absent).toBe("true");
  });
});

describe("loading and transport failure", () => {
  it("shows a ledger-loading state before the first response", () => {
    renderOutcome(undefined, true);
    expect(screen.getByTestId("forge-topology-loading")).toBeTruthy();
  });

  it("shows a real error only for a genuine transport failure", () => {
    render(
      <TopologyView
        outcome={undefined}
        isLoading={false}
        error={new Error("daemon not connected")}
        onVerify={vi.fn()}
      />
    );
    const panel = screen.getByTestId("forge-topology-error");
    expect(panel.textContent).toContain("daemon not connected");
    // THIS is the only case that gets the destructive treatment.
    expect(panel.className).toMatch(/destructive/);
  });
});
