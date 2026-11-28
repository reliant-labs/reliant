// Copyright (c) 2026 Reliant Labs

/**
 * The demo's Security and Tests answers (model/security.ts, model/tests.ts,
 * served on the wire by handlers/security.ts, tests.ts and coverageSource.ts)
 * are held to the one story, as coherence.test.ts holds every other page:
 * every release, commit, author, environment and artifact they name is
 * shopStory's, each environment's findings are its running release's, and
 * nothing happens after the page load. The coverage view's source is
 * answered from the bundle, never fetched.
 *
 * Every answer is asked the way the pages ask it: through their real clients
 * (controlPlaneVulnerabilityApi, the Tests pages' generated TestsService reads,
 * fetchCoverageSource) over the shop pack's transport, read-only guard
 * included.
 */

import type { DescMethod } from "@bufbuild/protobuf";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { createClient, type Transport } from "@connectrpc/connect";
import { QueryClient } from "@tanstack/react-query";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { TestsService } from "@/gen/controlplane/services/tests/v1/tests_pb";
import { VulnerabilityService } from "@/gen/controlplane/services/vulnerability/v1/vulnerability_pb";
import { CoverageSourceService } from "@/gen/reliant/v1/coverage_source_pb";
import { setTransport } from "@/lib/connect";
import { testsServiceQueries } from "@/hooks/controlplane/tests-service-hooks_gen";
import { onControlPlane } from "@/hooks/test-queries";
import type { CoverageRequest, FileCoverageRequest, FlakyRequest, ReleaseTestsRequest, RunRequest, RunsRequest, SummaryRequest, TestRequest } from "@/services/controlPlane/tests/types";
import {
  coverageRequest,
  coverageTreeOf,
  fileCoverageOf,
  fileCoverageRequest,
  flakePageOf,
  flakyRequest,
  releaseTestsOf,
  releaseTestsRequest,
  runDetailOf,
  runPageOf,
  runRequest,
  runsRequest,
  summaryOf,
  summaryRequest,
  testDetailOf,
  testRequest,
} from "@/services/controlPlane/tests/wire";
import { controlPlaneVulnerabilityApi } from "@/services/controlPlane/vulnerabilities/VulnerabilityApiContext";
import { fetchCoverageSource, gitBlobId, sourceLineCount, verifySource } from "@/services/coverageSource/coverageSource";

import { createShopTransport } from "..";
import { COVERAGE_SOURCE_RPC } from "../handlers/coverageSource";
import { SECURITY_RPCS } from "../handlers/security";
import { TESTS_RPCS } from "../handlers/tests";
import { SHOP_ADVISORIES } from "../model/security";
import { SHOP_BRANCH_COMMIT, SHOP_CHARGE_BLOB, SHOP_CHARGE_PATH, SHOP_CHARGE_SOURCE, SHOP_FIX_BRANCH, SHOP_TESTS } from "../model/tests";
import { DEMO_CAST, DEMO_CI_ACTOR, DEMO_ENV_IDS, DEMO_RELEASES, DEMO_SERVICE_NAMES, demoLedger, resetDemoNowForTests, RUNNING } from "../shopStory";
import { isDeclaredRead } from "./reads";

const NOW = Date.UTC(2026, 10, 10, 15, 0, 0);
const req = { orgId: null, project: "shop" };
const VERSIONS = DEMO_RELEASES.map((r) => r.version);
const COMMITS = new Set<string>(DEMO_RELEASES.flatMap((r) => r.commits.map((c) => c.sha)));
const commitOf = (version: string) => DEMO_RELEASES.find((r) => r.version === version)!.commit;
const ACTORS = new Set<string>([...DEMO_CAST.map((p) => p.handle), DEMO_CI_ACTOR]);

/**
 * The Tests pages' reads, asked as hooks/test-queries.ts asks them: the
 * generated query options, fetched on a QueryClient outside any scope (so
 * over the app's transport), each answer mapped as the page's `select` maps it.
 */
const testsClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 0, gcTime: 0 } } });
const testsApi = {
  getTestSummary: async (r: SummaryRequest) => summaryOf(await testsClient.fetchQuery(onControlPlane(testsServiceQueries.getTestSummary(summaryRequest(r))))),
  listTestRuns: async (r: RunsRequest) => runPageOf(await testsClient.fetchQuery(onControlPlane(testsServiceQueries.listTestRuns(runsRequest(r))))),
  getTestRun: async (r: RunRequest) => runDetailOf(await testsClient.fetchQuery(onControlPlane(testsServiceQueries.getTestRun(runRequest(r))))),
  getCoverage: async (r: CoverageRequest) => coverageTreeOf(await testsClient.fetchQuery(onControlPlane(testsServiceQueries.getCoverage(coverageRequest(r))))),
  getFileCoverage: async (r: FileCoverageRequest) => fileCoverageOf(await testsClient.fetchQuery(onControlPlane(testsServiceQueries.getFileCoverage(fileCoverageRequest(r))))),
  getReleaseTests: async (r: ReleaseTestsRequest) => releaseTestsOf(await testsClient.fetchQuery(onControlPlane(testsServiceQueries.getReleaseTests(releaseTestsRequest(r))))),
  listFlakyTests: async (r: FlakyRequest) => flakePageOf(await testsClient.fetchQuery(onControlPlane(testsServiceQueries.listFlakyTests(flakyRequest(r))))),
  getTest: async (r: TestRequest) => testDetailOf(await testsClient.fetchQuery(onControlPlane(testsServiceQueries.getTest(testRequest(r))))),
};

/** Every `…Ms` number and every timestamp in `value`, except an exemption's or a quarantine's expiry (meant to lie ahead). */
function times(value: unknown, out: { path: string; ms: number }[] = [], path = ""): { path: string; ms: number }[] {
  if (Array.isArray(value)) value.forEach((item, i) => times(item, out, `${path}[${i}]`));
  else if (value && typeof value === "object") {
    for (const [key, item] of Object.entries(value)) {
      if (key.endsWith("Ms") && !key.startsWith("expires") && key !== "durationMs" && typeof item === "number") out.push({ path: `${path}.${key}`, ms: item });
      else if (key !== "quarantine") times(item, out, `${path}.${key}`);
    }
  }
  return out;
}

/** The shop pack's transport, as the demo serves it; every client in this file reaches it. */
let transport: Transport;
/** Every "pkg.Service/Method" the clients asked the transport for. */
const asked = new Set<string>();
let restore = () => undefined as void;

beforeAll(() => {
  resetDemoNowForTests(NOW);
  transport = createShopTransport();
  const recording: Transport = {
    unary: (method, ...rest) => {
      asked.add(`${method.parent.typeName}/${method.name}`);
      return transport.unary(method, ...rest);
    },
    stream: (method, ...rest) => {
      asked.add(`${method.parent.typeName}/${method.name}`);
      return transport.stream(method, ...rest);
    },
  };
  restore = setTransport(recording);
});
afterAll(() => {
  restore();
  resetDemoNowForTests();
});

