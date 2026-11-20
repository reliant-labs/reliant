// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { forgeConsoleVulnerabilitiesSearchSchema } from "@/routeSchemas";
import type { EnvVulnSummary, ScanState } from "@/services/controlPlane/vulnerabilities/types";

import { activeFilterCount, filtersOf, fixLabel, formatEpss, pickVulnSearch, sbomSourceLabel, scopeTrust, untrustedEnvs } from "../vulnModel";

const env = (name: string, scanState: ScanState, overrides: Partial<EnvVulnSummary> = {}): EnvVulnSummary => ({
  environmentId: `env-${name}`,
  environmentName: name,
  releaseId: scanState === "none" ? "" : `rel-${name}`,
  releaseVersion: scanState === "none" ? "" : "v1",
  counts: { critical: 0, high: 0, medium: 0, low: 0, negligible: 0, unknown: 0 },
  kev: 0,
  fixable: 0,
  blocking: 0,
  exempt: 0,
  coverage: scanState === "scanned" || scanState === "stale" ? { artifacts: 2, withSbom: 2, scanned: 2 } : { artifacts: 2, withSbom: 0, scanned: 0 },
  lastScannedMs: scanState === "scanned" || scanState === "stale" ? 1 : null,
  scanState,
  failingSinceMs: null,
  ...overrides,
});

describe("scope trust: unknown is never clean", () => {
  it("is not scanned yet with nothing deployed, nothing with an SBOM, or every scan pending", () => {
    expect(scopeTrust([])).toBe("not-scanned-yet");
    expect(scopeTrust([env("dev", "none")])).toBe("not-scanned-yet");
    expect(scopeTrust([env("prod", "no-sbom"), env("dev", "none")])).toBe("not-scanned-yet");
    expect(scopeTrust([env("prod", "pending")])).toBe("not-scanned-yet");
    // A failed scan with no successful one before it has nothing to show either.
    expect(scopeTrust([env("prod", "failed")])).toBe("not-scanned-yet");
  });

  it("is trusted only when every deployed environment is scanned", () => {
    expect(scopeTrust([env("prod", "scanned"), env("dev", "none")])).toBe("trusted");
    expect(scopeTrust([env("prod", "scanned"), env("staging", "stale")])).toBe("partial");
    expect(scopeTrust([env("prod", "scanned"), env("staging", "pending")])).toBe("partial");
    expect(scopeTrust([env("prod", "failed", { lastScannedMs: 5 })])).toBe("partial");
    expect(scopeTrust([env("prod", "no-sbom", { coverage: { artifacts: 3, withSbom: 2, scanned: 2 } })])).toBe("partial");
    expect(untrustedEnvs([env("prod", "scanned"), env("staging", "stale"), env("dev", "none")]).map((e) => e.environmentName)).toEqual(["staging"]);
  });
});

describe("the URL's filters", () => {
  it("map one to one onto the request's optional fields", () => {
    expect(filtersOf(undefined, {})).toEqual({});
    expect(filtersOf("env-prod", { release: "v1", severity: "high", fixable: "no", kev: "1", exempt: "1" })).toEqual({
      environmentId: "env-prod",
      releaseVersion: "v1",
      minSeverity: "high",
      fixable: false,
      kev: true,
      includeExempt: true,
    });
  });

  it("read a value that does not fit as absent, in the page and in the route", () => {
    expect(pickVulnSearch({ severity: "urgent", fixable: "maybe", kev: "0", release: "" })).toEqual({});
    expect(pickVulnSearch({ severity: "critical", kev: 1, exempt: "1" })).toEqual({ severity: "critical", kev: "1", exempt: "1" });
    expect(forgeConsoleVulnerabilitiesSearchSchema.parse({ severity: "urgent", fixable: "maybe", kev: 1, release: 2026 })).toEqual({ kev: "1", release: "2026" });
    expect(activeFilterCount({ release: "v1", kev: "1" })).toBe(2);
  });
});

describe("words", () => {
  it("labels a lockfile SBOM, a fix and an EPSS", () => {
    expect(sbomSourceLabel("lockfile")).toBe("from lockfile");
    expect(sbomSourceLabel("image")).toBe("from image");
    expect(fixLabel("fixed", ["1.2.3", "2.0.1"])).toBe("fixed in 1.2.3, 2.0.1");
    expect(fixLabel("wont-fix", [])).toBe("won't be fixed");
    expect(formatEpss(0.004)).toBe("<1%");
    expect(formatEpss(0.94)).toBe("94%");
  });
});
