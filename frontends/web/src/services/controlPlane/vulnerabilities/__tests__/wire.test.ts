// Copyright (c) 2025 Reliant Labs

/**
 * wire.ts is the only file that knows VulnerabilityService's wire shape: the
 * responses map to the plain types without ever turning "unknown" into
 * "clean", and the requests set only the optional fields that are set (an
 * absent proto3 optional is "no filter"; a present false is a filter).
 */

import { describe, expect, it } from "vitest";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";

import {
  VulnerabilityFixState as WireFixState,
  VulnerabilityScanState as WireScanState,
  VulnerabilitySeverity as WireSeverity,
} from "@/gen/controlplane/controlplane/v1/vulnerability_pb";
import {
  EnvironmentVulnerabilitySummarySchema,
  GetReleaseVulnerabilitiesResponseSchema,
  GetVulnerabilitySummaryResponseSchema,
  ListVulnerabilitiesResponseSchema,
} from "@/gen/controlplane/services/vulnerability/v1/vulnerability_pb";

import { findingPageOf, listRequest, releaseOf, scanStateOf, severityOf, summaryOf, summaryRequest } from "../wire";

type EnvInit = Exclude<MessageInitShape<typeof EnvironmentVulnerabilitySummarySchema>, { $typeName: string }>;

const env = (overrides: EnvInit = {}) =>
  create(EnvironmentVulnerabilitySummarySchema, {
    environmentId: "env-prod",
    environmentName: "prod",
    releaseId: "rel-1",
    releaseVersion: "v1.4.0",
    counts: { critical: 1, high: 2, medium: 0, low: 0, negligible: 0, unknown: 0 },
    kevCount: 1,
    fixableCount: 2,
    blockingCount: 1,
    exemptCount: 0,
    coverage: { artifacts: 3, withSbom: 3, scanned: 3 },
    lastScannedAt: timestampFromMs(Date.UTC(2026, 10, 20)),
    scanState: WireScanState.SCANNED,
    ...overrides,
  });

describe("vulnerability wire mapping", () => {
  it("maps a summary, with unset times as null and an environment without a release as nothing to judge", () => {
    const summary = summaryOf(
      create(GetVulnerabilitySummaryResponseSchema, {
        environments: [env(), env({ environmentId: "env-dev", environmentName: "dev", releaseId: "", releaseVersion: "", scanState: WireScanState.SCANNED, lastScannedAt: undefined })],
        database: { scanner: "grype", scannerVersion: "v0.122.1", schema: "v6", builtAt: undefined, stale: false },
      })
    );
    expect(summary.environments[0]).toMatchObject({ environmentName: "prod", kev: 1, fixable: 2, blocking: 1, scanState: "scanned", lastScannedMs: Date.UTC(2026, 10, 20) });
    expect(summary.environments[0]!.counts.high).toBe(2);
    expect(summary.environments[1]).toMatchObject({ scanState: "none", lastScannedMs: null });
    expect(summary.database).toMatchObject({ scanner: "grype", builtAtMs: null });
  });

  it("never reads an unknown scan state as scanned", () => {
    expect(scanStateOf(WireScanState.NO_SBOM)).toBe("no-sbom");
    expect(scanStateOf(WireScanState.PENDING)).toBe("pending");
    expect(scanStateOf(WireScanState.FAILED)).toBe("failed");
    expect(scanStateOf(WireScanState.STALE)).toBe("stale");
    // A state a newer server adds.
    expect(scanStateOf(99 as WireScanState)).toBe("pending");
    expect(severityOf(WireSeverity.UNSPECIFIED)).toBe("unknown");
    expect(severityOf(WireSeverity.CRITICAL)).toBe("critical");
  });

  it("reads an artifact with no SBOM digest as not scanned, whatever its state says", () => {
    const rel = releaseOf(
      create(GetReleaseVulnerabilitiesResponseSchema, {
        releaseId: "rel-1",
        releaseVersion: "v1.4.0",
        artifacts: [
          { artifactName: "api", sbomDigest: "", format: "", generator: "", source: "", referrer: "", componentCount: 0, scanState: WireScanState.SCANNED, scanError: "" },
          { artifactName: "web", sbomDigest: "sha256:ab", format: "cyclonedx-json@1.6", generator: "syft@v1.57.2", source: "lockfile", referrer: "", componentCount: 9, scanState: WireScanState.FAILED, scanError: "boom" },
        ],
        kevCount: 0,
        fixableCount: 0,
        blockingCount: 0,
        exemptCount: 0,
        findings: [],
        nextPageToken: "",
      })
    );
    expect(rel.artifacts.map((a) => a.scanState)).toEqual(["no-sbom", "failed"]);
    expect(rel.artifacts[1]).toMatchObject({ source: "lockfile", scanError: "boom" });
    expect(rel.coverage).toEqual({ artifacts: 0, withSbom: 0, scanned: 0 });
  });

  it("maps a finding, its exemption and its fix", () => {
    const page = findingPageOf(
      create(ListVulnerabilitiesResponseSchema, {
        findings: [
          {
            advisory: { id: "CVE-1", aliases: ["GHSA-1"], severity: WireSeverity.HIGH, cvssScore: 7.5, kev: true, epss: 0.2, summary: "s", references: [] },
            package: { name: "qs", version: "6.10.3", type: "npm", purl: "pkg:npm/qs@6.10.3", location: "" },
            fixedVersions: ["6.10.4"],
            fixState: WireFixState.FIXED,
            artifactNames: ["web"],
            environmentIds: ["env-prod"],
            exemption: { environmentId: "env-prod", advisoryId: "GHSA-1", reason: "not reachable", expiresAt: timestampFromMs(Date.UTC(2027, 0, 1)) },
            blocking: false,
          },
        ],
        nextPageToken: "t2",
      })
    );
    expect(page.nextPageToken).toBe("t2");
    expect(page.database).toBeNull();
    expect(page.findings[0]).toMatchObject({
      advisory: { id: "CVE-1", severity: "high", cvss: 7.5, kev: true, epss: 0.2 },
      fixState: "fixed",
      firstSeenMs: null,
      exemption: { advisoryId: "GHSA-1", expiresMs: Date.UTC(2027, 0, 1) },
    });
  });

  it("sets only the filters that are set, and a false one as a filter", () => {
    expect(listRequest({ orgId: null, project: "shop", filters: {} })).toEqual({ project: "shop", pageSize: 0, pageToken: "" });
    expect(
      listRequest({
        orgId: "org-1",
        project: "shop",
        filters: { environmentId: "env-prod", releaseVersion: "v1.4.0", minSeverity: "high", fixable: false, kev: true, includeExempt: true },
        pageSize: 50,
        pageToken: "t2",
      })
    ).toEqual({
      project: "shop",
      pageSize: 50,
      pageToken: "t2",
      environmentId: "env-prod",
      releaseVersion: "v1.4.0",
      minSeverity: WireSeverity.HIGH,
      fixable: false,
      kev: true,
      includeExempt: true,
      orgId: "org-1",
    });
    expect(summaryRequest({ orgId: null, project: "shop" })).toEqual({ project: "shop" });
  });
});