describe("the demo's Security answers", () => {
  const api = () => controlPlaneVulnerabilityApi;

  it("scans what each environment runs: shopStory's release, every artifact with an SBOM, dev included", async () => {
    const summary = await api().getVulnerabilitySummary(req);
    expect(summary.environments.map((e) => e.environmentId).sort()).toEqual([...DEMO_ENV_IDS].sort());
    for (const env of summary.environments) {
      const running = RUNNING[env.environmentId as (typeof DEMO_ENV_IDS)[number]].version;
      expect(env.releaseVersion, env.environmentName).toBe(running);
      expect(env.releaseId).toBe(`rel-${running}`);
      expect(env.scanState, env.environmentName).toBe("scanned");
      expect(env.coverage).toEqual({ artifacts: DEMO_SERVICE_NAMES.length, withSbom: DEMO_SERVICE_NAMES.length, scanned: DEMO_SERVICE_NAMES.length });
      expect(env.lastScannedMs).not.toBeNull();
    }
    const by = Object.fromEntries(summary.environments.map((e) => [e.environmentName, e]));
    // prod enforces at critical (nothing blocks: v1.8.3's promotion passed); staging audits at high; dev has no gate.
    expect(by.prod!.blocking).toBe(0);
    expect(by.staging!.blocking).toBe(2);
    expect(by.dev!.blocking).toBe(0);
    expect([by.prod!.exempt, by.staging!.exempt, by.dev!.exempt]).toEqual([1, 1, 0]);
    expect(summary.database?.stale).toBe(false);
  });

  it("answers every release's SBOMs, naming the artifacts and image digests the release itself names", async () => {
    const releases = (await createClient(DeployService, transport).listReleases({ project: "shop" })).releases;
    for (const version of VERSIONS) {
      const rel = await api().getReleaseVulnerabilities({ ...req, version });
      expect(rel.releaseId).toBe(`rel-${version}`);
      expect(rel.artifacts.map((a) => a.artifactName)).toEqual([...DEMO_SERVICE_NAMES]);
      const deployed = releases.find((r) => r.version === version)!;
      for (const a of rel.artifacts) {
        expect(a.scanState).toBe("scanned");
        if (a.source === "lockfile") continue;
        expect(a.referrer, `${version} ${a.artifactName}`).toContain(deployed.artifacts.find((d) => d.name === a.artifactName)!.digest);
      }
      expect(rel.artifacts.find((a) => a.artifactName === "shop-web")!.source).toBe("lockfile");
    }
    await expect(api().getReleaseVulnerabilities({ ...req, version: "v9.9.9" })).rejects.toMatchObject({ code: 5 });
  });

  it("lists each environment's findings from its running release, and every finding names shop's artifacts and environments", async () => {
    for (const envId of DEMO_ENV_IDS) {
      const page = await api().listVulnerabilities({ ...req, filters: { environmentId: envId, includeExempt: true } });
      const release = await api().getReleaseVulnerabilities({ ...req, version: RUNNING[envId].version, environmentId: envId });
      expect(page.findings.map((f) => f.advisory.id)).toEqual(release.findings.map((f) => f.advisory.id));
      for (const f of page.findings) {
        expect(f.environmentIds).toEqual([envId]);
        for (const a of f.artifactNames) expect(DEMO_SERVICE_NAMES as readonly string[]).toContain(a);
      }
    }
    const all = await api().listVulnerabilities({ ...req, filters: {} });
    expect(all.findings.length).toBeGreaterThanOrEqual(3);
    // The exempt one is left out until asked for, everywhere both prod and staging exempt it and dev does not.
    const exempt = SHOP_ADVISORIES.find((a) => a.exempt)!.advisory.id;
    expect(all.findings.find((f) => f.advisory.id === exempt)?.environmentIds).toEqual(["env-prod", "env-staging", "env-dev"]);
    expect((await api().listVulnerabilities({ ...req, filters: { environmentId: "env-prod" } })).findings.some((f) => f.advisory.id === exempt)).toBe(false);
  });

  it("resolves connect-go's advisory with v1.8.3's bump, in each environment as v1.8.3 reached it", async () => {
    const connect = SHOP_ADVISORIES.find((a) => a.fixedVersions.includes("v1.19.0"))!.advisory.id;
    const byVersion = async (version: string) => (await api().getReleaseVulnerabilities({ ...req, version })).findings.map((f) => f.advisory.id);
    expect(await byVersion("v1.8.2")).toContain(connect);
    expect(await byVersion("v1.8.3")).not.toContain(connect);
    const detail = await api().getVulnerability({ ...req, advisoryId: connect });
    const ledger = demoLedger(NOW);
    for (const o of detail.observations) {
      const reached = ledger.find((p) => p.envId === o.environmentId && p.version === "v1.8.3" && !p.targets.length)!;
      expect(o.resolvedMs, o.environmentId).toBe(reached.atMs);
    }
    expect(detail.observations.length).toBe(3);
  });

  it("puts gate verdicts on the ledger's promotions only, and prod's enforced gate passes what it runs", async () => {
    const ledger = new Map(demoLedger(NOW).map((p) => [p.promotionId, p]));
    for (const a of SHOP_ADVISORIES) {
      const detail = await api().getVulnerability({ ...req, advisoryId: a.advisory.aliases[0] ?? a.advisory.id });
      expect(detail.advisory.id).toBe(a.advisory.id);
      for (const v of detail.gateVerdicts) {
        const p = ledger.get(v.promotionId);
        expect(p, v.promotionId).toBeDefined();
        expect(v.releaseVersion).toBe(p!.version);
        expect(v.environmentId).not.toBe("env-dev");
        if (v.environmentId === "env-prod") expect([v.mode, v.verdict]).toEqual(["enforce", "pass"]);
      }
      for (const o of detail.occurrences) expect(VERSIONS).toContain(o.releaseVersion);
    }
  });

  it("puts nothing after the page load", async () => {
    const answers = [
      await api().getVulnerabilitySummary(req),
      await api().listVulnerabilities({ ...req, filters: { includeExempt: true } }),
      ...(await Promise.all(VERSIONS.map((version) => api().getReleaseVulnerabilities({ ...req, version })))),
      ...(await Promise.all(SHOP_ADVISORIES.map((a) => api().getVulnerability({ ...req, advisoryId: a.advisory.id })))),
    ];
    const all = times(answers);
    expect(all.length).toBeGreaterThan(30);
    expect(all.filter((t) => t.ms > NOW)).toEqual([]);
  });
});

