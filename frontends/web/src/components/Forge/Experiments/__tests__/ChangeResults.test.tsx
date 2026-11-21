// Copyright (c) 2025 Reliant Labs

/**
 * The results panel against a fake GetChangeReadout (protojson, through the
 * real ChangesApi): each verdict draws in its own tone and words, a failed
 * sample-ratio check hides the lift, every refusal has its own plain
 * sentence, and a control plane without the readout keeps the "ships soon"
 * state.
 */

import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";

import { renderWithQuery } from "@/test/renderWithQuery";
import { ChangesApiProvider } from "@/services/controlPlane/changes/ChangesApiContext";
import { createChangesApi } from "@/services/controlPlane/changes/changesApi";
import type { Change } from "@/services/controlPlane/changes/changesTypes";

import { ChangeResults } from "../ChangeResults";

const CHANGE = { id: "chg-1", currentRun: { id: "r_abcdefgh" } } as unknown as Change;

function wireReadout(overrides: Record<string, unknown> = {}) {
  return {
    changeId: "chg-1",
    key: "new-checkout",
    run: { id: "r_abcdefgh", kind: "experiment" },
    verdict: "CHANGE_READOUT_VERDICT_TREATMENT_BETTER",
    bestVariant: "treatment",
    arms: [
      { variant: "control", weightBp: 5000, assigned: "1000", windowComplete: "900", conversions: "90", rate: 0.1, ciLow: 0.082, ciHigh: 0.121, pending: "100", expectedAssigned: 1005 },
      { variant: "treatment", weightBp: 5000, assigned: "1010", windowComplete: "905", conversions: "113", rate: 0.125, ciLow: 0.105, ciHigh: 0.148, pending: "105", expectedAssigned: 1005 },
    ],
    comparisons: [
      { treatment: "treatment", control: "control", absLift: 0.025, relLift: 0.25, diffCiLow: 0.004, diffCiHigh: 0.046, pValue: 0.0123, alpha: 0.05, significant: true, verdict: "CHANGE_READOUT_VERDICT_TREATMENT_BETTER" },
    ],
    srm: { pValue: 0.82, chiSquare: 0.05 },
    assigned: "2010",
    pending: "205",
    readoutStart: "2026-11-01T00:00:00Z",
    readoutEnd: "2026-11-20T00:00:00Z",
    readoutEndReason: "now",
    metric: { event: "checkout_completed", windowHours: 168, minSamplePerArm: "100", controlVariant: "control" },
    alpha: 0.05,
    horizonNote: "Decide once, at the pre-registered sample size, not at the first significant look.",
    ...overrides,
  };
}

function renderResults(answer: () => unknown) {
  const call = vi.fn(async (method: string) => {
    if (method !== "GetChangeReadout") throw new ConnectError(method, Code.Unimplemented);
    return answer();
  });
  renderWithQuery(
    <ChangesApiProvider api={createChangesApi(call, async () => "org-1")}>
      <ChangeResults change={CHANGE} />
    </ChangesApiProvider>
  );
  return call;
}

const refusal = (reason: string, code: Code) => () => {
  throw new ConnectError(reason, code, new Headers(reason ? { "x-forge-error-reason": reason } : {}));
};

