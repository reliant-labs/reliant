// Copyright (c) 2025 Reliant Labs

/**
 * The Security pages' fixtures, shaped like control-plane's VulnerabilityService
 * read API (control-plane#3161, "C5"), as the plain types of
 * services/controlPlane/vulnerabilities/types.ts. ONE module, shared by the
 * page tests, the dev preview and the console nav's Security section, so they
 * all speak one shape.
 *
 * `fixtureVulnerabilityApi(scenario)` is an in-memory VulnerabilityService that
 * answers the way the server does: filters as ListVulnerabilities applies
 * them (exempt rows left out unless asked, an UNKNOWN severity passing every
 * minimum), keyset-style paging, NotFound for an advisory found nowhere, and
 * the scan states C5 derives (no SBOM, pending, failed with failing-since,
 * stale, scanned). Serve it with vulnService.ts's `vulnerabilityTransport`
 * under a TransportScope (__tests__/securityTestUtils.tsx's renderWith), so
 * the pages' generated reads ask it over the wire.
 *
 * The project is "shop": prod runs v1.4.0, staging runs v1.5.0, and dev was
 * never promoted. Each release has three artifacts: `api` (an image), `worker`
 * (an image) and `web` (a static site, whose SBOM is from its lockfile).
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type {
  ArtifactSbom,
  EnvVulnSummary,
  Finding,
  ReleaseVulnerabilities,
  ScanState,
  VulnDatabase,
  VulnerabilityDetail,
} from "@/services/controlPlane/vulnerabilities/types";

import { byServerOrder, countsOf, passes } from "./vulnDataset";
import type { VulnerabilityReads } from "./vulnService";

/**
 * A fixed "now", so relative times do not move: the visual harness's and the
 * preview's frozen clock, FIXED_NOW in e2e/visual/clock.ts (2026-11-18
 * 15:30 UTC). src cannot import e2e/, so it is repeated here and
 * __tests__/fixtures.test.ts holds the two equal. Every fixture time here is
 * relative to it, so nothing that already happened lands in the preview's
 * future (only an exemption's expiry is ahead of it).
 */
export const FIXTURE_NOW = Date.UTC(2026, 10, 18, 15, 30, 0);
const DAY = 86_400_000;

export const FIXTURE_PROJECT = "shop";
export const FIXTURE_ENVS = {
  prod: { id: "env-prod", name: "prod", release: "v1.4.0", releaseId: "rel-140" },
  staging: { id: "env-staging", name: "staging", release: "v1.5.0", releaseId: "rel-150" },
  dev: { id: "env-dev", name: "dev", release: "", releaseId: "" },
} as const;

export const FIXTURE_DATABASE: VulnDatabase = {
  scanner: "grype",
  scannerVersion: "v0.122.1",
  schema: "v6.1.10",
  builtAtMs: FIXTURE_NOW - 0.5 * DAY,
  stale: false,
};

const pkg = (name: string, version: string, type: string, purl: string) => ({ name, version, type, purl, location: "" });

