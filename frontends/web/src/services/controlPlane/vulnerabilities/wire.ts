// Copyright (c) 2025 Reliant Labs

/**
 * The ONLY file that knows VulnerabilityService's wire shape: the generated
 * response messages (proto-vendor/controlplane, control-plane's
 * proto/services/vulnerability/v1/vulnerability.proto and
 * proto/controlplane/v1/vulnerability.proto) mapped to the plain types in
 * types.ts, and the requests built for the generated client.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import { timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";

import { VulnerabilityFixState, VulnerabilityScanState, VulnerabilitySeverity, type VulnerabilityDatabase } from "@/gen/controlplane/controlplane/v1/vulnerability_pb";
import type {
  EnvironmentVulnerabilitySummary,
  GetReleaseVulnerabilitiesRequestSchema,
  GetReleaseVulnerabilitiesResponse,
  GetVulnerabilityRequestSchema,
  GetVulnerabilityResponse,
  GetVulnerabilitySummaryRequestSchema,
  GetVulnerabilitySummaryResponse,
  ListVulnerabilitiesRequestSchema,
  ListVulnerabilitiesResponse,
  VulnerabilityAdvisory,
  VulnerabilityCoverage,
  VulnerabilityExemption,
  VulnerabilityFinding,
  VulnerabilitySeverityCounts,
  VulnerablePackage,
} from "@/gen/controlplane/services/vulnerability/v1/vulnerability_pb";

import type {
  Advisory,
  ArtifactSbom,
  DetailRequest,
  EnvVulnSummary,
  Finding,
  FindingPage,
  FixState,
  GateVerdict,
  ListRequest,
  MinSeverity,
  ReleaseRequest,
  ReleaseVulnerabilities,
  ScanState,
  Severity,
  SeverityCounts,
  SummaryRequest,
  VulnCoverage,
  VulnDatabase,
  VulnExemption,
  VulnObservation,
  VulnOccurrence,
  VulnPackage,
  VulnSummary,
  VulnerabilityDetail,
} from "./types";

// ── Responses → plain types ─────────────────────────────────────────────────

function ms(ts: Timestamp | undefined): number | null {
  if (!ts) return null;
  const value = timestampMs(ts);
  // The zero timestamp is "unset" written out, never an event in 1970.
  return value > 0 ? value : null;
}

export function severityOf(value: VulnerabilitySeverity): Severity {
  switch (value) {
    case VulnerabilitySeverity.CRITICAL:
      return "critical";
    case VulnerabilitySeverity.HIGH:
      return "high";
    case VulnerabilitySeverity.MEDIUM:
      return "medium";
    case VulnerabilitySeverity.LOW:
      return "low";
    case VulnerabilitySeverity.NEGLIGIBLE:
      return "negligible";
    default:
      return "unknown";
  }
}

export function fixStateOf(value: VulnerabilityFixState): FixState {
  switch (value) {
    case VulnerabilityFixState.FIXED:
      return "fixed";
    case VulnerabilityFixState.NOT_FIXED:
      return "not-fixed";
    case VulnerabilityFixState.WONT_FIX:
      return "wont-fix";
    default:
      return "unknown";
  }
}

/**
 * A scan state a newer server adds, that this client does not know, reads as
 * "pending": it is not "scanned", and so never shows as clean.
 */
export function scanStateOf(value: VulnerabilityScanState): ScanState {
  switch (value) {
    case VulnerabilityScanState.UNSPECIFIED:
      return "none";
    case VulnerabilityScanState.NO_SBOM:
      return "no-sbom";
    case VulnerabilityScanState.SCANNED:
      return "scanned";
    case VulnerabilityScanState.FAILED:
      return "failed";
    case VulnerabilityScanState.STALE:
      return "stale";
    default:
      return "pending";
  }
}

function databaseOf(db: VulnerabilityDatabase | undefined): VulnDatabase | null {
  if (!db) return null;
  return { scanner: db.scanner, scannerVersion: db.scannerVersion, schema: db.schema, builtAtMs: ms(db.builtAt), stale: db.stale };
}

