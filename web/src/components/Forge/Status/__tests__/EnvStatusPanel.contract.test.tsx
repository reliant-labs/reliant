// Copyright (c) 2025 Reliant Labs

/**
 * THE VISUAL CONTRACT TESTS for the env-monitoring panel.
 *
 * These pin the property the panel exists to guarantee: `unknown` is visually
 * distinct from BOTH `pass` and `fail`, and `skip` and `unknown` do not render
 * identically.
 *
 * Every assertion is on the RENDERED DISTINCTION, not merely on an attribute.
 * Two hooks are checked together, and either one alone can be satisfied by a
 * rendering that reintroduces the bug:
 *
 *   `data-disposition` — the semantic category. A regression that mapped
 *                        `unknown` onto measured-good would flip this.
 *   the class list     — the visible treatment. A regression that kept the
 *                        attribute honest while giving unknown the same solid
 *                        fill as pass would sail through an attribute-only test,
 *                        so the fill/border axis is asserted directly and the
 *                        treatments are compared AGAINST EACH OTHER rather than
 *                        against hardcoded strings.
 *
 * The history being guarded: `forge env status dev` reported all-green for an
 * hour while daemon-gateway was OOMKilled and CrashLoopBackOff, and for ten
 * hours while three more services crashlooped. Rendering "could not measure" as
 * green IS that bug.
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import { EnvStatusPanel } from "../EnvStatusPanel";
import { classifyForgeResponse, type ForgeOutcome } from "@/services/forge/topology";
import type { ForgeEnvStatusReport } from "@/services/forge/status";
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

function reportOutcome(report: ForgeEnvStatusReport): ForgeOutcome<ForgeEnvStatusReport> {
  return { kind: "report", meta: meta(), report };
}

function renderPanel(outcome: ForgeOutcome<ForgeEnvStatusReport> | undefined, isLoading = false) {
  return render(
    <EnvStatusPanel
      outcome={outcome}
      isLoading={isLoading}
      env="dev"
      projectName="control-plane"
    />
  );
}

/**
 * All five statuses at once, so the treatments can be compared against each
 * other in a single render. The names mirror forge's real check set.
 */
function allFiveReport(): ForgeEnvStatusReport {
  return {
    env: "dev",
    checks: [
      { name: "Compose Infra", status: "pass", message: "5 containers up", duration_ms: 120 },
      { name: "Cluster Workloads", status: "fail", message: "daemon-gateway CrashLoopBackOff" },
      { name: "Prometheus", status: "warn", message: "scrape lag 40s" },
      { name: "Delve", status: "skip", message: "not required for this project" },
      { name: "App Health", status: "unknown", message: "app port not discovered" },
    ],
  };
}

/** The badge element inside a check row — the thing that carries the treatment. */
function badgeFor(name: string): HTMLElement {
  const row = screen.getByTestId(`forge-check-${name}`);
  const badge = row.querySelector("[data-disposition-badge]");
  if (!(badge instanceof HTMLElement)) {
    throw new Error(`no disposition badge rendered for check ${name}`);
  }
  return badge;
}

describe("unknown renders distinctly from BOTH pass and fail", () => {
  it("maps unknown to its own disposition, not to a pass or a fail", () => {
    renderPanel(reportOutcome(allFiveReport()));

    expect(screen.getByTestId("forge-check-App Health").dataset.disposition).toBe("undetermined");
    expect(screen.getByTestId("forge-check-Compose Infra").dataset.disposition).toBe(
      "measured-good"
    );
    expect(screen.getByTestId("forge-check-Cluster Workloads").dataset.disposition).toBe(
      "measured-bad"
    );
  });

  it("gives unknown a VISIBLY different treatment from pass — unfilled and dashed, not solid", () => {
    renderPanel(reportOutcome(allFiveReport()));

    const unknown = badgeFor("App Health");
    const pass = badgeFor("Compose Infra");

    // The class lists differ at all — the floor, and not sufficient on its own.
    expect(unknown.className).not.toBe(pass.className);

    // The load-bearing axis: pass is SOLID and FILLED, unknown is DASHED and
    // has no fill. This survives greyscale and a colour-vision deficiency, which
    // hue alone does not.
    expect(pass.className).toContain("border-solid");
    expect(pass.className).toMatch(/bg-success/);
    expect(unknown.className).toContain("border-dashed");
    expect(unknown.className).toContain("bg-transparent");

    // And unknown must not borrow the success hue by any route.
    expect(unknown.className).not.toMatch(/success/);
  });

  it("gives unknown a VISIBLY different treatment from fail — not the destructive one", () => {
    renderPanel(reportOutcome(allFiveReport()));

    const unknown = badgeFor("App Health");
    const fail = badgeFor("Cluster Workloads");

    expect(unknown.className).not.toBe(fail.className);

    expect(fail.className).toContain("border-solid");
    expect(fail.className).toMatch(/bg-destructive/);
    expect(unknown.className).toContain("border-dashed");

    // A check that could not look must never be reported as a problem — that is
    // how a check earns itself being switched off in its first week.
    expect(unknown.className).not.toMatch(/destructive/);
  });

  it("says in words that unknown is neither a pass nor a skip", () => {
    renderPanel(reportOutcome(allFiveReport()));
    const row = screen.getByTestId("forge-check-App Health");
    expect(row.textContent).toContain("Could not measure");
    expect(row.textContent).toContain("neither a pass nor a skip");
  });

  it("keeps the header verdict off 'healthy' when any check is undetermined", () => {
    // No failures at all — just one hole in the report. A panel that called this
    // healthy would be the all-green screen that hid two outages.
    const outcome = reportOutcome({
      env: "dev",
      checks: [
        { name: "Compose Infra", status: "pass" },
        { name: "App Health", status: "unknown", message: "port not discovered" },
      ],
    });
    renderPanel(outcome);

    const verdict = screen.getByTestId("forge-status-verdict");
    expect(verdict.dataset.verdict).toBe("incomplete");
    expect(verdict.textContent).toContain("not a clean bill of health");
    // Not painted as success, and not painted as an error either.
    expect(verdict.className).not.toMatch(/success/);
    expect(verdict.className).not.toMatch(/destructive/);
    expect(verdict.className).toContain("border-dashed");
  });
});