/** prod's (v1.4.0) findings, with prod's policy applied. */
export const FIXTURE_FINDINGS: Finding[] = [
  {
    advisory: {
      id: "CVE-2026-4101",
      aliases: ["GHSA-7f3x-q2mv-9hc4"],
      severity: "critical",
      cvss: 9.8,
      kev: true,
      epss: 0.94,
      summary: "HTTP/2 rapid reset lets a client exhaust server memory.",
      references: [],
    },
    package: pkg("golang.org/x/net", "v0.17.0", "go-module", "pkg:golang/golang.org/x/net@v0.17.0"),
    fixedVersions: ["v0.23.0"],
    fixState: "fixed",
    artifactNames: ["api", "worker"],
    environmentIds: [FIXTURE_ENVS.prod.id, FIXTURE_ENVS.staging.id],
    firstSeenMs: FIXTURE_NOW - 12 * DAY,
    exemption: null,
    blocking: true,
  },
  {
    advisory: {
      id: "GHSA-c2qf-rxjj-qqgw",
      aliases: ["CVE-2026-2207"],
      severity: "high",
      cvss: 7.5,
      kev: false,
      epss: 0.031,
      summary: "Prototype pollution through crafted query strings.",
      references: [],
    },
    package: pkg("qs", "6.10.3", "npm", "pkg:npm/qs@6.10.3"),
    fixedVersions: ["6.10.4"],
    fixState: "fixed",
    artifactNames: ["web"],
    environmentIds: [FIXTURE_ENVS.prod.id],
    firstSeenMs: FIXTURE_NOW - 3 * DAY,
    exemption: null,
    blocking: true,
  },
  {
    advisory: {
      id: "CVE-2025-9912",
      aliases: [],
      severity: "medium",
      cvss: 5.3,
      kev: false,
      epss: 0.004,
      summary: "Out-of-bounds read in the zlib inflate window.",
      references: [],
    },
    package: pkg("zlib", "1.2.13-r0", "apk", "pkg:apk/alpine/zlib@1.2.13-r0"),
    fixedVersions: [],
    fixState: "not-fixed",
    artifactNames: ["api"],
    environmentIds: [FIXTURE_ENVS.prod.id, FIXTURE_ENVS.staging.id],
    firstSeenMs: FIXTURE_NOW - 40 * DAY,
    exemption: null,
    blocking: false,
  },
  {
    advisory: {
      id: "CVE-2025-1180",
      aliases: [],
      severity: "low",
      cvss: 3.1,
      kev: false,
      epss: null,
      summary: "Timing side channel in a constant-time compare.",
      references: [],
    },
    package: pkg("openssl", "3.1.4-r1", "apk", "pkg:apk/alpine/openssl@3.1.4-r1"),
    fixedVersions: [],
    fixState: "wont-fix",
    artifactNames: ["worker"],
    environmentIds: [FIXTURE_ENVS.prod.id],
    firstSeenMs: FIXTURE_NOW - 60 * DAY,
    exemption: null,
    blocking: false,
  },
  {
    advisory: {
      id: "CVE-2026-0042",
      aliases: [],
      severity: "unknown",
      cvss: null,
      kev: false,
      epss: null,
      summary: "",
      references: [],
    },
    package: pkg("github.com/acme/yaml", "v1.0.2", "go-module", "pkg:golang/github.com/acme/yaml@v1.0.2"),
    fixedVersions: [],
    fixState: "unknown",
    artifactNames: ["api"],
    environmentIds: [FIXTURE_ENVS.prod.id],
    firstSeenMs: FIXTURE_NOW - 1 * DAY,
    exemption: null,
    blocking: false,
  },
  {
    advisory: {
      id: "CVE-2026-3333",
      aliases: ["GHSA-aaaa-bbbb-cccc"],
      severity: "high",
      cvss: 8.1,
      kev: false,
      epss: 0.12,
      summary: "Path traversal in a static file handler the app never mounts.",
      references: [],
    },
    package: pkg("github.com/acme/static", "v2.3.0", "go-module", "pkg:golang/github.com/acme/static@v2.3.0"),
    fixedVersions: ["v2.3.1"],
    fixState: "fixed",
    artifactNames: ["api"],
    environmentIds: [FIXTURE_ENVS.prod.id],
    firstSeenMs: FIXTURE_NOW - 20 * DAY,
    exemption: {
      environmentId: FIXTURE_ENVS.prod.id,
      advisoryId: "CVE-2026-3333",
      reason: "The static handler is not mounted; tracked in shop#212.",
      expiresMs: FIXTURE_NOW + 25 * DAY,
    },
    blocking: false,
  },
];

function artifactsOf(release: string, state: ScanState, scanError = ""): ArtifactSbom[] {
  const scanned = state === "scanned" || state === "stale" ? FIXTURE_NOW - 2 * 3_600_000 : state === "failed" ? FIXTURE_NOW - 3 * DAY : null;
  const sbom = (name: string, source: string, componentCount: number, artifactState: ScanState): ArtifactSbom => ({
    artifactName: name,
    sbomDigest: artifactState === "no-sbom" ? "" : `sha256:${release.replace(/\W/g, "")}${name}`.padEnd(71, "0"),
    format: artifactState === "no-sbom" ? "" : "cyclonedx-json@1.6",
    generator: artifactState === "no-sbom" ? "" : "syft@v1.57.2",
    source: artifactState === "no-sbom" ? "" : source,
    referrer: artifactState === "no-sbom" || source === "lockfile" ? "" : `registry.reliant.dev/shop/${name}@sha256:${"ab".repeat(32)}`,
    componentCount: artifactState === "no-sbom" ? 0 : componentCount,
    scanState: artifactState,
    lastScannedMs: artifactState === "no-sbom" || artifactState === "pending" ? null : scanned,
    scanError: artifactState === "failed" ? scanError : "",
  });
  // The state names the least trustworthy artifact: `api` carries it. The others share a release-wide
  // state (pending, stale) and are otherwise scanned.
  const others: ScanState = state === "no-sbom" || state === "failed" ? "scanned" : state;
  return [sbom("api", "image", 412, state), sbom("worker", "image", 388, others), sbom("web", "lockfile", 1204, others)];
}