const ZERO_COUNTS: SeverityCounts = { critical: 0, high: 0, medium: 0, low: 0, negligible: 0, unknown: 0 };

function countsOf(counts: VulnerabilitySeverityCounts | undefined): SeverityCounts {
  if (!counts) return { ...ZERO_COUNTS };
  return { critical: counts.critical, high: counts.high, medium: counts.medium, low: counts.low, negligible: counts.negligible, unknown: counts.unknown };
}

function coverageOf(coverage: VulnerabilityCoverage | undefined): VulnCoverage {
  return { artifacts: coverage?.artifacts ?? 0, withSbom: coverage?.withSbom ?? 0, scanned: coverage?.scanned ?? 0 };
}

function envSummaryOf(env: EnvironmentVulnerabilitySummary): EnvVulnSummary {
  return {
    environmentId: env.environmentId,
    environmentName: env.environmentName,
    releaseId: env.releaseId,
    releaseVersion: env.releaseVersion,
    counts: countsOf(env.counts),
    kev: env.kevCount,
    fixable: env.fixableCount,
    blocking: env.blockingCount,
    exempt: env.exemptCount,
    coverage: coverageOf(env.coverage),
    lastScannedMs: ms(env.lastScannedAt),
    // An environment with no release has nothing to judge, whatever the state says.
    scanState: env.releaseId ? scanStateOf(env.scanState) : "none",
    failingSinceMs: ms(env.failingSince),
  };
}

export function summaryOf(res: GetVulnerabilitySummaryResponse): VulnSummary {
  return { environments: res.environments.map(envSummaryOf), database: databaseOf(res.database) };
}

function advisoryOf(a: VulnerabilityAdvisory | undefined): Advisory {
  return {
    id: a?.id ?? "",
    aliases: a?.aliases ?? [],
    severity: severityOf(a?.severity ?? VulnerabilitySeverity.UNSPECIFIED),
    cvss: a?.cvssScore ?? null,
    kev: a?.kev ?? false,
    epss: a?.epss ?? null,
    summary: a?.summary ?? "",
    references: a?.references ?? [],
  };
}

function packageOf(p: VulnerablePackage | undefined): VulnPackage {
  return { name: p?.name ?? "", version: p?.version ?? "", type: p?.type ?? "", purl: p?.purl ?? "", location: p?.location ?? "" };
}

function exemptionOf(x: VulnerabilityExemption): VulnExemption {
  return { environmentId: x.environmentId, advisoryId: x.advisoryId, reason: x.reason, expiresMs: ms(x.expiresAt) };
}

function findingOf(f: VulnerabilityFinding): Finding {
  return {
    advisory: advisoryOf(f.advisory),
    package: packageOf(f.package),
    fixedVersions: f.fixedVersions,
    fixState: fixStateOf(f.fixState),
    artifactNames: f.artifactNames,
    environmentIds: f.environmentIds,
    firstSeenMs: ms(f.firstSeenAt),
    exemption: f.exemption ? exemptionOf(f.exemption) : null,
    blocking: f.blocking,
  };
}

export function findingPageOf(res: ListVulnerabilitiesResponse): FindingPage {
  return { findings: res.findings.map(findingOf), nextPageToken: res.nextPageToken, database: databaseOf(res.database) };
}

