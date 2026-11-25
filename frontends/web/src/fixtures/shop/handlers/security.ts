// Copyright (c) 2026 Reliant Labs

/**
 * The shop pack's VulnerabilityService handlers: the four reads the Security
 * pages make (Vulnerabilities, SBOMs, an advisory, a release's findings, the
 * Overview's card), answered on the WIRE as control-plane would answer them.
 * The pack composes them with the other services' handlers (../index.ts
 * shopScenario).
 *
 * The data is shop's story (model/security.ts), and the server's rules
 * (filters, order, counts, paging, gate verdicts) are Security/vulnDataset.ts's.
 * Each handler decodes the request into the client's plain types, asks the
 * engine, and encodes the answer into the generated response message. So the
 * demo runs the pages' REAL client (services/controlPlane/vulnerabilities:
 * vulnerabilityApi.ts and its wire.ts) over the fixture transport, with no
 * VulnerabilityApiProvider in between.
 */

import { create } from "@bufbuild/protobuf";
import { timestampFromMs, type Timestamp } from "@bufbuild/protobuf/wkt";

import { VulnerabilityFixState, VulnerabilityScanState, VulnerabilitySeverity } from "@/gen/controlplane/controlplane/v1/vulnerability_pb";
import {
  GetReleaseVulnerabilitiesResponseSchema,
  GetVulnerabilityResponseSchema,
  GetVulnerabilitySummaryResponseSchema,
  ListVulnerabilitiesResponseSchema,
  VulnerabilityService,
  type GetReleaseVulnerabilitiesRequest,
  type GetVulnerabilityRequest,
  type GetVulnerabilitySummaryRequest,
  type ListVulnerabilitiesRequest,
} from "@/gen/controlplane/services/vulnerability/v1/vulnerability_pb";
import type {
  Advisory,
  Finding,
  FixState,
  MinSeverity,
  ScanState,
  Severity,
  SeverityCounts,
  VulnCoverage,
  VulnDatabase,
  VulnExemption,
  VulnPackage,
} from "@/services/controlPlane/vulnerabilities/types";
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { shopVulnEngine } from "../model/security";
import { demoNow } from "../shopStory";
import { withMessageRequests } from "./defaults";
import { scopedToDemo } from "./scope";

const rpc = (method: string) => `${VulnerabilityService.typeName}/${method}`;

/** The RPCs served here: every read the Security pages' client makes (vulnerabilityApi.ts). */
export const SECURITY_RPCS = {
  getVulnerabilitySummary: rpc("GetVulnerabilitySummary"),
  listVulnerabilities: rpc("ListVulnerabilities"),
  getVulnerability: rpc("GetVulnerability"),
  getReleaseVulnerabilities: rpc("GetReleaseVulnerabilities"),
} as const;

// ── Wire → the client's plain requests ──────────────────────────────────────

const MIN_SEVERITY: Partial<Record<VulnerabilitySeverity, MinSeverity>> = {
  [VulnerabilitySeverity.CRITICAL]: "critical",
  [VulnerabilitySeverity.HIGH]: "high",
  [VulnerabilitySeverity.MEDIUM]: "medium",
  [VulnerabilitySeverity.LOW]: "low",
};

/** The request's scope as the client sends it (an absent optional is undefined). */
const base = (req: { project: string; orgId?: string }) => ({ project: req.project, orgId: req.orgId ?? null });

// ── The client's plain answers → wire ───────────────────────────────────────

const SEVERITY: Record<Severity, VulnerabilitySeverity> = {
  critical: VulnerabilitySeverity.CRITICAL,
  high: VulnerabilitySeverity.HIGH,
  medium: VulnerabilitySeverity.MEDIUM,
  low: VulnerabilitySeverity.LOW,
  negligible: VulnerabilitySeverity.NEGLIGIBLE,
  unknown: VulnerabilitySeverity.UNKNOWN,
};

const FIX_STATE: Record<FixState, VulnerabilityFixState> = {
  fixed: VulnerabilityFixState.FIXED,
  "not-fixed": VulnerabilityFixState.NOT_FIXED,
  "wont-fix": VulnerabilityFixState.WONT_FIX,
  unknown: VulnerabilityFixState.UNKNOWN,
};

const SCAN_STATE: Record<ScanState, VulnerabilityScanState> = {
  none: VulnerabilityScanState.UNSPECIFIED,
  "no-sbom": VulnerabilityScanState.NO_SBOM,
  pending: VulnerabilityScanState.PENDING,
  scanned: VulnerabilityScanState.SCANNED,
  failed: VulnerabilityScanState.FAILED,
  stale: VulnerabilityScanState.STALE,
};

/** Epoch ms → Timestamp; null (never happened) is an absent field. */
const ts = (ms: number | null): Timestamp | undefined => (ms === null ? undefined : timestampFromMs(ms));

const counts = (c: SeverityCounts) => ({ ...c });
const coverage = (c: VulnCoverage) => ({ artifacts: c.artifacts, withSbom: c.withSbom, scanned: c.scanned });
const database = (db: VulnDatabase | null) =>
  db ? { scanner: db.scanner, scannerVersion: db.scannerVersion, schema: db.schema, builtAt: ts(db.builtAtMs), stale: db.stale } : undefined;

const advisory = (a: Advisory) => ({
  id: a.id,
  aliases: [...a.aliases],
  severity: SEVERITY[a.severity],
  cvssScore: a.cvss ?? undefined,
  kev: a.kev,
  epss: a.epss ?? undefined,
  summary: a.summary,
  references: [...a.references],
});

const pkg = (p: VulnPackage) => ({ ...p });

