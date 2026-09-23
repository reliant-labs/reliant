// Copyright (c) 2025 Reliant Labs

/**
 * The audit data layer's contract — the two rules, pinned directly.
 *
 * `isSevere` gating on `error` only, and `isAdvisory` reading forge's own flag,
 * are the two functions a well-meaning change is most likely to widen. A failure
 * here names which one moved.
 */

import { describe, expect, it } from "vitest";

import {
  auditCategories,
  auditStatusLabel,
  isAdvisory,
  isSevere,
  severityOf,
  severityTally,
  verdictLabel,
  verdictOf,
  verdictSentence,
  type ForgeAuditReport,
} from "../audit";

describe("severityOf", () => {
  it("maps the three forge statuses to distinct severities", () => {
    expect(severityOf("ok")).toBe("clean");
    expect(severityOf("warn")).toBe("notice");
    expect(severityOf("error")).toBe("problem");
  });

  it("keeps warn and error apart", () => {
    // If these collapse, the strip is red on a healthy project.
    expect(severityOf("warn")).not.toBe(severityOf("error"));
  });

  it("falls an unrecognised or absent status to unreadable, never to clean", () => {
    for (const status of ["critical", "OK", "", undefined]) {
      expect(severityOf(status), `status ${String(status)}`).toBe("unreadable");
    }
  });
});

describe("isSevere", () => {
  it("is true for error ONLY", () => {
    expect(isSevere("error")).toBe(true);
    // The rule. control-plane's real audit is overall_status "warn" on a healthy,
    // shipping project; a severe warn makes the strip permanently red.
    expect(isSevere("warn")).toBe(false);
    expect(isSevere("ok")).toBe(false);
    expect(isSevere(undefined)).toBe(false);
    expect(isSevere("from-the-future")).toBe(false);
  });
});

describe("isAdvisory", () => {
  it("reads forge's own advisory flag rather than a hardcoded category name", () => {
    expect(isAdvisory({ status: "ok", details: { advisory: true } })).toBe(true);
    expect(isAdvisory({ status: "ok", details: { advisory: false } })).toBe(false);
    expect(isAdvisory({ status: "ok", details: {} })).toBe(false);
    expect(isAdvisory({ status: "ok" })).toBe(false);
    expect(isAdvisory(undefined)).toBe(false);
  });

  it("is not fooled by a truthy non-boolean", () => {
    expect(isAdvisory({ details: { advisory: "yes" } as unknown as never })).toBe(false);
  });
});

/** control-plane's real shape: warn-only overall, plus the advisory file_sizes. */
function realWarnOnlyReport(): ForgeAuditReport {
  return {
    project_name: "control-plane",
    project_kind: "service",
    overall_status: "warn",
    categories: {
      version: { status: "ok", summary: "pinned" },
      config_deps: { status: "warn", summary: "2 keys read outside a provider" },
      migration_safety: { status: "warn", summary: "1 migration drops a column" },
      file_sizes: {
        status: "ok",
        summary: "3 oversized file(s) — advisory: non-gating",
        details: { advisory: true, oversized_files: [{ path: "up.go", lines: 3500 }] },
      },
    },
  };
}