describe("ChangeResults", () => {
  it("draws a better treatment in success words, with each version's counts, the lift and the window", async () => {
    const call = renderResults(() => wireReadout());
    const verdict = await screen.findByTestId("results-verdict");
    expect(verdict).toHaveAttribute("data-verdict", "treatment_better");
    const line = within(verdict).getByText(/The new version is better/);
    expect(line).toHaveClass("text-success-ink");
    expect(line).toHaveTextContent("(treatment)");
    expect(call).toHaveBeenCalledWith("GetChangeReadout", { changeId: "chg-1" }, expect.anything());

    const arm = screen.getByTestId("results-arm-treatment");
    expect(arm).toHaveTextContent("1,010");
    expect(arm).toHaveTextContent("905");
    expect(arm).toHaveTextContent("113");
    expect(arm).toHaveTextContent("12.5%");
    expect(arm).toHaveTextContent("105");
    expect(within(arm).getByRole("img")).toHaveAccessibleName("12.5%, likely between 10.5% and 14.8%");

    const lift = screen.getByTestId("results-comparisons");
    expect(lift).toHaveTextContent("+2.5 points (likely between +0.4 and +4.6), +25.0% relative");
    expect(lift).toHaveTextContent("p = 0.012");
    expect(screen.getByTestId("results-window")).toHaveTextContent("to now");
    expect(screen.getByTestId("results-window")).toHaveTextContent("Still collecting.");
    expect(screen.getByTestId("results-horizon")).toHaveTextContent("Decide once");
    expect(screen.getByText(/95% confidence/)).toBeInTheDocument();
  });

  it("draws a worse treatment in the danger tone", async () => {
    renderResults(() => wireReadout({ verdict: "CHANGE_READOUT_VERDICT_TREATMENT_WORSE", bestVariant: undefined }));
    const verdict = await screen.findByTestId("results-verdict");
    expect(within(verdict).getByText("The new version is worse")).toHaveClass("text-danger-ink");
  });

  it.each([
    ["CHANGE_READOUT_VERDICT_INCONCLUSIVE", "Not enough evidence yet"],
    ["CHANGE_READOUT_VERDICT_NO_DATA", "No one has been counted yet"],
  ])("keeps %s neutral", async (wire, label) => {
    renderResults(() => wireReadout({ verdict: wire, reason: "below_min_sample", bestVariant: undefined }));
    const verdict = await screen.findByTestId("results-verdict");
    const line = within(verdict).getByText(label);
    expect(line).toHaveClass("text-ink");
    expect(line.className).not.toMatch(/success|danger|warning/);
    if (wire.endsWith("INCONCLUSIVE")) expect(verdict).toHaveTextContent("fewer people than the minimum");
  });

  it("warns that a failed sample-ratio check makes the results untrustworthy, and shows no lift", async () => {
    renderResults(() =>
      wireReadout({
        verdict: "CHANGE_READOUT_VERDICT_SRM_FAILED",
        bestVariant: undefined,
        srm: { pValue: 0.0001, chiSquare: 15.1, failed: true, unplannedVariants: ["beta"] },
        conflicted: "3",
        truncatedByRetention: true,
        final: true,
        readoutEndReason: "run_ended",
      })
    );
    const verdict = await screen.findByTestId("results-verdict");
    expect(within(verdict).getByText("These results can't be trusted")).toHaveClass("text-warning-ink");
    expect(verdict).toHaveTextContent("Don't act on any difference");
    expect(verdict).toHaveTextContent("versions the split doesn't have: beta");
    expect(screen.queryByTestId("results-comparisons")).not.toBeInTheDocument();
    expect(screen.getByTestId("results-window")).toHaveTextContent("when the run ended");
    expect(screen.getByTestId("results-window")).toHaveTextContent("Final.");
    expect(screen.getByTestId("results-conflicted")).toHaveTextContent("3 people were shown more than one version");
    expect(screen.getByTestId("results-truncated")).toBeInTheDocument();
  });

  it("keeps the readout-service-ships state when the control plane has no readout", async () => {
    renderResults(refusal("", Code.Unimplemented));
    expect(await screen.findByTestId("results-not-deployed")).toHaveTextContent("Results appear once the readout service ships");
  });

  it.each([
    ["no_experiment_run", Code.FailedPrecondition, "results-no-experiment", "once you launch an experiment"],
    ["run_not_experiment", Code.FailedPrecondition, "results-not-experiment", "This run is a rollout"],
    ["metric_missing", Code.FailedPrecondition, "results-no-metric", "without a success metric"],
    ["readout_plan_invalid", Code.FailedPrecondition, "results-plan-invalid", "doesn't have a control and a new version"],
    ["scope_missing", Code.PermissionDenied, "results-denied", "obs:read"],
    ["", Code.Unavailable, "results-no-observability", "observability"],
  ])("says what %s means", async (reason, code, testId, words) => {
    renderResults(refusal(reason, code));
    expect(await screen.findByTestId(testId)).toHaveTextContent(words);
  });
});
