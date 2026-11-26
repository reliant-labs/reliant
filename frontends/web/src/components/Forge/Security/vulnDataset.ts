// Copyright (c) 2026 Reliant Labs

/**
 * VulnerabilityService's four reads over an in-memory RELEASE-scoped dataset,
 * answered the way control-plane's server does (its proto header is the
 * spec), as the plain types of services/controlPlane/vulnerabilities/types.ts.
 * A fixture pack's transport handler decodes a request into them and encodes
 * the answer back onto the wire (fixtures/shop/handlers/security.ts), so the
 * pages run their real client over it.
 *
 * A finding belongs to a release, because it is a package in one of
 * the release's artifact SBOMs; an environment adds only its current release,
 * its unexpired exemptions, its vuln_gate policy and its promotion ledger,
 * from which first-seen/resolved times and gate verdicts follow.
 *
 * fixtures.ts (the Security pages' scenario fixtures) is environment-scoped
 * instead: each of its releases runs in exactly one environment and dev is
 * never promoted. A dataset here may run one release in several
 * environments, keep releases no environment runs (a release page still has
 * SBOMs), and promote anything anywhere — the public demo's shop does all
 * three (fixtures/shop/model/security.ts).
 *
 * The filter, order and count rules are fixtures.ts's own (`passes`,
 * `byServerOrder`, `countsOf`), so the two agree on what a list shows.
 */

import { Code, ConnectError } from "@connectrpc/connect";

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
  VulnDatabase,
  VulnExemption,
  VulnObservation,
  VulnPackage,
  VulnSummary,
  VulnerabilityDetail,
} from "@/services/controlPlane/vulnerabilities/types";
import { SEVERITY_ORDER } from "@/services/controlPlane/vulnerabilities/types";

// ── The rules every fixture VulnerabilityService shares ─────────────────────

export const SEVERITY_RANK: Record<Severity, number> = { unknown: 1, negligible: 2, low: 3, medium: 4, high: 5, critical: 6 };

/** Counts of the findings an exemption does not cover (exempt ones count in `exempt` only). */
export function countsOf(findings: readonly Finding[]): SeverityCounts {
  const counts: SeverityCounts = { critical: 0, high: 0, medium: 0, low: 0, negligible: 0, unknown: 0 };
  for (const f of findings) if (!f.exemption) counts[f.advisory.severity] += 1;
  return counts;
}

/** ListVulnerabilities' filters, as the server applies them: exempt rows only when asked, UNKNOWN passing every minimum. */
export function passes(
  f: Finding,
  filters: { minSeverity?: MinSeverity; fixable?: boolean; kev?: boolean; includeExempt?: boolean }
): boolean {
  if (f.exemption && !filters.includeExempt) return false;
  if (filters.minSeverity && f.advisory.severity !== "unknown" && SEVERITY_RANK[f.advisory.severity] < SEVERITY_RANK[filters.minSeverity]) return false;
  if (filters.fixable !== undefined && filters.fixable !== (f.fixState === "fixed")) return false;
  if (filters.kev !== undefined && filters.kev !== f.advisory.kev) return false;
  return true;
}

/** The server's order: most severe first, KEV-listed first, then by id. */
export function byServerOrder(a: Finding, b: Finding): number {
  return (
    SEVERITY_ORDER.indexOf(a.advisory.severity) - SEVERITY_ORDER.indexOf(b.advisory.severity) ||
    Number(b.advisory.kev) - Number(a.advisory.kev) ||
    a.advisory.id.localeCompare(b.advisory.id)
  );
}

// ── The dataset ─────────────────────────────────────────────────────────────

/** One advisory in one package of a release: which of its artifacts' SBOMs hold the package. */
export interface DatasetFinding {
  advisory: Advisory;
  package: VulnPackage;
  fixedVersions: string[];
  fixState: FixState;
  artifactNames: string[];
  /** When the scanner's database first matched the advisory (a rescan finds it in what was already running). */
  knownSinceMs: number;
}

export interface DatasetRelease {
  releaseId: string;
  version: string;
  /** Every artifact of the release and its SBOM (scanState "no-sbom" when none is recorded). */
  artifacts: ArtifactSbom[];
  findings: DatasetFinding[];
  /** When scans of this release began failing, if its newest scan failed. */
  failingSinceMs?: number | null;
}

