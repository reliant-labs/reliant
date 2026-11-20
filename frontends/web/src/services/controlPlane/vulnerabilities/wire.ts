// Copyright (c) 2025 Reliant Labs

/**
 * The ONLY file that knows VulnerabilityService's wire shape: the response
 * messages, as protobuf-es generates them, mapped to the plain types in
 * types.ts, and the requests built from them.
 *
 * THE WIRE TYPES BELOW ARE DECLARED BY HAND, FOR NOW. The generated client
 * comes from proto-vendor/controlplane/, which takes only the services
 * control-plane's proto/public-api.txt EXPORTS, and VulnerabilityService is
 * withheld there until control-plane mounts it on platform-api (control-plane
 * #3160). Each interface here is the structural subset of the generated
 * message of the same name (control-plane#3147's
 * proto/services/vulnerability/v1/vulnerability.proto and
 * proto/controlplane/v1/vulnerability.proto): field names as protobuf-es
 * writes them, enums as their numbers, timestamps as google.protobuf.Timestamp.
 * So once `task contract:controlplane:sync` brings the service in, the
 * generated messages are assignable to these, and the swap is
 * vulnerabilityApi.ts calling the generated client; these declarations can
 * then be replaced by imports of the generated types.
 */

import { timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";

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

// ── Enums (controlplane/v1/vulnerability.proto) ──────────────────────────────

/** controlplane.v1.VulnerabilitySeverity. Ordered: a higher number is more severe. */
export const WireSeverity = { UNSPECIFIED: 0, UNKNOWN: 1, NEGLIGIBLE: 2, LOW: 3, MEDIUM: 4, HIGH: 5, CRITICAL: 6 } as const;
/** controlplane.v1.VulnerabilityFixState. */
export const WireFixState = { UNSPECIFIED: 0, FIXED: 1, NOT_FIXED: 2, WONT_FIX: 3, UNKNOWN: 4 } as const;
/** controlplane.v1.VulnerabilityScanState. */
export const WireScanState = { UNSPECIFIED: 0, NO_SBOM: 1, PENDING: 2, SCANNED: 3, FAILED: 4, STALE: 5 } as const;

// ── Messages ────────────────────────────────────────────────────────────────

export interface WireDatabase {
  scanner: string;
  scannerVersion: string;
  schema: string;
  builtAt?: Timestamp;
  stale: boolean;
}

export interface WireSeverityCounts {
  critical: number;
  high: number;
  medium: number;
  low: number;
  negligible: number;
  unknown: number;
}

export interface WireCoverage {
  artifacts: number;
  withSbom: number;
  scanned: number;
}

export interface WireEnvSummary {
  environmentId: string;
  environmentName: string;
  releaseId: string;
  releaseVersion: string;
  counts?: WireSeverityCounts;
  kevCount: number;
  fixableCount: number;
  blockingCount: number;
  exemptCount: number;
  coverage?: WireCoverage;
  lastScannedAt?: Timestamp;
  scanState: number;
  failingSince?: Timestamp;
}

export interface WireSummaryResponse {
  environments: WireEnvSummary[];
  database?: WireDatabase;
}

export interface WireAdvisory {
  id: string;
  aliases: string[];
  severity: number;
  cvssScore?: number;
  kev: boolean;
  epss?: number;
  summary: string;
  references: string[];
}

export interface WirePackage {
  name: string;
  version: string;
  type: string;
  purl: string;
  location: string;
}

export interface WireExemption {
  environmentId: string;
  advisoryId: string;
  reason: string;
  expiresAt?: Timestamp;
}

export interface WireFinding {
  advisory?: WireAdvisory;
  package?: WirePackage;
  fixedVersions: string[];
  fixState: number;
  artifactNames: string[];
  environmentIds: string[];
  firstSeenAt?: Timestamp;
  exemption?: WireExemption;
  blocking: boolean;
}

export interface WireListResponse {
  findings: WireFinding[];
  nextPageToken: string;
  database?: WireDatabase;
}

export interface WireOccurrence {
  releaseId: string;
  releaseVersion: string;
  artifactName: string;
  sbomDigest: string;
  package?: WirePackage;
  fixedVersions: string[];
  fixState: number;
  environmentIds: string[];
}

export interface WireObservation {
  environmentId: string;
  purl: string;
  firstSeenAt?: Timestamp;
  lastSeenAt?: Timestamp;
  resolvedAt?: Timestamp;
  firstReleaseVersion: string;
}

export interface WireGateVerdict {
  promotionId: string;
  environmentId: string;
  releaseVersion: string;
  mode: string;
  verdict: string;
  overrideReason: string;
  createdAt?: Timestamp;
}

export interface WireDetailResponse {
  advisory?: WireAdvisory;
  occurrences: WireOccurrence[];
  observations: WireObservation[];
  exemptions: WireExemption[];
  gateVerdicts: WireGateVerdict[];
}

export interface WireArtifactSbom {
  artifactName: string;
  sbomDigest: string;
  format: string;
  generator: string;
  source: string;
  referrer: string;
  componentCount: number;
  scanState: number;
  lastScannedAt?: Timestamp;
  scanError: string;
}

export interface WireReleaseResponse {
  releaseId: string;
  releaseVersion: string;
  artifacts: WireArtifactSbom[];
  coverage?: WireCoverage;
  counts?: WireSeverityCounts;
  kevCount: number;
  fixableCount: number;
  blockingCount: number;
  exemptCount: number;
  findings: WireFinding[];
  nextPageToken: string;
  database?: WireDatabase;
}

// ── Responses → plain types ─────────────────────────────────────────────────

function ms(ts: Timestamp | undefined): number | null {
  if (!ts) return null;
  const value = timestampMs(ts);
  // The zero timestamp is "unset" written out, never an event in 1970.
  return value > 0 ? value : null;
}

export function severityOf(value: number): Severity {
  switch (value) {
    case WireSeverity.CRITICAL:
      return "critical";
    case WireSeverity.HIGH:
      return "high";
    case WireSeverity.MEDIUM:
      return "medium";
    case WireSeverity.LOW:
      return "low";
    case WireSeverity.NEGLIGIBLE:
      return "negligible";
    default:
      return "unknown";
  }
}

export function fixStateOf(value: number): FixState {
  switch (value) {
    case WireFixState.FIXED:
      return "fixed";
    case WireFixState.NOT_FIXED:
      return "not-fixed";
    case WireFixState.WONT_FIX:
      return "wont-fix";
    default:
      return "unknown";
  }
}

/**
 * A scan state a newer server adds, that this client does not know, reads as
 * "pending": it is not "scanned", and so never shows as clean.
 */
export function scanStateOf(value: number): ScanState {
  switch (value) {
    case WireScanState.UNSPECIFIED:
      return "none";
    case WireScanState.NO_SBOM:
      return "no-sbom";
    case WireScanState.SCANNED:
      return "scanned";
    case WireScanState.FAILED:
      return "failed";
    case WireScanState.STALE:
      return "stale";
    default:
      return "pending";
  }
}

function databaseOf(db: WireDatabase | undefined): VulnDatabase | null {
  if (!db) return null;
  return { scanner: db.scanner, scannerVersion: db.scannerVersion, schema: db.schema, builtAtMs: ms(db.builtAt), stale: db.stale };
}

const ZERO_COUNTS: SeverityCounts = { critical: 0, high: 0, medium: 0, low: 0, negligible: 0, unknown: 0 };

function countsOf(counts: WireSeverityCounts | undefined): SeverityCounts {
  if (!counts) return { ...ZERO_COUNTS };
  return { critical: counts.critical, high: counts.high, medium: counts.medium, low: counts.low, negligible: counts.negligible, unknown: counts.unknown };
}

function coverageOf(coverage: WireCoverage | undefined): VulnCoverage {
  return { artifacts: coverage?.artifacts ?? 0, withSbom: coverage?.withSbom ?? 0, scanned: coverage?.scanned ?? 0 };
}

function envSummaryOf(env: WireEnvSummary): EnvVulnSummary {
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

export function summaryOf(res: WireSummaryResponse): VulnSummary {
  return { environments: res.environments.map(envSummaryOf), database: databaseOf(res.database) };
}

function advisoryOf(a: WireAdvisory | undefined): Advisory {
  return {
    id: a?.id ?? "",
    aliases: a?.aliases ?? [],
    severity: severityOf(a?.severity ?? 0),
    cvss: a?.cvssScore ?? null,
    kev: a?.kev ?? false,
    epss: a?.epss ?? null,
    summary: a?.summary ?? "",
    references: a?.references ?? [],
  };
}

function packageOf(p: WirePackage | undefined): VulnPackage {
  return { name: p?.name ?? "", version: p?.version ?? "", type: p?.type ?? "", purl: p?.purl ?? "", location: p?.location ?? "" };
}

function exemptionOf(x: WireExemption): VulnExemption {
  return { environmentId: x.environmentId, advisoryId: x.advisoryId, reason: x.reason, expiresMs: ms(x.expiresAt) };
}

function findingOf(f: WireFinding): Finding {
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

export function findingPageOf(res: WireListResponse): FindingPage {
  return { findings: res.findings.map(findingOf), nextPageToken: res.nextPageToken, database: databaseOf(res.database) };
}

export function detailOf(res: WireDetailResponse): VulnerabilityDetail {
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

export function releaseOf(res: WireReleaseResponse): ReleaseVulnerabilities {
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

const MIN_SEVERITY_WIRE: Record<MinSeverity, number> = {
  critical: WireSeverity.CRITICAL,
  high: WireSeverity.HIGH,
  medium: WireSeverity.MEDIUM,
  low: WireSeverity.LOW,
};

/** Only the fields that are set: every filter is proto3 `optional`, so an absent field means "no filter". */
function defined<T extends Record<string, unknown>>(fields: T): Partial<T> {
  return Object.fromEntries(Object.entries(fields).filter(([, value]) => value !== undefined && value !== null && value !== "")) as Partial<T>;
}

export function summaryRequest(req: SummaryRequest) {
  return { project: req.project, ...defined({ environmentId: req.environmentId, orgId: req.orgId ?? undefined }) };
}

export function listRequest(req: ListRequest) {
  const f = req.filters;
  return {
    project: req.project,
    pageSize: req.pageSize ?? 0,
    pageToken: req.pageToken ?? "",
    ...defined({
      environmentId: f.environmentId,
      releaseVersion: f.releaseVersion,
      minSeverity: f.minSeverity ? MIN_SEVERITY_WIRE[f.minSeverity] : undefined,
      fixable: f.fixable,
      kev: f.kev,
      includeExempt: f.includeExempt || undefined,
      orgId: req.orgId ?? undefined,
    }),
  };
}

export function detailRequest(req: DetailRequest) {
  return { project: req.project, advisoryId: req.advisoryId, ...defined({ orgId: req.orgId ?? undefined }) };
}

export function releaseRequest(req: ReleaseRequest) {
  return {
    project: req.project,
    version: req.version,
    pageSize: req.pageSize ?? 0,
    pageToken: req.pageToken ?? "",
    ...defined({ environmentId: req.environmentId, orgId: req.orgId ?? undefined }),
  };
}
