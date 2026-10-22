// Copyright (c) 2025 Reliant Labs

/**
 * The env-status data layer's contract.
 *
 * These pin the mapping directly, so a regression is caught here — with a one-line
 * failure naming the status — rather than only in a rendered table.
 */

import { describe, expect, it } from "vitest";

import {
  checkStatusExplanation,
  checkStatusLabel,
  checksOf,
  dispositionOf,
  dispositionTally,
  formatCheckDuration,
  verdictOf,
  verdictSentence,
  wasMeasured,
  type ForgeEnvStatusReport,
} from "../status";

describe("dispositionOf", () => {
  it("maps each of the five statuses to its OWN disposition", () => {
    expect(dispositionOf("pass")).toBe("measured-good");
    expect(dispositionOf("fail")).toBe("measured-bad");
    expect(dispositionOf("warn")).toBe("measured-degraded");
    expect(dispositionOf("skip")).toBe("not-applicable");
    expect(dispositionOf("unknown")).toBe("undetermined");
  });

  it("keeps skip and unknown apart", () => {
    // Forge's doctor package says these must never render the same. They cannot
    // render the same if they do not classify the same.
    expect(dispositionOf("skip")).not.toBe(dispositionOf("unknown"));
  });

  it("falls an unrecognised or absent status to undetermined, never to measured-good", () => {
    for (const status of ["indeterminate", "PASS", "ok", "", undefined]) {
      expect(dispositionOf(status), `status ${String(status)}`).toBe("undetermined");
    }
  });
});

describe("wasMeasured", () => {
  it("is true only for the three statuses that involved an actual measurement", () => {
    expect(wasMeasured("pass")).toBe(true);
    expect(wasMeasured("warn")).toBe(true);
    expect(wasMeasured("fail")).toBe(true);
    expect(wasMeasured("skip")).toBe(false);
    expect(wasMeasured("unknown")).toBe(false);
  });
});

describe("labels and explanations", () => {
  it("never gives two statuses the same label", () => {
    const labels = ["pass", "fail", "warn", "skip", "unknown"].map(checkStatusLabel);
    expect(new Set(labels).size).toBe(labels.length);
  });

  it("words skip as a conclusion and unknown as the absence of one", () => {
    expect(checkStatusLabel("skip")).toBe("Not applicable");
    expect(checkStatusLabel("unknown")).toBe("Could not measure");
  });

  it("explains unknown as neither a pass nor a skip", () => {
    expect(checkStatusExplanation("unknown")).toContain("neither a pass nor a skip");
  });

  it("explains skip as a finished answer with nothing missing", () => {
    expect(checkStatusExplanation("skip")).toContain("Nothing is missing");
  });
});

describe("checksOf", () => {
  it("filters NOTHING out", () => {
    const report: ForgeEnvStatusReport = {
      checks: [
        { name: "Compose Infra", status: "pass" },
        { name: "Cluster Workloads", status: "fail" },
        { name: "Delve", status: "skip" },
        { name: "App Health", status: "unknown" },
      ],
    };
    expect(checksOf(report)).toHaveLength(4);
    expect(checksOf(report).map((c) => c.name)).toEqual([
      "Compose Infra",
      "Cluster Workloads",
      "Delve",
      "App Health",
    ]);
  });

  it("labels a nameless check positionally rather than dropping it", () => {
    expect(checksOf({ checks: [{ status: "pass" }] })[0].name).toBe("Check 1");
  });

  it("tolerates a missing or malformed checks array", () => {
    expect(checksOf(undefined)).toEqual([]);
    expect(checksOf({})).toEqual([]);
    expect(checksOf({ checks: "nope" as unknown as never })).toEqual([]);
  });
});

describe("dispositionTally", () => {
  it("counts every disposition from the rendered rows", () => {
    const tally = dispositionTally({
      checks: [
        { name: "a", status: "pass" },
        { name: "b", status: "pass" },
        { name: "c", status: "warn" },
        { name: "d", status: "fail" },
        { name: "e", status: "skip" },
        { name: "f", status: "unknown" },
        { name: "g", status: "from-the-future" },
      ],
    });
    expect(tally).toEqual({
      "measured-good": 2,
      "measured-degraded": 1,
      "measured-bad": 1,
      "not-applicable": 1,
      undetermined: 2,
    });
  });
});

describe("verdictOf", () => {
  it("is all-good only when every check passed or legitimately did not apply", () => {
    expect(
      verdictOf({ checks: [{ name: "a", status: "pass" }, { name: "b", status: "skip" }] })
    ).toBe("all-good");
  });

  it("is incomplete — NOT all-good — when any check could not be measured", () => {
    // The whole point. A pass plus a hole is not a clean bill of health.
    expect(
      verdictOf({ checks: [{ name: "a", status: "pass" }, { name: "b", status: "unknown" }] })
    ).toBe("incomplete");
  });

  it("ranks incomplete above degraded, so holes are not reported as mere degradation", () => {
    expect(
      verdictOf({ checks: [{ name: "a", status: "warn" }, { name: "b", status: "unknown" }] })
    ).toBe("incomplete");
  });

  it("is failing when anything was measured and found wrong", () => {
    expect(
      verdictOf({ checks: [{ name: "a", status: "unknown" }, { name: "b", status: "fail" }] })
    ).toBe("failing");
  });

  it("is no-checks — not all-good — for a report with no checks at all", () => {
    expect(verdictOf({})).toBe("no-checks");
    expect(verdictOf({ checks: [] })).toBe("no-checks");
    expect(verdictOf(undefined)).toBe("no-checks");
  });

  it("cannot be all-good with an unrecognised status present", () => {
    expect(
      verdictOf({ checks: [{ name: "a", status: "pass" }, { name: "b", status: "novel" }] })
    ).toBe("incomplete");
  });
});

describe("verdictSentence", () => {
  it("denies the clean-bill-of-health reading for incomplete", () => {
    expect(verdictSentence("incomplete", "dev")).toContain("not a clean bill of health");
  });

  it("says no-checks means nothing was measured", () => {
    expect(verdictSentence("no-checks", "dev")).toContain("nothing here has been measured");
  });
});

describe("formatCheckDuration", () => {
  it("renders sub-second durations in ms and longer ones in seconds", () => {
    expect(formatCheckDuration(120)).toBe("120ms");
    expect(formatCheckDuration(1500)).toBe("1.5s");
  });

  it("renders nothing rather than a zero for a duration forge did not give", () => {
    expect(formatCheckDuration(undefined)).toBe("");
    expect(formatCheckDuration(Number.NaN)).toBe("");
    expect(formatCheckDuration(-1)).toBe("");
  });
});
