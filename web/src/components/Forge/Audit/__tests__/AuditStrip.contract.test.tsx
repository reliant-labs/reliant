// Copyright (c) 2025 Reliant Labs

/**
 * THE VISUAL CONTRACT TESTS for the audit status strip.
 *
 * Two properties, both learned from real behaviour rather than invented:
 *
 * 1. A WARN-ONLY AUDIT IS NOT AN ERROR STATE. control-plane's real audit today is
 *    overall_status "warn", with config_deps and migration_safety warning on a
 *    project that is healthy and shipping. A strip that goes red on that is red
 *    permanently, and a permanently-red indicator trains people to ignore it.
 *
 * 2. `file_sizes` IS ADVISORY. Forge holds its status at "ok" even while listing
 *    oversized files, and stamps `details.advisory: true`. Rendering its findings
 *    as a failure invents a defect forge explicitly declined to report — and
 *    since it never gates overall_status, it would also put the strip in
 *    disagreement with its own header.
 *
 * Plus the additive-contract guard: an unrecognised category or status from a
 * newer forge must render, must not crash, and must not read as healthy.
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { AuditStrip } from "../AuditStrip";
import { classifyForgeResponse, type ForgeOutcome } from "@/services/forge/topology";
import type { ForgeAuditReport } from "@/services/forge/audit";
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

function reportOutcome(report: ForgeAuditReport): ForgeOutcome<ForgeAuditReport> {
  return { kind: "report", meta: meta(), report };
}

function renderStrip(outcome: ForgeOutcome<ForgeAuditReport> | undefined, isLoading = false) {
  return render(
    <AuditStrip outcome={outcome} isLoading={isLoading} projectName="control-plane" />
  );
}

/**
 * control-plane's real audit shape: overall "warn", with the two categories that
 * actually warn today, a healthy majority, and the advisory file_sizes entry
 * carrying findings while holding status "ok".
 */
function realWarnOnlyReport(): ForgeAuditReport {
  return {
    project_name: "control-plane",
    project_kind: "service",
    binary_version: "v0.9.1",
    generated_at: "2025-06-01T12:00:00Z",
    overall_status: "warn",
    categories: {
      version: { status: "ok", summary: "forge pin matches CI" },
      shape: { status: "ok", summary: "service project, 4 services" },
      conventions: { status: "ok", summary: "no violations" },
      config_deps: { status: "warn", summary: "2 config keys read outside a provider" },
      migration_safety: { status: "warn", summary: "1 migration drops a column" },
      file_sizes: {
        status: "ok",
        summary:
          "3 oversized file(s) (>800 lines), 1 god-object type(s) (>25 methods) — advisory: splitting aids LLM navigation (non-gating)",
        details: {
          advisory: true,
          line_threshold: 800,
          oversized_files: [{ path: "internal/cli/up.go", lines: 3500 }],
        },
      },
    },
  };
}

async function expand() {
  await userEvent.click(screen.getByRole("button", { expanded: false }));
}

describe("a warn-only audit does NOT render as an error state", () => {
  it("gives the verdict the notice treatment, not the destructive one", () => {
    renderStrip(reportOutcome(realWarnOnlyReport()));

    const strip = screen.getByTestId("forge-audit-strip");
    expect(strip.dataset.verdict).toBe("notice");

    const verdict = screen.getByTestId("forge-audit-verdict");
    expect(verdict.dataset.severity).toBe("notice");
    // THE assertion this whole file exists for.
    expect(verdict.className).not.toMatch(/destructive/);
    // It is visible, in the warning hue — calm, not alarming, not invisible.
    expect(verdict.className).toMatch(/warning/);
  });

  it("says in words that warnings are not errors and are normal", () => {
    renderStrip(reportOutcome(realWarnOnlyReport()));
    const note = screen.getByTestId("forge-audit-verdict-note");
    expect(note.textContent).toContain("no errors");
    expect(note.textContent).toContain("normal on a healthy project");
  });

  it("counts errors and warnings separately rather than merging them into findings", () => {
    renderStrip(reportOutcome(realWarnOnlyReport()));
    // "0 errors" is a different statement from "2 findings", and the difference
    // is what keeps a healthy project from reading as broken.
    expect(screen.getByTestId("forge-audit-strip").textContent).toContain("0 errors");
    expect(screen.getByTestId("forge-audit-strip").textContent).toContain("2 warn");
  });

  it("reserves the destructive treatment for an actual error category", () => {
    const report = realWarnOnlyReport();
    report.categories!.codegen = { status: "error", summary: "generated code is stale" };
    report.overall_status = "error";
    renderStrip(reportOutcome(report));

    const verdict = screen.getByTestId("forge-audit-verdict");
    expect(screen.getByTestId("forge-audit-strip").dataset.verdict).toBe("problem");
    expect(verdict.dataset.severity).toBe("problem");
    expect(verdict.className).toMatch(/destructive/);
  });

  it("does not render an individual warn category as a failure either", async () => {
    renderStrip(reportOutcome(realWarnOnlyReport()));
    await expand();

    const row = screen.getByTestId("forge-audit-category-config_deps");
    expect(row.dataset.severity).toBe("notice");
    const badge = row.querySelector("[data-severity-badge]") as HTMLElement;
    expect(badge.className).not.toMatch(/destructive/);
  });
});