/** The scenarios a test or the preview can render. */
export type VulnScenario =
  /** prod and staging scanned, with findings; dev never promoted. */
  | "findings"
  /** Every release scanned, nothing found. */
  | "clean"
  /** Nothing ever promoted anywhere. */
  | "nothing-deployed"
  /** Releases cut before SBOMs: nothing has an SBOM. */
  | "no-sbom"
  /** SBOMs recorded, first scans still running. */
  | "pending"
  /** prod's newest scan failed (its previous scan's findings still count). */
  | "failed"
  /** prod's newest scan used a stale database. */
  | "stale"
  /** prod scanned; staging's release has an artifact without an SBOM. */
  | "partial"
  /** The caller lacks vuln:read. */
  | "denied"
  /** A control plane without the service. */
  | "not-configured";

interface ScenarioData {
  envState: Record<"prod" | "staging", ScanState>;
  findings: Finding[];
  database: VulnDatabase;
  error?: ConnectError;
}

export const FIXTURE_SCAN_ERROR = "grype: SBOM component limit exceeded (200,000)";

function scenarioData(scenario: VulnScenario): ScenarioData {
  switch (scenario) {
    case "findings":
      return { envState: { prod: "scanned", staging: "scanned" }, findings: FIXTURE_FINDINGS, database: FIXTURE_DATABASE };
    case "clean":
      return { envState: { prod: "scanned", staging: "scanned" }, findings: [], database: FIXTURE_DATABASE };
    case "nothing-deployed":
      return { envState: { prod: "none", staging: "none" }, findings: [], database: FIXTURE_DATABASE };
    case "no-sbom":
      return { envState: { prod: "no-sbom", staging: "no-sbom" }, findings: [], database: FIXTURE_DATABASE };
    case "pending":
      return { envState: { prod: "pending", staging: "pending" }, findings: [], database: FIXTURE_DATABASE };
    case "failed":
      return { envState: { prod: "failed", staging: "scanned" }, findings: FIXTURE_FINDINGS, database: FIXTURE_DATABASE };
    case "stale":
      return {
        envState: { prod: "stale", staging: "scanned" },
        findings: FIXTURE_FINDINGS,
        database: { ...FIXTURE_DATABASE, builtAtMs: FIXTURE_NOW - 9 * DAY, stale: true },
      };
    case "partial":
      return { envState: { prod: "scanned", staging: "no-sbom" }, findings: FIXTURE_FINDINGS, database: FIXTURE_DATABASE };
    case "denied":
      return {
        envState: { prod: "scanned", staging: "scanned" },
        findings: [],
        database: FIXTURE_DATABASE,
        error: new ConnectError("missing vuln:read in target organization", Code.PermissionDenied),
      };
    case "not-configured":
      return {
        envState: { prod: "scanned", staging: "scanned" },
        findings: [],
        database: FIXTURE_DATABASE,
        error: new ConnectError("vulnerability findings are not configured on this deployment", Code.Unimplemented),
      };
  }
}

/** The findings of one environment's current release, as the server scopes them. */
function envFindings(data: ScenarioData, envId: string): Finding[] {
  const key = envId === FIXTURE_ENVS.prod.id ? "prod" : envId === FIXTURE_ENVS.staging.id ? "staging" : null;
  if (!key) return [];
  const state = data.envState[key];
  if (state === "none" || state === "pending" || (state === "no-sbom" && data.envState.prod !== "scanned")) return [];
  return data.findings
    .filter((f) => f.environmentIds.includes(envId))
    .map((f) => ({ ...f, environmentIds: [envId], exemption: f.exemption?.environmentId === envId ? f.exemption : null }));
}