describe("skip and unknown do not render identically", () => {
  it("classifies them as different dispositions", () => {
    renderPanel(reportOutcome(allFiveReport()));
    expect(screen.getByTestId("forge-check-Delve").dataset.disposition).toBe("not-applicable");
    expect(screen.getByTestId("forge-check-App Health").dataset.disposition).toBe("undetermined");
  });

  it("renders them with different treatments AND different labels", () => {
    renderPanel(reportOutcome(allFiveReport()));

    const skip = badgeFor("Delve");
    const unknown = badgeFor("App Health");

    // The whole class list differs — they share the dashed/unfilled half of the
    // vocabulary, so the distinction has to come from the other axes.
    expect(skip.className).not.toBe(unknown.className);

    // Opacity: a skip is genuinely less important and is dimmed. Undetermined is
    // full-opacity and RINGED, because it is the row a reader should stop on.
    expect(skip.className).toContain("opacity-70");
    expect(unknown.className).not.toContain("opacity-70");
    expect(unknown.className).toContain("ring-1");
    expect(skip.className).not.toContain("ring-1");

    // And the words differ: one is a conclusion, the other is the absence of one.
    expect(skip.textContent).toContain("Not applicable");
    expect(unknown.textContent).toContain("Could not measure");
    expect(skip.textContent).not.toBe(unknown.textContent);
  });

  it("explains each as what it is — a finished answer vs a hole in the report", () => {
    renderPanel(reportOutcome(allFiveReport()));
    expect(screen.getByTestId("forge-check-Delve").textContent).toContain("does not apply here");
    expect(screen.getByTestId("forge-check-App Health").textContent).toContain(
      "could not obtain a fact it needed"
    );
  });

  it("does not let a skip-only report drag the verdict off healthy", () => {
    // skip is a finished answer, so a project whose shape skips a check is
    // healthy. This is the converse guard to the undetermined test above: the
    // two must not be conflated in EITHER direction.
    renderPanel(
      reportOutcome({
        env: "dev",
        checks: [
          { name: "Compose Infra", status: "pass" },
          { name: "Delve", status: "skip" },
        ],
      })
    );
    expect(screen.getByTestId("forge-status-verdict").dataset.verdict).toBe("all-good");
  });
});

describe("warn is its own level, between measured-good and measured-bad", () => {
  it("does not render warn as a failure", () => {
    renderPanel(reportOutcome(allFiveReport()));
    const warn = badgeFor("Prometheus");
    expect(warn.className).not.toMatch(/destructive/);
    // Measured, so it keeps the solid/continuous treatment — it is certain.
    expect(warn.className).toContain("border-solid");
    expect(warn.className).not.toBe(badgeFor("Cluster Workloads").className);
  });
});

describe("no check is filtered out", () => {
  it("renders the cluster-workload check alongside the host-side app check", () => {
    // Forge puts Cluster Workloads in the `app` signal deliberately. A panel
    // that dropped it to look tidier is the report that was all-green while
    // four services crashlooped in the deployed namespace.
    renderPanel(reportOutcome(allFiveReport()));
    expect(screen.getByTestId("forge-check-Cluster Workloads")).toBeTruthy();
    expect(screen.getByTestId("forge-check-App Health")).toBeTruthy();
    expect(screen.getByTestId("forge-status-checks").children).toHaveLength(5);
  });

  it("renders a check with no name rather than dropping it", () => {
    renderPanel(reportOutcome({ env: "dev", checks: [{ status: "unknown" }] }));
    // Positionally labelled, still present, still undetermined.
    expect(screen.getByTestId("forge-check-Check 1").dataset.disposition).toBe("undetermined");
  });
});