/** One promotion of an environment's ledger. `targets` empty: the whole release; else those artifacts only. */
export interface DatasetPromotion {
  promotionId: string;
  releaseVersion: string;
  targets: readonly string[];
  atMs: number;
}

export interface DatasetEnv {
  id: string;
  name: string;
  /** The release of the environment's newest whole-release promotion; "" when nothing was promoted there. */
  releaseVersion: string;
  /** Its vuln_gate (forge's vocabulary: audit | warn | enforce) and the severity it blocks at; null: no gate declared. */
  policy: { mode: "audit" | "warn" | "enforce"; blockAt: MinSeverity } | null;
  /** Its unexpired exemptions (the env's KCL). */
  exemptions: VulnExemption[];
  /** Every promotion, oldest first. */
  promotions: DatasetPromotion[];
}

export interface VulnDataset {
  database: VulnDatabase;
  /** In the order the summary lists them. */
  envs: DatasetEnv[];
  /** Newest first: the order a detail page lists occurrences in. */
  releases: DatasetRelease[];
}

// ── The engine ──────────────────────────────────────────────────────────────

const SCAN_TRUST: ScanState[] = ["none", "no-sbom", "failed", "pending", "stale", "scanned"];

const keyOf = (f: { advisory: Advisory; package: VulnPackage }) => `${f.advisory.id}|${f.package.purl}`;
const names = (f: { advisory: Advisory }, id: string) => f.advisory.id === id || f.advisory.aliases.includes(id);

function blocks(env: DatasetEnv, f: DatasetFinding): boolean {
  if (!env.policy) return false;
  return f.advisory.severity !== "unknown" && SEVERITY_RANK[f.advisory.severity] >= SEVERITY_RANK[env.policy.blockAt];
}

/** VulnerabilityService's reads over `dataset`, request by request; a refusal (NotFound, InvalidArgument) is a thrown ConnectError. */
export interface VulnDatasetEngine {
  getVulnerabilitySummary(req: SummaryRequest): VulnSummary;
  listVulnerabilities(req: ListRequest): FindingPage;
  getVulnerability(req: DetailRequest): VulnerabilityDetail;
  getReleaseVulnerabilities(req: ReleaseRequest): ReleaseVulnerabilities;
}

