// Copyright (c) 2026 Reliant Labs

/**
 * A fixture VulnerabilityService: the four reads the Security pages make
 * (Vulnerabilities, SBOMs, an advisory, a release's findings, the Overview's
 * card), answered on the WIRE as control-plane would answer them, from any
 * `VulnerabilityReads` (the console's plain types).
 *
 * Two sources use it: the shop pack (fixtures/shop/handlers/security.ts, over
 * vulnDataset.ts's engine) and the component tests (fixtures.ts's scenarios,
 * through `vulnerabilityTransport` under a TransportScope). Each handler
 * decodes the request into the plain types, asks the source, and encodes the
 * answer into the generated response message. So both run the pages' REAL
 * reads (hooks/vulnerability-queries.ts: the generated hooks and wire.ts) over
 * a transport, with nothing in between.
 */

import { create, type DescMethod } from "@bufbuild/protobuf";
import { timestampFromMs, type Timestamp } from "@bufbuild/protobuf/wkt";
import { createRouterTransport, type ServiceImpl, type Transport } from "@connectrpc/connect";

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
  DetailRequest,
  Finding,
  FindingPage,
  FixState,
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
  VulnerabilityDetail,
  VulnExemption,
  VulnPackage,
  VulnSummary,
} from "@/services/controlPlane/vulnerabilities/types";
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

/**
 * VulnerabilityService's reads in the console's plain types, as a fixture
 * answers them: vulnDataset.ts's engine (synchronously) or a test scenario.
 */
export interface VulnerabilityReads {
  getVulnerabilitySummary(req: SummaryRequest): VulnSummary | Promise<VulnSummary>;
  listVulnerabilities(req: ListRequest): FindingPage | Promise<FindingPage>;
  getVulnerability(req: DetailRequest): VulnerabilityDetail | Promise<VulnerabilityDetail>;
  getReleaseVulnerabilities(req: ReleaseRequest): ReleaseVulnerabilities | Promise<ReleaseVulnerabilities>;
}

const rpc = (method: string) => `${VulnerabilityService.typeName}/${method}`;

/** The RPCs served here: every read the Security pages make (hooks/vulnerability-queries.ts). */
export const VULNERABILITY_READ_RPCS = {
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
 * VulnerabilityService's reads, keyed by `VULNERABILITY_READ_RPCS`, over
 * `reads`. Each takes its request as the message (a mock transport's
 * scenario normalizes it first: shop's withMessageRequests) and answers the
 * response message; a rejection of `reads` is the rpc's error.
 */
export function vulnerabilityHandlers(reads: VulnerabilityReads): MockHandlers {
  return {
    [VULNERABILITY_READ_RPCS.getVulnerabilitySummary]: async (req: GetVulnerabilitySummaryRequest) => {
      const res = await reads.getVulnerabilitySummary({ ...base(req), environmentId: req.environmentId });
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
    [VULNERABILITY_READ_RPCS.listVulnerabilities]: async (req: ListVulnerabilitiesRequest) => {
      const res = await reads.listVulnerabilities({
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
    [VULNERABILITY_READ_RPCS.getVulnerability]: async (req: GetVulnerabilityRequest) => {
      const res = await reads.getVulnerability({ ...base(req), advisoryId: req.advisoryId });
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
    [VULNERABILITY_READ_RPCS.getReleaseVulnerabilities]: async (req: GetReleaseVulnerabilitiesRequest) => {
      const res = await reads.getReleaseVulnerabilities({
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
}

/**
 * A transport whose VulnerabilityService answers from `reads`, through
 * Connect's in-memory router (so a rejection reaches the caller as the
 * ConnectError it is, code and headers included). Every other service goes to
 * `fallback` (a test's app transport, so a page's other reads are unchanged),
 * or answers Unimplemented without one. Mount it in a TransportScope: the
 * generated hooks under it read it.
 */
export function vulnerabilityTransport(reads: VulnerabilityReads, fallback?: Transport): Transport {
  const handlers = vulnerabilityHandlers(reads);
  const impl: Record<string, (req: never) => Promise<unknown>> = {};
  for (const method of VulnerabilityService.methods) {
    const handler = handlers[rpc(method.name)];
    if (handler) impl[method.localName] = async (req: never) => handler(req);
  }
  const router = createRouterTransport(({ service }) => service(VulnerabilityService, impl as unknown as Partial<ServiceImpl<typeof VulnerabilityService>>));
  if (!fallback) return router;
  const pick = (method: DescMethod) => (method.parent.typeName === VulnerabilityService.typeName ? router : fallback);
  return {
    unary: (method, ...rest) => pick(method).unary(method, ...rest),
    stream: (method, ...rest) => pick(method).stream(method, ...rest),
  };
}
