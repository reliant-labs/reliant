// Copyright (c) 2025 Reliant Labs

/**
 * THE THREE-OUTCOME RULE, asserted directly.
 *
 * timed_out and not_waited are the ABSENCE of an answer. They must be
 * distinguishable from ready AND from failed, along an axis that survives a
 * greyscale screenshot — which is why the assertion is on the shared certainty
 * grouping and on the dashed/unfilled container, not on a colour.
 *
 * An unrecognised state — a newer forge reporting something this build has never
 * heard of — must land on unknown and never on ready. Forge's own decoder refuses
 * such a value outright; a renderer cannot refuse, so it falls to unknown.
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import { RolloutResults } from "../RolloutResults";
import { prodPlan } from "./fixtures";

function withResults(results: Array<{ kind: string; name: string; state: string; detail?: string }>) {
  return prodPlan({
    mode: "apply",
    rollout: {
      mode: "wait",
      timeout_seconds: 300,
      results,
      ready: 0,
      failed: 0,
      timed_out: 0,
      not_waited: 0,
    },
  });
}

describe("the four rollout states", () => {
  it("renders timed_out and not_waited as UNKNOWN, distinct from both ready and failed", () => {
    render(
      <RolloutResults
        plan={withResults([
          { kind: "Deployment", name: "ready-one", state: "ready" },
          { kind: "Deployment", name: "failed-one", state: "failed", detail: "CrashLoopBackOff" },
          { kind: "Deployment", name: "timed-one", state: "timed_out" },
          { kind: "Deployment", name: "notwaited-one", state: "not_waited" },
        ])}
      />
    );

    const certainty = (name: string) =>
      screen.getByTestId(`deploy-rollout-${name}`).getAttribute("data-certainty");

    expect(certainty("ready-one")).toBe("known-good");
    expect(certainty("failed-one")).toBe("known-bad");
    // The two that matter: neither borrows a certain treatment.
    expect(certainty("timed-one")).toBe("unknown");
    expect(certainty("notwaited-one")).toBe("unknown");

    // And the unknown pair carries the DASHED, unfilled container — the axis that
    // survives greyscale and a high-contrast theme.
    for (const name of ["timed-one", "notwaited-one"]) {
      const row = screen.getByTestId(`deploy-rollout-${name}`);
      expect(row.className).toContain("border-dashed");
      expect(row.className).toContain("bg-transparent");
    }
    // While the certain pair is filled with a continuous border.
    for (const name of ["ready-one", "failed-one"]) {
      const row = screen.getByTestId(`deploy-rollout-${name}`);
      expect(row.className).toContain("border-solid");
      expect(row.className).not.toContain("border-dashed");
    }
  });

  it("gives timed_out and not_waited different words, because the next action differs", () => {
    render(
      <RolloutResults
        plan={withResults([
          { kind: "Deployment", name: "timed-one", state: "timed_out" },
          { kind: "Deployment", name: "notwaited-one", state: "not_waited" },
        ])}
      />
    );
    const timed = screen.getByTestId("deploy-rollout-timed-one").textContent ?? "";
    const notWaited = screen.getByTestId("deploy-rollout-notwaited-one").textContent ?? "";
    expect(timed).toMatch(/timed out/i);
    expect(notWaited).toMatch(/not waited on/i);
    expect(timed).not.toBe(notWaited);
  });

  it("falls an UNRECOGNISED state to unknown, never to ready", () => {
    render(
      <RolloutResults
        plan={withResults([
          { kind: "Deployment", name: "future-one", state: "converging_slowly" },
        ])}
      />
    );
    const row = screen.getByTestId("deploy-rollout-future-one");
    expect(row.getAttribute("data-state")).toBe("unknown");
    expect(row.getAttribute("data-certainty")).toBe("unknown");
    expect(row.textContent).not.toMatch(/\bReady\b/);
  });

  it("counts the three unknown kinds separately from ready and failed", () => {
    render(
      <RolloutResults
        plan={withResults([
          { kind: "Deployment", name: "a", state: "ready" },
          { kind: "Deployment", name: "b", state: "failed" },
          { kind: "Deployment", name: "c", state: "timed_out" },
          { kind: "Deployment", name: "d", state: "not_waited" },
          { kind: "Deployment", name: "e", state: "who_knows" },
        ])}
      />
    );
    const tally = screen.getByTestId("deploy-rollout-tally").textContent ?? "";
    expect(tally).toMatch(/1 ready/);
    expect(tally).toMatch(/1 failed/);
    expect(tally).toMatch(/1 timed out/);
    expect(tally).toMatch(/1 not waited on/);
    expect(tally).toMatch(/1 not known/);
  });

  it("says nothing was observed rather than nothing was wrong, for an empty skip", () => {
    render(
      <RolloutResults
        plan={prodPlan({
          rollout: {
            mode: "skip",
            timeout_seconds: 0,
            results: [],
            ready: 0,
            failed: 0,
            timed_out: 0,
            not_waited: 0,
          },
        })}
      />
    );
    expect(screen.getByTestId("deploy-rollout-empty").textContent).toMatch(
      /not to wait|no resource was observed/i
    );
  });
});