/** The server's answers over `dataset`. */
export function vulnDatasetEngine(dataset: VulnDataset): VulnDatasetEngine {
  const release = (version: string) => dataset.releases.find((r) => r.version === version) ?? null;
  const envById = (id: string) => dataset.envs.find((e) => e.id === id) ?? null;
  const exemptionIn = (env: DatasetEnv, f: DatasetFinding) => env.exemptions.find((x) => names(f, x.advisoryId)) ?? null;

  /** Whether `version`'s artifact `artifact` holds the finding's package. */
  const holds = (version: string, artifact: string, f: DatasetFinding) =>
    release(version)?.findings.some((g) => keyOf(g) === keyOf(f) && g.artifactNames.includes(artifact)) ?? false;
  /** Whether the env's current release holds it. */
  const runs = (env: DatasetEnv, f: DatasetFinding) => !!env.releaseVersion && !!release(env.releaseVersion)?.findings.some((g) => keyOf(g) === keyOf(f));

  const lastScanOf = (env: DatasetEnv) => {
    const scans = (release(env.releaseVersion)?.artifacts ?? []).map((a) => a.lastScannedMs).filter((ms): ms is number => ms !== null);
    return scans.length ? Math.max(...scans) : null;
  };

  /**
   * The finding's history in one environment, replayed from its ledger: per
   * artifact, which release runs; the package is present while any running
   * artifact holds it, and seen once the scanner knew it.
   */
  function observe(env: DatasetEnv, f: DatasetFinding): VulnObservation | null {
    const running = new Map<string, string>();
    const present: { fromMs: number; toMs: number | null }[] = [];
    for (const p of env.promotions) {
      const rel = release(p.releaseVersion);
      if (!rel) continue;
      for (const a of rel.artifacts) if (!p.targets.length || p.targets.includes(a.artifactName)) running.set(a.artifactName, p.releaseVersion);
      const holdsNow = [...running].some(([artifact, version]) => holds(version, artifact, f));
      const open = present.at(-1)?.toMs === null ? present.at(-1) : undefined;
      if (holdsNow && !open) present.push({ fromMs: p.atMs, toMs: null });
      else if (!holdsNow && open) open.toMs = p.atMs;
    }
    // Only what was still running once the scanner knew the advisory was ever seen; a rescan finds what already ran.
    const seen = present.filter((span) => span.toMs === null || span.toMs > f.knownSinceMs);
    if (!seen.length) return null;
    const firstSeenMs = Math.max(seen[0]!.fromMs, f.knownSinceMs);
    const resolvedMs = seen.at(-1)!.toMs;
    return {
      environmentId: env.id,
      purl: f.package.purl,
      firstSeenMs,
      // Still there: last seen by the newest scan of what the environment runs.
      lastSeenMs: resolvedMs ?? lastScanOf(env),
      resolvedMs,
      // The release the environment had most recently been given when it was first seen.
      firstReleaseVersion: env.promotions.filter((p) => p.atMs <= firstSeenMs).at(-1)?.releaseVersion ?? "",
    };
  }

  /** A release's finding with `env` applied (or none: no exemption, no policy, no first-seen beyond the envs running it). */
  function applied(f: DatasetFinding, env: DatasetEnv | null): Finding {
    const containing = (env ? [env] : dataset.envs).filter((e) => runs(e, f));
    const exemption = env ? exemptionIn(env, f) : null;
    const seen = containing.map((e) => observe(e, f)?.firstSeenMs ?? null).filter((ms): ms is number => ms !== null);
    return {
      advisory: f.advisory,
      package: f.package,
      fixedVersions: f.fixedVersions,
      fixState: f.fixState,
      artifactNames: f.artifactNames,
      environmentIds: containing.map((e) => e.id),
      firstSeenMs: seen.length ? Math.min(...seen) : null,
      exemption,
      blocking: !!env && !exemption && blocks(env, f),
    };
  }

  /** The findings of the env's current release, with the env applied. */
  function envFindings(env: DatasetEnv): Finding[] {
    const rel = env.releaseVersion ? release(env.releaseVersion) : null;
    return rel ? rel.findings.map((f) => applied(f, env)) : [];
  }

  function coverageOf(artifacts: readonly ArtifactSbom[]) {
    return {
      artifacts: artifacts.length,
      withSbom: artifacts.filter((a) => a.sbomDigest).length,
      scanned: artifacts.filter((a) => a.scanState === "scanned" || a.scanState === "stale").length,
    };
  }

  function envSummary(env: DatasetEnv): EnvVulnSummary {
    const rel = env.releaseVersion ? release(env.releaseVersion) : null;
    if (!rel) {
      return {
        environmentId: env.id,
        environmentName: env.name,
        releaseId: "",
        releaseVersion: "",
        counts: countsOf([]),
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
    const findings = envFindings(env);
    const live = findings.filter((f) => !f.exemption);
    const scanState = rel.artifacts.reduce<ScanState>((worst, a) => (SCAN_TRUST.indexOf(a.scanState) < SCAN_TRUST.indexOf(worst) ? a.scanState : worst), "scanned");
    return {
      environmentId: env.id,
      environmentName: env.name,
      releaseId: rel.releaseId,
      releaseVersion: rel.version,
      counts: countsOf(findings),
      kev: live.filter((f) => f.advisory.kev).length,
      fixable: live.filter((f) => f.fixState === "fixed").length,
      blocking: live.filter((f) => f.blocking).length,
      exempt: findings.length - live.length,
      coverage: coverageOf(rel.artifacts),
      lastScannedMs: lastScanOf(env),
      scanState,
      failingSinceMs: scanState === "failed" ? (rel.failingSinceMs ?? null) : null,
    };
  }

  /** What each promotion's vuln_gate decided about `f`, newest first: those after the scanner knew it, whose artifacts hold it. */
  function verdicts(f: DatasetFinding): GateVerdict[] {
    const out: GateVerdict[] = [];
    for (const env of dataset.envs) {
      if (!env.policy) continue;
      for (const p of env.promotions) {
        if (p.atMs < f.knownSinceMs) continue;
        const rel = release(p.releaseVersion);
        const touched = rel?.artifacts.filter((a) => !p.targets.length || p.targets.includes(a.artifactName)).map((a) => a.artifactName) ?? [];
        if (!touched.some((artifact) => holds(p.releaseVersion, artifact, f))) continue;
        const would = !exemptionIn(env, f) && blocks(env, f);
        out.push({
          promotionId: p.promotionId,
          environmentId: env.id,
          releaseVersion: p.releaseVersion,
          mode: env.policy.mode,
          verdict: !would ? "pass" : env.policy.mode === "enforce" ? "block" : "warn",
          overrideReason: "",
          createdMs: p.atMs,
        });
      }
    }
    return out.sort((a, b) => (b.createdMs ?? 0) - (a.createdMs ?? 0));
  }

  const notFound = (what: string) => new ConnectError(`${what} not found`, Code.NotFound);

  return {
    getVulnerabilitySummary: (req) => {
      const envs = dataset.envs.map(envSummary);
      return { environments: req.environmentId ? envs.filter((e) => e.environmentId === req.environmentId) : envs, database: dataset.database };
    },
    listVulnerabilities: (req) => {
      const { filters } = req;
      const env = filters.environmentId ? envById(filters.environmentId) : null;
      let rows: Finding[];
      if (filters.releaseVersion) {
        const rel = release(filters.releaseVersion);
        if (!rel) throw notFound("release");
        rows = rel.findings.map((f) => applied(f, env));
      } else if (filters.environmentId) {
        rows = env ? envFindings(env) : [];
      } else {
        // Every environment's current release, merged: one row per (advisory, package).
        const merged = new Map<string, Finding>();
        for (const e of dataset.envs) {
          for (const f of envFindings(e)) {
            const key = keyOf(f);
            const prev = merged.get(key);
            merged.set(
              key,
              prev
                ? {
                    ...prev,
                    artifactNames: [...new Set([...prev.artifactNames, ...f.artifactNames])],
                    environmentIds: [...prev.environmentIds, ...f.environmentIds],
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
      if (!Number.isInteger(start) || start < 0) throw new ConnectError("page token from another request", Code.InvalidArgument);
      const size = req.pageSize || 50;
      return { findings: kept.slice(start, start + size), nextPageToken: start + size < kept.length ? String(start + size) : "", database: dataset.database };
    },
    getVulnerability: (req) => {
      const id = req.advisoryId;
      const found = dataset.releases.flatMap((rel) => rel.findings.filter((f) => names(f, id)).map((f) => ({ rel, f })));
      if (found.length === 0) throw notFound("advisory");
      const { f: first } = found[0]!;
      const purls = [...new Map(found.map(({ f }) => [keyOf(f), f])).values()];
      return {
        advisory: {
          ...first.advisory,
          references: [`https://nvd.nist.gov/vuln/detail/${first.advisory.id}`, `https://github.com/advisories/${first.advisory.aliases[0] ?? first.advisory.id}`],
        },
        occurrences: found.flatMap(({ rel, f }) =>
          f.artifactNames.map((artifactName) => ({
            releaseId: rel.releaseId,
            releaseVersion: rel.version,
            artifactName,
            sbomDigest: rel.artifacts.find((a) => a.artifactName === artifactName)?.sbomDigest ?? "",
            package: f.package,
            fixedVersions: f.fixedVersions,
            fixState: f.fixState,
            environmentIds: dataset.envs.filter((e) => e.releaseVersion === rel.version).map((e) => e.id),
          }))
        ),
        observations: dataset.envs.flatMap((env) => purls.map((f) => observe(env, f)).filter((o): o is VulnObservation => o !== null)),
        exemptions: dataset.envs.map((env) => exemptionIn(env, first)).filter((x): x is VulnExemption => x !== null),
        gateVerdicts: purls.flatMap(verdicts).sort((a, b) => (b.createdMs ?? 0) - (a.createdMs ?? 0)),
      };
    },
    getReleaseVulnerabilities: (req) => {
      const rel = release(req.version);
      if (!rel) throw notFound("release");
      const env = req.environmentId ? envById(req.environmentId) : null;
      const findings = rel.findings.map((f) => applied(f, env)).sort(byServerOrder);
      const live = findings.filter((f) => !f.exemption);
      const start = req.pageToken ? Number(req.pageToken) : 0;
      const size = req.pageSize || 500;
      return {
        releaseId: rel.releaseId,
        releaseVersion: rel.version,
        artifacts: rel.artifacts,
        coverage: coverageOf(rel.artifacts),
        counts: countsOf(findings),
        kev: live.filter((f) => f.advisory.kev).length,
        fixable: live.filter((f) => f.fixState === "fixed").length,
        blocking: live.filter((f) => f.blocking).length,
        exempt: findings.length - live.length,
        findings: findings.slice(start, start + size),
        nextPageToken: start + size < findings.length ? String(start + size) : "",
        database: dataset.database,
      };
    },
  };
}