describe("auditCategories", () => {
  it("iterates the document's keys rather than a hardcoded list", () => {
    const report = realWarnOnlyReport();
    report.categories!.brand_new_category = { status: "ok", summary: "additive" };
    const keys = auditCategories(report).map((row) => row.key);
    expect(keys).toContain("brand_new_category");
  });

  it("orders known categories by forge's print order and appends unknown ones", () => {
    const rows = auditCategories({
      categories: {
        zzz_unknown: { status: "ok" },
        file_sizes: { status: "ok" },
        version: { status: "ok" },
        aaa_unknown: { status: "ok" },
      },
    });
    // version before file_sizes (forge's order), then the two unknowns
    // alphabetically at the end.
    expect(rows.map((r) => r.key)).toEqual([
      "version",
      "file_sizes",
      "aaa_unknown",
      "zzz_unknown",
    ]);
  });

  it("humanizes the label while keeping the exact key for precision", () => {
    const row = auditCategories({ categories: { migration_safety: { status: "ok" } } })[0];
    expect(row.key).toBe("migration_safety");
    expect(row.label).toBe("Migration safety");
  });

  it("marks the advisory category as advisory and leaves its severity clean", () => {
    const row = auditCategories(realWarnOnlyReport()).find((r) => r.key === "file_sizes")!;
    expect(row.advisory).toBe(true);
    expect(row.severity).toBe("clean");
    expect(row.status).toBe("ok");
  });

  it("survives a malformed entry by yielding an unreadable row, not by throwing", () => {
    const rows = auditCategories({
      categories: {
        broken: "not an object" as unknown as never,
        alsoBroken: null as unknown as never,
      },
    });
    expect(rows).toHaveLength(2);
    for (const row of rows) expect(row.severity).toBe("unreadable");
  });

  it("tolerates a missing or malformed categories map", () => {
    expect(auditCategories(undefined)).toEqual([]);
    expect(auditCategories({})).toEqual([]);
    expect(auditCategories({ categories: "nope" as unknown as never })).toEqual([]);
  });
});

describe("severityTally", () => {
  it("counts the real warn-only report as two notices and no problems", () => {
    expect(severityTally(realWarnOnlyReport())).toEqual({
      clean: 2,
      notice: 2,
      problem: 0,
      unreadable: 0,
    });
  });
});

describe("verdictOf", () => {
  it("is notice — NOT problem — for the real warn-only project", () => {
    expect(verdictOf(realWarnOnlyReport())).toBe("notice");
  });

  it("is problem only when a category reports error", () => {
    const report = realWarnOnlyReport();
    report.categories!.codegen = { status: "error", summary: "stale" };
    expect(verdictOf(report)).toBe("problem");
  });

  it("ranks unreadable above notice, so a gap is not reported as mere warnings", () => {
    const report = realWarnOnlyReport();
    report.categories!.future = { status: "catastrophe" };
    expect(verdictOf(report)).toBe("unreadable");
  });

  it("is clean when every category is ok, including an advisory one with findings", () => {
    expect(
      verdictOf({
        categories: {
          version: { status: "ok" },
          file_sizes: {
            status: "ok",
            details: { advisory: true, oversized_files: [{ path: "a.go", lines: 9000 }] },
          },
        },
      })
    ).toBe("clean");
  });

  it("is no-categories — not clean — when nothing was assessed", () => {
    expect(verdictOf({})).toBe("no-categories");
    expect(verdictOf({ categories: {} })).toBe("no-categories");
    expect(verdictOf(undefined)).toBe("no-categories");
  });

  it("is derived from the rows, so it can disagree with forge's own roll-up on an unread status", () => {
    // Forge's rollupStatus has no arm for an unknown value, so it would call
    // this "ok". Deriving locally keeps the header consistent with the list.
    const report: ForgeAuditReport = {
      overall_status: "ok",
      categories: { novel: { status: "catastrophe" } },
    };
    expect(report.overall_status).toBe("ok");
    expect(verdictOf(report)).toBe("unreadable");
  });
});

describe("verdict copy", () => {
  it("states outright that warnings are not errors", () => {
    expect(verdictSentence("notice")).toContain("no errors");
    expect(verdictSentence("notice")).toContain("normal on a healthy project");
  });

  it("never labels a notice verdict with error language", () => {
    expect(verdictLabel("notice")).toBe("Warnings");
    expect(verdictLabel("problem")).toBe("Needs attention");
  });
});

describe("auditStatusLabel", () => {
  it("gives an unrecognised status its own word rather than an existing one", () => {
    expect(auditStatusLabel("catastrophe")).toBe("Unrecognised");
    expect(auditStatusLabel("ok")).toBe("OK");
  });
});