describe("file_sizes is not rendered as a failure", () => {
  it("renders it as the OK it is, despite carrying findings", async () => {
    renderStrip(reportOutcome(realWarnOnlyReport()));
    await expand();

    const row = screen.getByTestId("forge-audit-category-file_sizes");
    expect(row.dataset.auditStatus).toBe("ok");
    expect(row.dataset.severity).toBe("clean");

    const badge = row.querySelector("[data-severity-badge]") as HTMLElement;
    // Neither red nor amber: forge did not report a problem, so neither do we.
    expect(badge.className).not.toMatch(/destructive/);
    expect(badge.className).not.toMatch(/warning/);
    expect(badge.className).toMatch(/success/);
  });

  it("labels it advisory so the listed findings are not read as defects", async () => {
    renderStrip(reportOutcome(realWarnOnlyReport()));
    await expand();

    const row = screen.getByTestId("forge-audit-category-file_sizes");
    expect(row.dataset.advisory).toBe("true");
    expect(row.textContent).toContain("advisory");
    expect(row.textContent).toContain("non-gating");
  });

  it("does not let file_sizes affect the verdict", () => {
    // A project whose ONLY category is file_sizes with a long findings list is
    // clean, because forge says it is clean.
    const outcome = reportOutcome({
      project_name: "control-plane",
      overall_status: "ok",
      categories: {
        file_sizes: {
          status: "ok",
          summary: "12 oversized file(s) — advisory: non-gating",
          details: { advisory: true, oversized_files: [{ path: "a.go", lines: 9000 }] },
        },
      },
    });
    renderStrip(outcome);

    expect(screen.getByTestId("forge-audit-strip").dataset.verdict).toBe("clean");
    expect(screen.getByTestId("forge-audit-verdict").className).not.toMatch(/destructive|warning/);
  });
});

describe("an unrecognised category or status from a newer forge", () => {
  it("renders an unknown category key as itself, without crashing", async () => {
    const report = realWarnOnlyReport();
    report.categories!.quantum_entanglement = {
      status: "ok",
      summary: "a category this build has never heard of",
    };
    renderStrip(reportOutcome(report));
    await expand();

    const row = screen.getByTestId("forge-audit-category-quantum_entanglement");
    expect(row.textContent).toContain("quantum_entanglement");
    expect(row.textContent).toContain("never heard of");
  });

  it("does not read an unrecognised STATUS as healthy", async () => {
    const outcome = reportOutcome({
      project_name: "control-plane",
      overall_status: "ok",
      categories: {
        version: { status: "ok", summary: "fine" },
        future_thing: { status: "catastrophe", summary: "a status from a newer forge" },
      },
    });
    renderStrip(outcome);

    // The verdict is NOT clean, even though forge's own roll-up said "ok" (its
    // switch has no arm for an unknown value, so it fell through to ok).
    expect(screen.getByTestId("forge-audit-strip").dataset.verdict).toBe("unreadable");
    const verdict = screen.getByTestId("forge-audit-verdict");
    expect(verdict.dataset.severity).toBe("unreadable");
    expect(verdict.className).not.toMatch(/success/);
    // And not an error either — forge did not report one.
    expect(verdict.className).not.toMatch(/destructive/);
    expect(verdict.className).toContain("border-dashed");

    await expand();
    expect(screen.getByTestId("forge-audit-category-future_thing").dataset.severity).toBe(
      "unreadable"
    );
    // Forge's own opinion is still shown verbatim, alongside ours.
    expect(screen.getByTestId("forge-audit-strip").textContent).toContain("roll-up");
  });

  it("does not crash on a malformed category entry", async () => {
    // A string where an object belongs — what a badly-versioned forge could emit.
    const outcome = reportOutcome({
      project_name: "control-plane",
      categories: {
        version: { status: "ok", summary: "fine" },
        broken: "not an object" as unknown as never,
      },
    });
    renderStrip(outcome);
    expect(screen.getByTestId("forge-audit-strip").dataset.verdict).toBe("unreadable");
    await expand();
    expect(screen.getByTestId("forge-audit-category-broken").dataset.severity).toBe("unreadable");
  });

  it("renders a report whose fields are all missing without throwing", () => {
    const outcome = classifyForgeResponse<ForgeAuditReport>(meta(), "{}");
    expect(outcome.kind).toBe("report");
    renderStrip(outcome);
    // No categories is not health: nothing was assessed.
    expect(screen.getByTestId("forge-audit-strip").dataset.verdict).toBe("no-categories");
    expect(screen.getByTestId("forge-audit-verdict").className).not.toMatch(/success/);
  });
});