function envSummary(data: ScenarioData, key: "prod" | "staging" | "dev"): EnvVulnSummary {
  const env = FIXTURE_ENVS[key];
  const state: ScanState = key === "dev" ? "none" : data.envState[key];
  if (state === "none") {
    return {
      environmentId: env.id,
      environmentName: env.name,
      releaseId: "",
      releaseVersion: "",
      counts: { critical: 0, high: 0, medium: 0, low: 0, negligible: 0, unknown: 0 },
      kev: 0,
      fixable: 0,
      blocking: 0,
      exempt: 0,
      coverage: { artifacts: 0, withSbom: 0, scanned: 0 },
      lastScannedMs: null,
      scanState: "none",
      failingSinceMs: null,
    };
  }
  const findings = envFindings(data, env.id);
  const live = findings.filter((f) => !f.exemption);
  const coverage =
    state === "no-sbom"
      ? data.envState.prod === "scanned"
        ? { artifacts: 3, withSbom: 2, scanned: 2 }
        : { artifacts: 3, withSbom: 0, scanned: 0 }
      : state === "pending"
        ? { artifacts: 3, withSbom: 3, scanned: 0 }
        : { artifacts: 3, withSbom: 3, scanned: 3 };
  return {
    environmentId: env.id,
    environmentName: env.name,
    releaseId: env.releaseId,
    releaseVersion: env.release,
    counts: countsOf(findings),
    kev: live.filter((f) => f.advisory.kev).length,
    fixable: live.filter((f) => f.fixState === "fixed").length,
    blocking: live.filter((f) => f.blocking).length,
    exempt: findings.length - live.length,
    coverage,
    lastScannedMs: state === "pending" || coverage.scanned === 0 ? null : state === "failed" ? FIXTURE_NOW - 3 * DAY : FIXTURE_NOW - 2 * 3_600_000,
    scanState: state,
    failingSinceMs: state === "failed" ? FIXTURE_NOW - 2 * DAY : null,
  };
}

function releaseFor(version: string): { key: "prod" | "staging"; releaseId: string } | null {
  if (version === FIXTURE_ENVS.prod.release) return { key: "prod", releaseId: FIXTURE_ENVS.prod.releaseId };
  if (version === FIXTURE_ENVS.staging.release) return { key: "staging", releaseId: FIXTURE_ENVS.staging.releaseId };
  return null;
}

/** A fixture's reads, each answering asynchronously as the server does, and every call it was asked. */
export type FixtureVulnerabilityApi = {
  [K in keyof VulnerabilityReads]: (req: Parameters<VulnerabilityReads[K]>[0]) => Promise<Awaited<ReturnType<VulnerabilityReads[K]>>>;
} & { calls: { method: string; req: unknown }[] };