export function detailOf(res: GetVulnerabilityResponse): VulnerabilityDetail {
  return {
    advisory: advisoryOf(res.advisory),
    occurrences: res.occurrences.map(
      (o): VulnOccurrence => ({
        releaseId: o.releaseId,
        releaseVersion: o.releaseVersion,
        artifactName: o.artifactName,
        sbomDigest: o.sbomDigest,
        package: packageOf(o.package),
        fixedVersions: o.fixedVersions,
        fixState: fixStateOf(o.fixState),
        environmentIds: o.environmentIds,
      })
    ),
    observations: res.observations.map(
      (o): VulnObservation => ({
        environmentId: o.environmentId,
        purl: o.purl,
        firstSeenMs: ms(o.firstSeenAt),
        lastSeenMs: ms(o.lastSeenAt),
        resolvedMs: ms(o.resolvedAt),
        firstReleaseVersion: o.firstReleaseVersion,
      })
    ),
    exemptions: res.exemptions.map(exemptionOf),
    gateVerdicts: res.gateVerdicts.map(
      (v): GateVerdict => ({
        promotionId: v.promotionId,
        environmentId: v.environmentId,
        releaseVersion: v.releaseVersion,
        mode: v.mode,
        verdict: v.verdict,
        overrideReason: v.overrideReason,
        createdMs: ms(v.createdAt),
      })
    ),
  };
}

export function releaseOf(res: GetReleaseVulnerabilitiesResponse): ReleaseVulnerabilities {
  return {
    releaseId: res.releaseId,
    releaseVersion: res.releaseVersion,
    artifacts: res.artifacts.map(
      (a): ArtifactSbom => ({
        artifactName: a.artifactName,
        sbomDigest: a.sbomDigest,
        format: a.format,
        generator: a.generator,
        source: a.source,
        referrer: a.referrer,
        componentCount: a.componentCount,
        // No digest is no SBOM, whatever a server says beside it.
        scanState: a.sbomDigest ? scanStateOf(a.scanState) : "no-sbom",
        lastScannedMs: ms(a.lastScannedAt),
        scanError: a.scanError,
      })
    ),
    coverage: coverageOf(res.coverage),
    counts: countsOf(res.counts),
    kev: res.kevCount,
    fixable: res.fixableCount,
    blocking: res.blockingCount,
    exempt: res.exemptCount,
    findings: res.findings.map(findingOf),
    nextPageToken: res.nextPageToken,
    database: databaseOf(res.database),
  };
}

// ── Requests ────────────────────────────────────────────────────────────────

const MIN_SEVERITY_WIRE: Record<MinSeverity, VulnerabilitySeverity> = {
  critical: VulnerabilitySeverity.CRITICAL,
  high: VulnerabilitySeverity.HIGH,
  medium: VulnerabilitySeverity.MEDIUM,
  low: VulnerabilitySeverity.LOW,
};

// Every filter and scope field is proto3 `optional`: undefined leaves it absent ("no filter"), a
// present false is a filter. An empty string or a null org is absent too.

export function summaryRequest(req: SummaryRequest): MessageInitShape<typeof GetVulnerabilitySummaryRequestSchema> {
  return { project: req.project, environmentId: req.environmentId || undefined, orgId: req.orgId || undefined };
}

export function listRequest(req: ListRequest): MessageInitShape<typeof ListVulnerabilitiesRequestSchema> {
  const f = req.filters;
  return {
    project: req.project,
    pageSize: req.pageSize ?? 0,
    pageToken: req.pageToken ?? "",
    environmentId: f.environmentId || undefined,
    releaseVersion: f.releaseVersion || undefined,
    minSeverity: f.minSeverity ? MIN_SEVERITY_WIRE[f.minSeverity] : undefined,
    fixable: f.fixable,
    kev: f.kev,
    includeExempt: f.includeExempt || undefined,
    orgId: req.orgId || undefined,
  };
}

export function detailRequest(req: DetailRequest): MessageInitShape<typeof GetVulnerabilityRequestSchema> {
  return { project: req.project, advisoryId: req.advisoryId, orgId: req.orgId || undefined };
}

export function releaseRequest(req: ReleaseRequest): MessageInitShape<typeof GetReleaseVulnerabilitiesRequestSchema> {
  return {
    project: req.project,
    version: req.version,
    pageSize: req.pageSize ?? 0,
    pageToken: req.pageToken ?? "",
    environmentId: req.environmentId || undefined,
    orgId: req.orgId || undefined,
  };
}