describe("the strip stays compact", () => {
  it("renders no category rows until asked", () => {
    renderStrip(reportOutcome(realWarnOnlyReport()));
    // Per-project health is context, not a competitor for the topology view's
    // attention — and the real document is ~58KB.
    expect(screen.queryByTestId("forge-audit-categories")).toBeNull();
  });

  it("renders them on expand", async () => {
    renderStrip(reportOutcome(realWarnOnlyReport()));
    await expand();
    expect(screen.getByTestId("forge-audit-categories")).toBeTruthy();
  });
});

describe("the non-report outcomes", () => {
  it("renders is_forge_project:false as a sensible non-error one-liner", () => {
    const outcome = classifyForgeResponse<ForgeAuditReport>(meta({ isForgeProject: false }), "");
    expect(outcome.kind).toBe("not-forge-project");

    renderStrip(outcome);
    const panel = screen.getByTestId("forge-audit-not-project");
    expect(panel.textContent).toContain("forge.yaml");
    expect(panel.textContent).toContain("expected");
    expect(panel.className).not.toMatch(/destructive/);
    expect(screen.queryByTestId("forge-audit-strip")).toBeNull();
  });

  it("renders supported:false as a non-error state that names the version", () => {
    const outcome = classifyForgeResponse<ForgeAuditReport>(
      meta({ supported: false, forgeVersion: "v0.4.2", unsupportedReason: "unknown command" }),
      ""
    );
    expect(outcome.kind).toBe("unsupported");

    renderStrip(outcome);
    const panel = screen.getByTestId("forge-audit-unsupported");
    expect(panel.textContent).toContain("v0.4.2");
    expect(panel.textContent).toContain("unknown command");
    expect(panel.textContent).toContain("has not changed");
    expect(panel.className).not.toMatch(/destructive/);
  });

  it("renders UNREACHABLE as unknown, not as an error", () => {
    const outcome = classifyForgeResponse<ForgeAuditReport>(
      meta({ reachability: ForgeReachability.UNREACHABLE, exitCode: 2, unreachableReason: "timeout" }),
      ""
    );
    expect(outcome.kind).toBe("unreachable");

    renderStrip(outcome);
    const panel = screen.getByTestId("forge-audit-unreachable");
    expect(panel.textContent).toContain("unknown");
    expect(panel.textContent).toContain("not evidence of a problem");
    expect(panel.className).toContain("border-dashed");
    expect(panel.className).not.toMatch(/destructive/);
  });

  it("does not crash on a malformed body", () => {
    const outcome = classifyForgeResponse<ForgeAuditReport>(meta(), "}}} not json");
    expect(outcome.kind).toBe("malformed");
    renderStrip(outcome);
    expect(screen.getByTestId("forge-audit-malformed")).toBeTruthy();
  });

  it("shows a real error only for a genuine transport failure", () => {
    render(<AuditStrip outcome={undefined} isLoading={false} error={new Error("no daemon")} />);
    const panel = screen.getByTestId("forge-audit-error");
    expect(panel.textContent).toContain("no daemon");
    expect(panel.className).toMatch(/destructive/);
  });
});