/** An in-memory VulnerabilityService for `scenario`. Calls are recorded in `calls`, for tests. */
export function fixtureVulnerabilityApi(scenario: VulnScenario = "findings"): FixtureVulnerabilityApi {
  const data = scenarioData(scenario);
  const calls: { method: string; req: unknown }[] = [];
  const fail = () => (data.error ? Promise.reject(data.error) : null);
  return {
    calls,
    getVulnerabilitySummary: async (req) => {
      calls.push({ method: "getVulnerabilitySummary", req });
      await fail();
      const envs = (["prod", "staging", "dev"] as const).map((key) => envSummary(data, key));
      return { environments: req.environmentId ? envs.filter((e) => e.environmentId === req.environmentId) : envs, database: data.database };
    },
    listVulnerabilities: async (req) => {
      calls.push({ method: "listVulnerabilities", req });
      await fail();
      const { filters } = req;
      let rows: Finding[];
      if (filters.releaseVersion) {
        const rel = releaseFor(filters.releaseVersion);
        if (!rel) throw new ConnectError("release not found", Code.NotFound);
        rows = envFindings(data, FIXTURE_ENVS[rel.key].id);
      } else if (filters.environmentId) {
        rows = envFindings(data, filters.environmentId);
      } else {
        // Every environment's current release, merged: one row per (advisory, package).
        const merged = new Map<string, Finding>();
        for (const env of [FIXTURE_ENVS.prod, FIXTURE_ENVS.staging]) {
          for (const f of envFindings(data, env.id)) {
            const key = `${f.advisory.id}|${f.package.purl}`;
            const prev = merged.get(key);
            merged.set(
              key,
              prev
                ? {
                    ...prev,
                    environmentIds: [...prev.environmentIds, env.id],
                    blocking: prev.blocking || f.blocking,
                    // Exempt only when every environment containing it exempts it.
                    exemption: prev.exemption && f.exemption ? prev.exemption : null,
                    firstSeenMs: Math.min(prev.firstSeenMs ?? Infinity, f.firstSeenMs ?? Infinity),
                  }
                : f
            );
          }
        }
        rows = [...merged.values()];
      }
      const kept = rows.filter((f) => passes(f, filters)).sort(byServerOrder);
      const start = req.pageToken ? Number(req.pageToken) : 0;
      const size = req.pageSize || 50;
      const page = kept.slice(start, start + size);
      return { findings: page, nextPageToken: start + size < kept.length ? String(start + size) : "", database: data.database };
    },
    getVulnerability: async (req): Promise<VulnerabilityDetail> => {
      calls.push({ method: "getVulnerability", req });
      await fail();
      const id = req.advisoryId;
      const found = data.findings.filter((f) => f.advisory.id === id || f.advisory.aliases.includes(id));
      if (found.length === 0) throw new ConnectError("advisory not found", Code.NotFound);
      const f = found[0]!;
      const envs = f.environmentIds;
      return {
        advisory: {
          ...f.advisory,
          references: [`https://nvd.nist.gov/vuln/detail/${f.advisory.id}`, `https://github.com/advisories/${f.advisory.aliases[0] ?? f.advisory.id}`],
        },
        occurrences: envs.flatMap((envId) => {
          const env = envId === FIXTURE_ENVS.prod.id ? FIXTURE_ENVS.prod : FIXTURE_ENVS.staging;
          return f.artifactNames.map((artifactName) => ({
            releaseId: env.releaseId,
            releaseVersion: env.release,
            artifactName,
            sbomDigest: `sha256:${"cd".repeat(32)}`,
            package: f.package,
            fixedVersions: f.fixedVersions,
            fixState: f.fixState,
            environmentIds: [envId],
          }));
        }),
        observations: envs.map((envId) => ({
          environmentId: envId,
          purl: f.package.purl,
          firstSeenMs: f.firstSeenMs,
          lastSeenMs: FIXTURE_NOW - 2 * 3_600_000,
          resolvedMs: null,
          firstReleaseVersion: envId === FIXTURE_ENVS.prod.id ? "v1.3.0" : FIXTURE_ENVS.staging.release,
        })),
        exemptions: f.exemption ? [f.exemption] : [],
        gateVerdicts: envs.map((envId) => ({
          promotionId: `promo-${envId}`,
          environmentId: envId,
          releaseVersion: envId === FIXTURE_ENVS.prod.id ? FIXTURE_ENVS.prod.release : FIXTURE_ENVS.staging.release,
          mode: envId === FIXTURE_ENVS.prod.id ? "enforce" : "audit",
          verdict: f.exemption ? "pass" : f.blocking ? (envId === FIXTURE_ENVS.prod.id ? "block" : "warn") : "pass",
          overrideReason: "",
          createdMs: FIXTURE_NOW - 2 * DAY,
        })),
      };
    },
    getReleaseVulnerabilities: async (req): Promise<ReleaseVulnerabilities> => {
      calls.push({ method: "getReleaseVulnerabilities", req });
      await fail();
      const rel = releaseFor(req.version);
      if (!rel) throw new ConnectError("release not found", Code.NotFound);
      const state = data.envState[rel.key];
      const findings = envFindings(data, FIXTURE_ENVS[rel.key].id);
      const live = findings.filter((f) => !f.exemption);
      const artifacts = artifactsOf(req.version, state === "none" ? "no-sbom" : state, FIXTURE_SCAN_ERROR);
      const withSbom = artifacts.filter((a) => a.sbomDigest).length;
      const scanned = artifacts.filter((a) => a.scanState === "scanned" || a.scanState === "stale").length;
      const size = req.pageSize || 500;
      return {
        releaseId: rel.releaseId,
        releaseVersion: req.version,
        artifacts,
        coverage: { artifacts: artifacts.length, withSbom, scanned },
        counts: countsOf(findings),
        kev: live.filter((f) => f.advisory.kev).length,
        fixable: live.filter((f) => f.fixState === "fixed").length,
        blocking: live.filter((f) => f.blocking).length,
        exempt: findings.length - live.length,
        findings: [...findings].sort(byServerOrder).slice(0, size),
        nextPageToken: findings.length > size ? String(size) : "",
        database: data.database,
      };
    },
  };
}