const exemption = (x: VulnExemption) => ({ environmentId: x.environmentId, advisoryId: x.advisoryId, reason: x.reason, expiresAt: ts(x.expiresMs) });

const finding = (f: Finding) => ({
  advisory: advisory(f.advisory),
  package: pkg(f.package),
  fixedVersions: [...f.fixedVersions],
  fixState: FIX_STATE[f.fixState],
  artifactNames: [...f.artifactNames],
  environmentIds: [...f.environmentIds],
  firstSeenAt: ts(f.firstSeenMs),
  exemption: f.exemption ? exemption(f.exemption) : undefined,
  blocking: f.blocking,
});

/**
 * VulnerabilityService's reads over shop's dataset as of `now` (the pack
 * clock), with requests normalized to messages and reads outside shop
 * answered empty (scope.ts).
 */
export function securityHandlers(now: () => number = demoNow): MockHandlers {
  // One engine per transport: the dataset is fixed by the pack clock, which never moves.
  let engine: ReturnType<typeof shopVulnEngine> | null = null;
  const answers = () => (engine ??= shopVulnEngine(now()));
  const handlers: MockHandlers = {
    [SECURITY_RPCS.getVulnerabilitySummary]: (req: GetVulnerabilitySummaryRequest) => {
      const res = answers().getVulnerabilitySummary({ ...base(req), environmentId: req.environmentId });
      return create(GetVulnerabilitySummaryResponseSchema, {
        environments: res.environments.map((e) => ({
          environmentId: e.environmentId,
          environmentName: e.environmentName,
          releaseId: e.releaseId,
          releaseVersion: e.releaseVersion,
          counts: counts(e.counts),
          kevCount: e.kev,
          fixableCount: e.fixable,
          blockingCount: e.blocking,
          exemptCount: e.exempt,
          coverage: coverage(e.coverage),
          lastScannedAt: ts(e.lastScannedMs),
          scanState: SCAN_STATE[e.scanState],
          failingSince: ts(e.failingSinceMs),
        })),
        database: database(res.database),
      });
    },
    [SECURITY_RPCS.listVulnerabilities]: (req: ListVulnerabilitiesRequest) => {
      const res = answers().listVulnerabilities({
        ...base(req),
        filters: {
          environmentId: req.environmentId || undefined,
          releaseVersion: req.releaseVersion || undefined,
          minSeverity: req.minSeverity === undefined ? undefined : MIN_SEVERITY[req.minSeverity],
          fixable: req.fixable,
          kev: req.kev,
          includeExempt: req.includeExempt,
        },
        pageSize: req.pageSize,
        pageToken: req.pageToken,
      });
      return create(ListVulnerabilitiesResponseSchema, { findings: res.findings.map(finding), nextPageToken: res.nextPageToken, database: database(res.database) });
    },
    [SECURITY_RPCS.getVulnerability]: (req: GetVulnerabilityRequest) => {
      const res = answers().getVulnerability({ ...base(req), advisoryId: req.advisoryId });
      return create(GetVulnerabilityResponseSchema, {
        advisory: advisory(res.advisory),
        occurrences: res.occurrences.map((o) => ({
          releaseId: o.releaseId,
          releaseVersion: o.releaseVersion,
          artifactName: o.artifactName,
          sbomDigest: o.sbomDigest,
          package: pkg(o.package),
          fixedVersions: [...o.fixedVersions],
          fixState: FIX_STATE[o.fixState],
          environmentIds: [...o.environmentIds],
        })),
        observations: res.observations.map((o) => ({
          environmentId: o.environmentId,
          purl: o.purl,
          firstSeenAt: ts(o.firstSeenMs),
          lastSeenAt: ts(o.lastSeenMs),
          resolvedAt: ts(o.resolvedMs),
          firstReleaseVersion: o.firstReleaseVersion,
        })),
        exemptions: res.exemptions.map(exemption),
        gateVerdicts: res.gateVerdicts.map((v) => ({
          promotionId: v.promotionId,
          environmentId: v.environmentId,
          releaseVersion: v.releaseVersion,
          mode: v.mode,
          verdict: v.verdict,
          overrideReason: v.overrideReason,
          createdAt: ts(v.createdMs),
        })),
      });
    },
    [SECURITY_RPCS.getReleaseVulnerabilities]: (req: GetReleaseVulnerabilitiesRequest) => {
      const res = answers().getReleaseVulnerabilities({
        ...base(req),
        version: req.version,
        environmentId: req.environmentId || undefined,
        pageSize: req.pageSize,
        pageToken: req.pageToken,
      });
      return create(GetReleaseVulnerabilitiesResponseSchema, {
        releaseId: res.releaseId,
        releaseVersion: res.releaseVersion,
        artifacts: res.artifacts.map((a) => ({
          artifactName: a.artifactName,
          sbomDigest: a.sbomDigest,
          format: a.format,
          generator: a.generator,
          source: a.source,
          referrer: a.referrer,
          componentCount: a.componentCount,
          scanState: SCAN_STATE[a.scanState],
          lastScannedAt: ts(a.lastScannedMs),
          scanError: a.scanError,
        })),
        coverage: coverage(res.coverage),
        counts: counts(res.counts),
        kevCount: res.kev,
        fixableCount: res.fixable,
        blockingCount: res.blocking,
        exemptCount: res.exempt,
        findings: res.findings.map(finding),
        nextPageToken: res.nextPageToken,
        database: database(res.database),
      });
    },
  };
  return withMessageRequests(scopedToDemo(handlers, [VulnerabilityService]), [VulnerabilityService]);
}