describe("the demo's Tests answers", () => {
  const api = () => testsApi;

  it("summarises main at v1.8.4's commit, with every release on the trend", async () => {
    const summary = await api().getTestSummary(req);
    expect(summary.latest?.gitCommit).toBe(commitOf("v1.8.4"));
    expect(summary.lastRun?.gitCommit).toBe(commitOf("v1.8.4"));
    for (const p of summary.trend) expect(COMMITS.has(p.gitCommit), p.gitCommit).toBe(true);
    expect(summary.releaseMarkers.map((m) => [m.releaseVersion, m.gitCommit])).toEqual([...DEMO_RELEASES].reverse().map((r) => [r.version, r.commit]));
    for (const m of summary.releaseMarkers) expect(summary.trend.some((p) => p.gitCommit === m.gitCommit), m.releaseVersion).toBe(true);
    expect([summary.flaky, summary.quarantined, summary.suspected]).toEqual([1, 1, 1]);
    expect(summary.passRate).toBe(1);
  });

  it("records runs on shopStory's commits only, by CI or the cast, with one failed: the fix branch's new test", async () => {
    const runs = [];
    let token = "";
    do {
      const page = await api().listTestRuns({ ...req, filters: {}, pageToken: token || undefined });
      runs.push(...page.runs);
      token = page.nextPageToken;
    } while (token);
    expect(runs.length).toBeGreaterThan(10);
    expect(new Set(runs.map((r) => r.runId)).size).toBe(runs.length);
    for (const r of runs) {
      expect(r.runId).toMatch(/^[0-9A-HJKMNP-TV-Z]{26}$/);
      expect(COMMITS.has(r.gitCommit), r.gitCommit).toBe(true);
      expect(ACTORS.has(r.uploadedBy), r.uploadedBy).toBe(true);
      if (r.environmentId) expect(DEMO_ENV_IDS as readonly string[]).toContain(r.environmentId);
      const detail = await api().getTestRun({ ...req, runId: r.runId });
      expect(detail.run?.runId).toBe(r.runId);
    }
    const failed = runs.filter((r) => r.outcome === "failed");
    expect(failed.map((r) => [r.branch, r.gitCommit])).toEqual([[SHOP_FIX_BRANCH, SHOP_BRANCH_COMMIT]]);
    expect(DEMO_RELEASES[0].commits.find((c) => c.sha === SHOP_BRANCH_COMMIT)?.subject).toMatch(/^test\(orders\): CreateOrder survives a slow payment provider/);
    const detail = await api().getTestRun({ ...req, runId: failed[0]!.runId });
    expect(detail.failures.map((f) => [f.test.name, f.verdict])).toEqual([[SHOP_TESTS.slowProvider.name, "real"]]);
    // A release's runs are its commit's.
    for (const version of VERSIONS) {
      const page = await api().listTestRuns({ ...req, filters: { releaseVersion: version } });
      expect(page.runs.length, version).toBeGreaterThan(0);
      expect(page.runs.every((r) => r.gitCommit === commitOf(version))).toBe(true);
    }
    await expect(api().getTestRun({ ...req, runId: "01NOTARUN" })).rejects.toMatchObject({ code: 5 });
  });

  it("caught v1.8.3's retry test as a flake, and answers each flaky test's page", async () => {
    const flaky = await api().listFlakyTests({ ...req, filters: {} });
    expect(flaky.tests[0]?.test.name).toBe(SHOP_TESTS.retryOnTimeout.name);
    expect(flaky.tests[0]?.state).toBe("flaky");
    const v183 = await api().getReleaseTests({ ...req, version: "v1.8.3" });
    expect(v183.newFlakes.map((f) => f.test.name)).toEqual([SHOP_TESTS.retryOnTimeout.name]);
    for (const f of flaky.tests) {
      const detail = await api().getTest({ ...req, testId: f.test.id });
      expect(detail.flake?.test.id).toBe(f.test.id);
      expect(detail.events.length).toBeGreaterThan(0);
      for (const e of detail.events) expect(COMMITS.has(e.gitCommit), e.gitCommit).toBe(true);
      for (const o of detail.outcomes) expect(COMMITS.has(o.gitCommit), o.gitCommit).toBe(true);
    }
    expect((await api().listFlakyTests({ ...req, filters: { state: "quarantined" } })).tests.map((t) => t.test.name)).toEqual([SHOP_TESTS.redelivery.name]);
  });

  it("gives every release a report on its own commit, compared with the release before it", async () => {
    for (const [i, r] of DEMO_RELEASES.entries()) {
      const tests = await api().getReleaseTests({ ...req, version: r.version });
      expect(tests.status, r.version).toBe("ok");
      expect(tests.gitCommit).toBe(r.commit);
      expect(tests.report?.gitCommit).toBe(r.commit);
      expect(tests.previousReleaseVersion).toBe(DEMO_RELEASES[i + 1]?.version ?? "");
    }
  });

  it("resolves coverage using the entire supplied commit prefix", async () => {
    const sha = commitOf("v1.8.4");
    for (let length = 7; length <= sha.length; length++) {
      const target = { gitCommit: sha.slice(0, length) };
      expect((await api().getCoverage({ ...req, target, path: "" })).gitCommit).toBe(sha);
      expect((await api().getFileCoverage({ ...req, target, path: SHOP_CHARGE_PATH })).blobId).toBe(SHOP_CHARGE_BLOB);
    }
    for (const gitCommit of [sha.slice(0, 6), `${sha.slice(0, 12)}invalid`, `${sha}0`]) {
      await expect(api().getCoverage({ ...req, target: { gitCommit }, path: "" })).rejects.toMatchObject({ code: 5 });
    }
  });

  it("browses coverage by commit, release and environment, and serves charge.go's source from the bundle", async () => {
    const root = await api().getCoverage({ ...req, target: { gitCommit: commitOf("v1.8.4") }, path: "" });
    expect(root.children.map((c) => c.path)).toEqual(expect.arrayContaining(["internal/orders", "internal/checkout", "internal/inventory", "cmd/worker", "web/src/checkout"]));
    const prod = await api().getCoverage({ ...req, target: { environmentId: "env-prod" }, path: "" });
    expect(prod.gitCommit).toBe(commitOf(RUNNING["env-prod"].version));
    for (const p of (await api().getTestSummary(req)).trend) await expect(api().getCoverage({ ...req, target: { gitCommit: p.gitCommit }, path: "internal/orders" })).resolves.toBeDefined();
    // v1.8.4's fix against v1.8.3: every changed line of charge.go covered.
    const orders = await api().getCoverage({ ...req, target: { gitCommit: commitOf("v1.8.4") }, path: "internal/orders", baseSha: commitOf("v1.8.3") });
    const charge = orders.children.find((c) => c.path === SHOP_CHARGE_PATH)!;
    expect(charge.changed).toBe(true);
    expect(charge.diff?.percent).toBe(100);

    const file = await api().getFileCoverage({ ...req, target: { gitCommit: commitOf("v1.8.4") }, path: SHOP_CHARGE_PATH });
    expect(file.blobId).toBe(SHOP_CHARGE_BLOB);
    expect(gitBlobId(SHOP_CHARGE_SOURCE)).toBe(SHOP_CHARGE_BLOB);
    const lastLine = Math.max(...file.lines.map((l) => l.line));
    expect(lastLine).toBeLessThanOrEqual(sourceLineCount(SHOP_CHARGE_SOURCE));
    expect(verifySource(await fetchCoverageSource("shop", file.blobId), file.blobId, lastLine)).toEqual({ text: SHOP_CHARGE_SOURCE });
  });

  it("puts nothing after the page load and names nothing from the Tests pages' own fixtures", async () => {
    const t = api();
    const runs = (await t.listTestRuns({ ...req, filters: {}, pageSize: 100 })).runs;
    const answers = [
      await t.getTestSummary(req),
      runs,
      ...(await Promise.all(runs.map((r) => t.getTestRun({ ...req, runId: r.runId })))),
      await t.listFlakyTests({ ...req, filters: {} }),
      ...(await Promise.all(Object.values(SHOP_TESTS).map((k) => t.getTest({ ...req, testId: k.id })))),
      ...(await Promise.all(VERSIONS.map((version) => t.getReleaseTests({ ...req, version })))),
    ];
    const all = times(answers);
    expect(all.length).toBeGreaterThan(50);
    expect(all.filter((x) => x.ms > NOW)).toEqual([]);
    expect(JSON.stringify(answers)).not.toMatch(/shopfront|acme\.dev/);
  });
});