describe("a status from a newer forge", () => {
  it("does not crash and does not read as healthy", () => {
    renderPanel(
      reportOutcome({
        env: "dev",
        checks: [
          { name: "Compose Infra", status: "pass" },
          { name: "Quantum Probe", status: "indeterminate-ish", message: "who knows" },
        ],
      })
    );

    const row = screen.getByTestId("forge-check-Quantum Probe");
    // An unread status falls to undetermined — the one direction that cannot
    // claim a pass.
    expect(row.dataset.disposition).toBe("undetermined");
    const badge = badgeFor("Quantum Probe");
    expect(badge.className).not.toMatch(/success/);
    expect(badge.className).toContain("border-dashed");
    // And it drags the verdict off healthy, because the report has a hole in it.
    expect(screen.getByTestId("forge-status-verdict").dataset.verdict).toBe("incomplete");
  });
});

describe("evidence is shown but never branched on", () => {
  it("renders evidence on demand without letting it change the status", () => {
    renderPanel(
      reportOutcome({
        env: "dev",
        checks: [
          {
            name: "App Health",
            status: "unknown",
            message: "port not discovered",
            // Prose that a naive implementation might scan for "ok" or "healthy"
            // and upgrade on. The status is the only input to the treatment.
            evidence: "GET /healthz -> 200 OK (stale cache, address unresolved)",
          },
        ],
      })
    );

    const row = screen.getByTestId("forge-check-App Health");
    expect(row.textContent).toContain("GET /healthz -> 200 OK");
    expect(row.dataset.disposition).toBe("undetermined");
    expect(badgeFor("App Health").className).not.toMatch(/success/);
  });
});

describe("the non-report outcomes", () => {
  it("renders is_forge_project:false as a sensible non-error state", () => {
    const outcome = classifyForgeResponse<ForgeEnvStatusReport>(
      meta({ isForgeProject: false }),
      ""
    );
    expect(outcome.kind).toBe("not-forge-project");

    renderPanel(outcome);
    const panel = screen.getByTestId("forge-not-project");
    expect(panel.textContent).toContain("forge.yaml");
    expect(panel.textContent).toContain("expected");
    expect(panel.className).not.toMatch(/destructive/);
    expect(screen.queryByTestId("forge-env-status")).toBeNull();
  });

  it("renders supported:false as a non-error state that names the version", () => {
    const outcome = classifyForgeResponse<ForgeEnvStatusReport>(
      meta({ supported: false, forgeVersion: "v0.4.2", unsupportedReason: "unknown flag: --json" }),
      ""
    );
    expect(outcome.kind).toBe("unsupported");

    renderPanel(outcome);
    const panel = screen.getByTestId("forge-unsupported");
    expect(panel.textContent).toContain("too old");
    expect(panel.textContent).toContain("v0.4.2");
    expect(panel.textContent).toContain("unknown flag: --json");
    expect(panel.className).not.toMatch(/destructive/);
  });

  it("renders UNREACHABLE as unknown — neither a red banner nor a green screen", () => {
    const outcome = classifyForgeResponse<ForgeEnvStatusReport>(
      meta({
        reachability: ForgeReachability.UNREACHABLE,
        exitCode: 2,
        unreachableReason: "kubectl: dial tcp: i/o timeout",
      }),
      ""
    );
    expect(outcome.kind).toBe("unreachable");

    renderPanel(outcome);
    const panel = screen.getByTestId("forge-unreachable");
    expect(panel.textContent).toContain("unknown");
    expect(panel.className).toContain("border-dashed");
    expect(panel.className).not.toMatch(/destructive/);
  });

  it("does not crash on a malformed report body", () => {
    const outcome = classifyForgeResponse<ForgeEnvStatusReport>(meta(), "not json {{{");
    expect(outcome.kind).toBe("malformed");
    renderPanel(outcome);
    expect(screen.getByTestId("forge-malformed")).toBeTruthy();
  });

  it("renders a report with no checks as 'nothing measured', not as healthy", () => {
    const outcome = classifyForgeResponse<ForgeEnvStatusReport>(meta(), "{}");
    expect(outcome.kind).toBe("report");
    renderPanel(outcome);

    const empty = screen.getByTestId("forge-status-no-checks");
    expect(empty.textContent).toContain("not a statement that the environment is healthy");
    const verdict = screen.getByTestId("forge-status-verdict");
    expect(verdict.dataset.verdict).toBe("no-checks");
    expect(verdict.className).not.toMatch(/success/);
  });

  it("shows a real error only for a genuine transport failure", () => {
    render(
      <EnvStatusPanel
        outcome={undefined}
        isLoading={false}
        error={new Error("daemon not connected")}
        env="dev"
      />
    );
    const panel = screen.getByTestId("forge-status-error");
    expect(panel.textContent).toContain("daemon not connected");
    // The ONLY case that gets the destructive treatment.
    expect(panel.className).toMatch(/destructive/);
  });

  it("shows a loading state before the first response", () => {
    renderPanel(undefined, true);
    expect(screen.getByTestId("forge-status-loading")).toBeTruthy();
  });
});