describe("the demo's coverage source", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("never makes a request", async () => {
    const fetch = vi.fn(() => Promise.reject(new Error("no request may leave the demo")));
    vi.stubGlobal("fetch", fetch);
    expect(await fetchCoverageSource("shop", SHOP_CHARGE_BLOB)).toEqual({ text: SHOP_CHARGE_SOURCE });
    expect(await fetchCoverageSource("shop", "0".repeat(40))).toEqual({ unavailable: "not-yet-available" });
    expect(await fetchCoverageSource("shop", "")).toEqual({ unavailable: "uncommitted" });
    expect(fetch).not.toHaveBeenCalled();
  });
});

describe("the demo transport's Security and Tests RPCs", () => {
  /** Each client method the pages call, asked once over the transport with a request it answers. */
  const calls: Record<string, () => Promise<unknown>> = {
    getVulnerabilitySummary: () => controlPlaneVulnerabilityApi.getVulnerabilitySummary(req),
    listVulnerabilities: () => controlPlaneVulnerabilityApi.listVulnerabilities({ ...req, filters: {} }),
    getVulnerability: () => controlPlaneVulnerabilityApi.getVulnerability({ ...req, advisoryId: SHOP_ADVISORIES[0]!.advisory.id }),
    getReleaseVulnerabilities: () => controlPlaneVulnerabilityApi.getReleaseVulnerabilities({ ...req, version: VERSIONS[0]! }),
    getTestSummary: () => testsApi.getTestSummary(req),
    listTestRuns: () => testsApi.listTestRuns({ ...req, filters: {} }),
    getTestRun: async () => testsApi.getTestRun({ ...req, runId: (await testsApi.listTestRuns({ ...req, filters: {} })).runs[0]!.runId }),
    getCoverage: () => testsApi.getCoverage({ ...req, target: { gitCommit: commitOf("v1.8.4") }, path: "" }),
    getFileCoverage: () => testsApi.getFileCoverage({ ...req, target: { gitCommit: commitOf("v1.8.4") }, path: SHOP_CHARGE_PATH }),
    getReleaseTests: () => testsApi.getReleaseTests({ ...req, version: "v1.8.4" }),
    listFlakyTests: () => testsApi.listFlakyTests({ ...req, filters: {} }),
    getTest: () => testsApi.getTest({ ...req, testId: SHOP_TESTS.retryOnTimeout.id }),
    fetchCoverageSource: () => fetchCoverageSource("shop", SHOP_CHARGE_BLOB),
  };

  it("serves every read the Security and Tests pages' clients make, and nothing else of theirs", () => {
    // A client method added without a handler fails here, not as a "Sample data isn't available" page.
    expect(Object.keys(controlPlaneVulnerabilityApi).sort()).toEqual(Object.keys(SECURITY_RPCS).sort());
    // Every generated TestsService read is a page's, but the ingest's HasCoverageBlob (CI asks it); the paged reads are their lists'.
    const pageReads = Object.keys(testsServiceQueries).filter((method) => !method.endsWith("Infinite") && method !== "hasCoverageBlob");
    expect(pageReads.sort()).toEqual(Object.keys(TESTS_RPCS).sort());
    expect(Object.keys(testsApi).sort()).toEqual(Object.keys(TESTS_RPCS).sort());
    expect(Object.keys(calls).sort()).toEqual([...Object.keys(SECURITY_RPCS), ...Object.keys(TESTS_RPCS), "fetchCoverageSource"].sort());
  });

  it("answers each of them over the transport, past the demo's read-only guard", async () => {
    asked.clear();
    for (const [name, call] of Object.entries(calls)) await expect(call(), name).resolves.toBeDefined();
    expect([...asked].sort()).toEqual([...Object.values(SECURITY_RPCS), ...Object.values(TESTS_RPCS), COVERAGE_SOURCE_RPC].sort());
    // And their protos declare them reads (NO_SIDE_EFFECTS), so no write slipped through as one.
    const methods = new Map<string, DescMethod>(
      [VulnerabilityService, TestsService, CoverageSourceService].flatMap((s) => s.methods.map((m): [string, DescMethod] => [`${s.typeName}/${m.name}`, m]))
    );
    for (const rpc of asked) expect(isDeclaredRead(methods.get(rpc)!), rpc).toBe(true);
  });

  it("refuses their writes", async () => {
    await expect(createClient(VulnerabilityService, transport).recordReleaseSBOM({ project: "shop" })).rejects.toMatchObject({ code: 7 });
    await expect(createClient(TestsService, transport).recordTestRun({ project: "shop" })).rejects.toMatchObject({ code: 7 });
  });
});
