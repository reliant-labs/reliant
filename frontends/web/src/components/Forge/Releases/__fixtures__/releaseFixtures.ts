// Copyright (c) 2025 Reliant Labs

/**
 * An in-memory release ledger for the Releases pages' tests and preview: the
 * story a busy project tells, as the control plane would record it.
 *
 *   prod   … v20261017 → 20261018.052711 → .063708 (smoke failed) → rolled
 *          back to .052711 → .084518 → .155157, then frontends re-shipped as
 *          a scoped deploy of .155157 an hour later.
 *   e2e    … v20261016 → v20261017 → rolled back to v20261016 → .030249 →
 *          .052711 (three behind prod).
 *   dev    every release from .030249 on.
 *
 * .052711 recorded a truncated commit range (a shallow clone), and the two
 * oldest releases predate commit ranges and bundles altogether — the
 * "degraded" answers the Changes tab has to say out loud (truncated, by cut
 * time, unrecorded, bundle missing). Times are relative to module load, so
 * "5h ago" stays true in a screenshot.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type { DoraMetrics, Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import type {
  ArtifactChange,
  ForgeAppliedSlot,
  ObjectDelta,
  ReleaseArtifact,
  ReleaseBundle,
  ReleaseCommit,
  ReleaseComparison,
  ReleaseGate,
  ReleasePromotion,
  ReleaseRecord,
  ReleaseRollout,
} from "@/services/forge/releases";
import type { DeployRun } from "@/services/forge/deployAttempts";
import type { ReleasesApi } from "@/services/forge/ReleasesApiContext";

import type { ReleaseEnv } from "../releaseModel";

const MIN = 60_000;
const HOUR = 60 * MIN;
export const FIXTURE_NOW = Date.now();
const at = (agoMs: number) => new Date(FIXTURE_NOW - agoMs).toISOString();

export const RELEASE_PROJECT = "control-plane";

/** A stable hex string for a seed. */
export function hexOf(seed: string, length = 40): string {
  let h = 2166136261;
  let out = "";
  for (let round = 0; out.length < length; round++) {
    for (const ch of `${seed}#${round}`) h = Math.imul(h ^ ch.charCodeAt(0), 16777619) >>> 0;
    out += h.toString(16).padStart(8, "0");
  }
  return out.slice(0, length);
}

// ── Environments ────────────────────────────────────────────────────────────

const env = (id: string, name: string, kind: ReleaseEnv["kind"], current: string): ReleaseEnv => ({
  id,
  name,
  kind,
  current,
  promotedByActor: "",
  promotedByUserId: "",
});

// ── Releases ────────────────────────────────────────────────────────────────

interface ReleaseSeed {
  version: string;
  subject: string;
  author: string;
  agoMs: number;
  /** Images this release rebuilt (others keep the previous digest). */
  rebuilt: string[];
  commits?: { subjects: string[]; total?: number; truncated?: boolean } | null;
}

const IMAGES = ["control-plane", "workspace-base", "admin-web", "internal-console", "litellm"];

const SEEDS: ReleaseSeed[] = [
  {
    version: "20261018.155157-8aecd00c380b",
    subject: "fix(identity): the reconciler's owner key authenticates; the identity gateway sets x-zitadel-forwarded (#989)",
    author: "sean",
    agoMs: 5 * HOUR,
    rebuilt: ["control-plane", "admin-web", "internal-console"],
    commits: {
      subjects: [
        "fix(identity): the reconciler's owner key authenticates; the identity gateway sets x-zitadel-forwarded (#989)",
        "fix(admin-web): release table keeps its column widths",
        "chore: regenerate after forge bump",
      ],
    },
  },
  {
    version: "20261018.084518-53649dd6416d",
    subject: "feat(identity): identity reconciler goes apply (#987)",
    author: "reliant-01",
    agoMs: 12 * HOUR,
    rebuilt: ["control-plane"],
    commits: { subjects: ["feat(identity): identity reconciler goes apply (#987)", "test(deployconverge): table-drive the promotion pointer cases"] },
  },
  {
    version: "20261018.063708-87603696e13e",
    subject: "fix(deployconverge): no forced Flux reconcile when the hub already carries the promotion (#985)",
    author: "reliant-01",
    agoMs: 14 * HOUR,
    rebuilt: ["control-plane", "workspace-base"],
    commits: { subjects: ["fix(deployconverge): no forced Flux reconcile when the hub already carries the promotion (#985)"] },
  },
  {
    version: "20261018.052711-950b560e58ce",
    subject: "fluxobserve: export reliant.deploy.tree_state and pointer_drift (#984)",
    author: "reliant-01",
    agoMs: 16 * HOUR,
    rebuilt: ["control-plane"],
    commits: {
      subjects: [
        "fluxobserve: export reliant.deploy.tree_state and pointer_drift (#984)",
        "feat(fluxobserve): record convergence per Kustomization",
        "fix(identity): zitadel project grant survives re-provision",
        "refactor(workers): one retry policy for provisioning jobs",
      ],
      total: 0,
      truncated: true,
    },
  },
  {
    version: "20261018.030249-d1ad18266f0f",
    subject: "chore(deps): pin forge 7fb6d653 + reliant a619960e (#981)",
    author: "sean",
    agoMs: 18 * HOUR,
    rebuilt: ["control-plane", "workspace-base", "admin-web", "internal-console", "litellm"],
    commits: { subjects: ["chore(deps): pin forge 7fb6d653 + reliant a619960e (#981)", "ci: record the ci:full summary in the release"] },
  },
  {
    version: "20261017.080240-09d4f21782d1",
    subject: "self-monitoring: nats-exporter, selfmon scrapes, /healthz/alerting (#971)",
    author: "sean",
    agoMs: 38 * HOUR,
    rebuilt: ["control-plane", "litellm"],
    commits: { subjects: ["self-monitoring: nats-exporter, selfmon scrapes, /healthz/alerting (#971)", "fix(selfmon): scrape nats-exporter on its metrics port (#979)"] },
  },
  {
    version: "20261016.191204-5c1e7a0b9d23",
    subject: "",
    author: "",
    agoMs: 50 * HOUR,
    rebuilt: ["control-plane"],
    // Cut by a forge that recorded no commit range, subject or author.
    commits: null,
  },
  {
    version: "20261015.103311-77b0e1d4c2a9",
    subject: "",
    author: "",
    agoMs: 62 * HOUR,
    rebuilt: IMAGES,
    commits: null,
  },
];

function digestFor(name: string, version: string): string {
  return `sha256:${hexOf(`${name}@${version}`, 64)}`;
}

/** Each image's digest in each release: rebuilt ones move, the rest carry over from the release before. */
const ARTIFACTS: Map<string, ReleaseArtifact[]> = (() => {
  const out = new Map<string, ReleaseArtifact[]>();
  const current = new Map<string, string>();
  for (const seed of [...SEEDS].reverse()) {
    for (const name of seed.rebuilt) current.set(name, digestFor(name, seed.version));
    out.set(
      seed.version,
      IMAGES.map((name) => ({
        name,
        kind: name === "admin-web" || name === "internal-console" ? "static" : "image",
        digest: current.get(name) ?? digestFor(name, "base"),
        version: "",
        platforms: name === "admin-web" || name === "internal-console" ? [] : ["linux/amd64", "linux/arm64"],
        uri: `us-docker.pkg.dev/reliant-labs/control-plane/${name}`,
        variant: "",
      }))
    );
  }
  return out;
})();

export const RELEASES: ReleaseRecord[] = SEEDS.map((seed, index) => {
  const sha = hexOf(seed.version);
  const base = SEEDS[index + 1];
  const commits: ReleaseCommit[] = (seed.commits?.subjects ?? []).map((subject, i) => ({
    sha: i === 0 ? sha : hexOf(`${seed.version}/${i}`),
    subject,
    author: i === 0 ? seed.author : i % 2 ? "reliant-01" : "sean",
    time: at(seed.agoMs + 20 * MIN + i * 37 * MIN),
  }));
  return {
    id: `rel-${index}`,
    version: seed.version,
    project: RELEASE_PROJECT,
    gitCommit: sha,
    gitTag: "",
    dirty: false,
    createdAt: at(seed.agoMs),
    createdByUserId: seed.author ? `user-${seed.author}` : "",
    subject: seed.subject,
    author: seed.author,
    branch: "main",
    repo: "github.com/reliant-labs/control-plane",
    forgeVersion: seed.commits ? "7fb6d653" : "5e10c2aa",
    commitTime: seed.commits ? at(seed.agoMs + 20 * MIN) : undefined,
    run: seed.commits ? { id: `run-${index}`, url: `https://github.com/reliant-labs/control-plane/actions/runs/${18000 + index}`, provider: "github" } : null,
    artifacts: ARTIFACTS.get(seed.version)!,
    commits: seed.commits
      ? {
          baseRelease: base?.version ?? "",
          baseCommit: base ? hexOf(base.version) : "",
          commits,
          total: seed.commits.total ?? commits.length,
          truncated: seed.commits.truncated ?? false,
        }
      : null,
  };
});

const V = RELEASES.map((r) => r.version);

// ── Promotions ──────────────────────────────────────────────────────────────

const passed = (name: string, summary: string, agoMs: number, source: ReleaseGate["source"] = "claimed"): ReleaseGate => ({
  name,
  status: "passed",
  rawStatus: "",
  summary,
  url: `https://github.com/reliant-labs/control-plane/actions/runs/${hexOf(name + agoMs, 6)}`,
  recordedBy: "token:ci",
  finishedAt: at(agoMs),
  source,
});

const CI = (agoMs: number) => passed("ci:full", "26 of 26 checks passed", agoMs);
const E2E = (agoMs: number) => passed("e2e", "41 passed, 0 failed", agoMs);

let promotionSeq = 0;
function promotion(
  envId: string,
  version: string,
  agoMs: number,
  by: { user?: string; actor?: string },
  extra: Partial<ReleasePromotion> = {}
): ReleasePromotion {
  promotionSeq += 1;
  return {
    id: `promo-${String(promotionSeq).padStart(3, "0")}`,
    environmentId: envId,
    releaseVersion: version,
    fromEnvironmentName: "",
    promotedByUserId: by.user ? `user-${by.user}` : "",
    promotedByActor: by.actor ?? "",
    approvedBy: "",
    note: "",
    createdAt: at(agoMs),
    targets: [],
    gates: [],
    bundleId: `bundle-${hexOf(envId + version + agoMs, 8)}`,
    bundleDigest: `sha256:${hexOf(`bundle/${envId}/${version}`, 64)}`,
    ...extra,
  };
}

const SEAN = { user: "sean" };
const AGENT = { actor: "reliant-01" };
const G3 = { user: "g3" };

/** Each environment's ledger, newest first. */
export const PROMOTIONS: Record<string, ReleasePromotion[]> = {
  "env-prod": [
    promotion("env-prod", V[0]!, 4 * HOUR, G3, { targets: ["admin-web", "internal-console"], note: "Frontends re-shipped after the CDN cache rule fix." }),
    promotion("env-prod", V[0]!, 5 * HOUR - 20 * MIN, SEAN, { gates: [CI(5 * HOUR - 30 * MIN), E2E(5 * HOUR - 25 * MIN)], fromEnvironmentName: "dev" }),
    promotion("env-prod", V[1]!, 12 * HOUR - 50 * MIN, SEAN, { gates: [CI(12 * HOUR - 55 * MIN), E2E(12 * HOUR - 52 * MIN)] }),
    promotion("env-prod", V[3]!, 14 * HOUR - 70 * MIN, SEAN, { note: "Rolling back: the smoke test failed on .063708." }),
    promotion("env-prod", V[2]!, 14 * HOUR - 35 * MIN, SEAN, {
      gates: [
        CI(14 * HOUR - 40 * MIN),
        {
          name: "smoke",
          status: "failed",
          rawStatus: "",
          summary: "admin-server /healthz returned 503 for 6 minutes",
          url: "https://github.com/reliant-labs/control-plane/actions/runs/18210",
          recordedBy: "token:ci",
          finishedAt: at(13 * HOUR),
          source: "recorded",
        },
      ],
      note: "Stops the forced Flux reconcile when the hub cluster's Kustomization already points at the promoted bundle. Verified on e2e: 0 forced reconciles across 12 promotions.",
    }),
    promotion("env-prod", V[3]!, 16 * HOUR - 50 * MIN, AGENT, { gates: [CI(16 * HOUR - 55 * MIN)] }),
    promotion("env-prod", V[4]!, 18 * HOUR - 40 * MIN, SEAN, { gates: [CI(18 * HOUR - 45 * MIN), E2E(18 * HOUR - 42 * MIN)] }),
    promotion("env-prod", V[5]!, 38 * HOUR - 60 * MIN, SEAN, { gates: [CI(38 * HOUR - 65 * MIN)] }),
    promotion("env-prod", V[6]!, 50 * HOUR - 60 * MIN, SEAN),
    promotion("env-prod", V[7]!, 62 * HOUR - 60 * MIN, SEAN),
  ],
  "env-e2e": [
    promotion("env-e2e", V[3]!, 16 * HOUR - 9 * MIN, AGENT, { gates: [CI(16 * HOUR - 12 * MIN)] }),
    promotion("env-e2e", V[4]!, 18 * HOUR - 20 * MIN, { actor: "ci" }, { gates: [CI(18 * HOUR - 22 * MIN)] }),
    promotion("env-e2e", V[6]!, 38 * HOUR - 4 * HOUR - 13 * MIN, SEAN, { note: "Back to the Oct 16 release: selfmon scrapes flood the collector." }),
    promotion("env-e2e", V[5]!, 38 * HOUR - 30 * MIN, { actor: "ci" }, { gates: [CI(38 * HOUR - 35 * MIN)] }),
    promotion("env-e2e", V[6]!, 50 * HOUR - 30 * MIN, { actor: "ci" }),
  ],
  "env-dev": [
    promotion("env-dev", V[0]!, 5 * HOUR - 10 * MIN, SEAN),
    promotion("env-dev", V[1]!, 12 * HOUR - 10 * MIN, AGENT),
    promotion("env-dev", V[2]!, 14 * HOUR - 10 * MIN, AGENT),
    promotion("env-dev", V[3]!, 16 * HOUR - 10 * MIN, AGENT),
    promotion("env-dev", V[4]!, 18 * HOUR - 10 * MIN, SEAN),
  ],
};

export const RELEASE_ENVS: ReleaseEnv[] = [
  env("env-prod", "prod", "long", V[0]!),
  env("env-e2e", "e2e", "long", V[3]!),
  env("env-dev", "dev", "long", V[0]!),
  env("env-pr-981", "pr-981", "preview", V[4]!),
  env("env-pr-984", "pr-984", "preview", V[0]!),
  env("env-pr-986", "pr-986", "preview", V[0]!),
];

export const FIXTURE_NAMES = new Map([
  ["user-sean", "sean"],
  ["user-g3", "G3"],
  ["user-reliant-01", "reliant-01"],
]);

// ── Comparisons ─────────────────────────────────────────────────────────────

function compare(fromVersion: string, toVersion: string, environmentId?: string): ReleaseComparison {
  const from = RELEASES.find((r) => r.version === fromVersion) ?? null;
  const to = RELEASES.find((r) => r.version === toVersion) ?? null;
  const fromIndex = V.indexOf(fromVersion);
  const toIndex = V.indexOf(toVersion);
  const reverts = toIndex > fromIndex;
  const [newer, older] = reverts ? [fromIndex, toIndex] : [toIndex, fromIndex];
  // The ranges of the releases after the older one, up to the newer one. When
  // one of them recorded none, no base chain joins the two: the commits the
  // others recorded, by cut time (approximate). When none did, unrecorded.
  const composed = RELEASES.slice(newer, older);
  const recorded = composed.filter((r) => r.commits);
  const unrecorded = composed.length > 0 && recorded.length === 0;
  const byCutTime = !unrecorded && recorded.length < composed.length;
  const commits = recorded.flatMap((r) => r.commits!.commits);
  const truncated = byCutTime || composed.some((r) => r.commits?.truncated);
  const artifacts: ArtifactChange[] = [];
  for (const artifact of to?.artifacts ?? []) {
    const before = from?.artifacts.find((a) => a.name === artifact.name);
    if (before && before.digest !== artifact.digest) {
      artifacts.push({ name: artifact.name, kind: artifact.kind, change: "modified", from: before.digest, to: artifact.digest });
    }
  }
  const deltas: ObjectDelta[] = artifacts.length
    ? [
        ...artifacts
          .filter((a) => a.kind === "image")
          .map((a) => ({
            change: "modified" as const,
            cluster: "hub",
            kind: "Deployment",
            namespace: "control-plane-prod",
            name: a.name === "control-plane" ? "admin-server" : a.name,
            workload: a.name,
            summary: `image ${a.name} ${a.from.slice(7, 19)} → ${a.to.slice(7, 19)}`,
            images: [{ artifact: a.name, from: a.from, to: a.to }],
          })),
        {
          change: "modified",
          cluster: "hub",
          kind: "ConfigMap",
          namespace: "control-plane-prod",
          name: "admin-server-config",
          workload: "admin-server",
          summary: "data.IDENTITY_FORWARDED_HEADER added",
          images: [],
        },
        {
          change: "added",
          cluster: "hub",
          kind: "NetworkPolicy",
          namespace: "control-plane-prod",
          name: "identity-gateway-egress",
          workload: "",
          summary: "egress to zitadel:8080",
          images: [],
        },
        {
          change: "removed",
          cluster: "hub",
          kind: "CronJob",
          namespace: "control-plane-prod",
          name: "preprod-cleanup",
          workload: "",
          summary: "the last preprod remnant",
          images: [],
        },
      ]
    : [];
  // The two oldest releases were deployed before bundles were recorded.
  const unbundled = V.slice(-2);
  const oldest = unbundled.find((v) => v === fromVersion || v === toVersion);
  return {
    from,
    to,
    commits: {
      state: unrecorded ? "unrecorded" : composed.length === 0 ? "same" : byCutTime ? "by-cut-time" : "computed",
      reverts,
      commits,
      total: truncated ? 0 : commits.length,
      truncated,
      releases: composed.map((r) => r.version),
    },
    artifacts,
    unchangedArtifacts: (to?.artifacts.length ?? 0) - artifacts.length,
    objects: !environmentId
      ? { state: "no-environment", detail: "", deltas: [], unchanged: 0, summariesMissing: false }
      : oldest
        ? {
            state: "bundle-missing",
            detail: `No bundle was recorded for ${oldest} in this environment: it was deployed before bundles were.`,
            deltas: [],
            unchanged: 0,
            summariesMissing: false,
          }
        : { state: "compared", detail: "", deltas, unchanged: 46, summariesMissing: false },
  };
}

function rollout(environmentId: string, promotionId: string): ReleaseRollout {
  const ledger = PROMOTIONS[environmentId] ?? [];
  const index = ledger.findIndex((p) => p.id === promotionId);
  const p = ledger[index];
  const superseded = index > 0 && ledger.slice(0, index).some((later) => later.targets.length === 0);
  const workloads = ["admin-server", "reliant-api-server", "workspace-operator", "daemon-gateway", "litellm"].map((name, i) => ({
    name,
    artifact: name === "litellm" ? "litellm" : "control-plane",
    pinnedDigest: digestFor(name === "litellm" ? "litellm" : "control-plane", p?.releaseVersion ?? ""),
    observedDigest: digestFor(name === "litellm" ? "litellm" : "control-plane", p?.releaseVersion ?? ""),
    phase: superseded ? ("superseded" as const) : ("succeeded" as const),
    verdict: "converged" as const,
    observedState: "ready" as const,
    updatedReplicas: i === 0 ? 2 : 1,
    desiredReplicas: i === 0 ? 2 : 1,
    convergedAt: p?.createdAt ? new Date(Date.parse(p.createdAt) + (3 + i) * MIN).toISOString() : undefined,
    lastError: "",
    reason: "",
  }));
  return {
    promotionId,
    phase: superseded ? "superseded" : "succeeded",
    reason: superseded ? "A newer promotion replaced this one." : "",
    startedAt: p?.createdAt,
    finishedAt: p?.createdAt ? new Date(Date.parse(p.createdAt) + 9 * MIN).toISOString() : undefined,
    workloads,
    unpinned: [],
    forgeApplied: [],
  };
}

function bundle(environmentId: string, bundleId: string, digest: string): ReleaseBundle | null {
  const p = (PROMOTIONS[environmentId] ?? []).find((candidate) => candidate.bundleId === bundleId || candidate.bundleDigest === digest);
  if (!p) return null;
  return {
    id: p.bundleId,
    digest: p.bundleDigest,
    reference: `oci://us-docker.pkg.dev/reliant-labs/bundles/control-plane@${p.bundleDigest}`,
    configDigest: `sha256:${hexOf(`config/${p.bundleId}`, 64)}`,
    snapshotDigest: `sha256:${hexOf(`snapshot/${p.bundleId}`, 64)}`,
    createdAt: p.createdAt,
    createdBy: "token:ci",
  };
}

// ── Deploys ─────────────────────────────────────────────────────────────────

const runBase = {
  promotionId: "",
  startedByUserId: "user-sean",
  startedByActor: "",
  phases: [],
  failedPhase: "",
  error: "",
  exitCode: null,
  runUrl: "",
  hasLog: true,
  logTruncated: false,
  hasReport: false,
};

/** Newest first: a deploy rolling out to e2e, one that failed building for prod, and the one before it that landed. */
export const DEPLOY_RUNS: DeployRun[] = [
  { ...runBase, id: "att-3", env: "e2e", mode: "apply", source: "console", release: V[0]!, phase: "rollout", state: "running", startedAt: at(4 * MIN) },
  {
    ...runBase,
    id: "att-2",
    env: "prod",
    mode: "apply",
    source: "terminal",
    release: "",
    phase: "build",
    state: "failed",
    failedPhase: "build",
    error: "building api: docker build exited 1",
    exitCode: 1,
    startedAt: at(2 * HOUR),
    finishedAt: at(2 * HOUR - 3 * MIN),
  },
  { ...runBase, id: "att-1", env: "prod", mode: "apply", source: "ci", release: V[1]!, phase: "rollout", state: "succeeded", exitCode: 0, startedAt: at(26 * HOUR), finishedAt: at(26 * HOUR - 6 * MIN) },
];

export const DEPLOY_RUN_LOG = "[build] api: step 7/12 RUN go build ./cmd/api\n# github.com/acme/api\ninternal/handlers/orders.go:41:2: undefined: ordersv2\nerror: building api: docker build exited 1\n";

// ── The fake API ────────────────────────────────────────────────────────────

export interface ReleasesFixtureOptions {
  /** "empty": nothing was ever cut. "error": every read fails. "loading": every read hangs. */
  state?: "data" | "empty" | "error" | "loading";
  /** Ledger reads fail (the list still loads; the env columns fall back to the live view). */
  promotionsFail?: boolean;
  /** CompareReleases fails. */
  compareFails?: boolean;
  /** GetRollout's forge-applied slots (a self-managed environment the hub does not converge). */
  forgeApplied?: ForgeAppliedSlot[];
}

export function createReleasesFixtureApi(options: ReleasesFixtureOptions = {}): ReleasesApi {
  const state = options.state ?? "data";
  const answer = async <T,>(value: () => T): Promise<T> => {
    if (state === "loading") return new Promise<T>(() => {});
    if (state === "error") throw new ConnectError("control plane unavailable", Code.Unavailable);
    return value();
  };
  const empty = state === "empty";
  return {
    listReleases: () => answer(() => (empty ? [] : RELEASES)),
    getRelease: (_project, version) =>
      answer(() => {
        const release = empty ? undefined : RELEASES.find((r) => r.version === version);
        if (!release) return null;
        return {
          release,
          currentEnvironmentIds: RELEASE_ENVS.filter((e) => e.current === version).map((e) => e.id),
        };
      }),
    compareReleases: (req) =>
      answer(() => {
        if (options.compareFails) throw new ConnectError("compare timed out", Code.DeadlineExceeded);
        return compare(req.from, req.to, req.environmentId);
      }),
    listPromotions: (environmentId) =>
      answer(() => {
        if (options.promotionsFail) throw new ConnectError("ledger unavailable", Code.Unavailable);
        return empty ? [] : (PROMOTIONS[environmentId] ?? []);
      }),
    getRollout: (environmentId, promotionId) =>
      answer(() => ({ ...rollout(environmentId, promotionId), forgeApplied: options.forgeApplied ?? [] })),
    getBundle: (environmentId, bundleId, digest) => answer(() => bundle(environmentId, bundleId, digest)),
    listDeployRuns: (_project, env) => answer(() => (empty ? [] : DEPLOY_RUNS.filter((run) => !env || run.env === env))),
    getDeployRun: (id) =>
      answer(() => {
        const run = DEPLOY_RUNS.find((candidate) => candidate.id === id);
        if (!run) throw new ConnectError("no such deploy", Code.NotFound);
        return { run, log: DEPLOY_RUN_LOG, report: "" };
      }),
  };
}

// ── Delivery (names, DORA) ──────────────────────────────────────────────────

/** The same ledger as DeliveryInsights' markers, oldest first — what resolves user ids to names. */
export const FIXTURE_DELIVERY: Promotion[] = Object.entries(PROMOTIONS)
  .flatMap(([envId, ledger]) =>
    ledger.map((p) => ({
      promotionId: p.id,
      envId,
      envName: RELEASE_ENVS.find((e) => e.id === envId)?.name ?? "",
      releaseId: `rel-${V.indexOf(p.releaseVersion)}`,
      version: p.releaseVersion,
      gitCommit: hexOf(p.releaseVersion),
      gitTag: "",
      promotedAtMs: Date.parse(p.createdAt ?? ""),
      promotedBy: p.promotedByActor || FIXTURE_NAMES.get(p.promotedByUserId) || "",
      promotedByUserId: p.promotedByUserId,
      kind: "promote",
    }))
  )
  .sort((a, b) => a.promotedAtMs - b.promotedAtMs);

export const FIXTURE_DORA: DoraMetrics = {
  windowStartMs: FIXTURE_NOW - 30 * 24 * HOUR,
  windowEndMs: FIXTURE_NOW,
  failureWindowHours: 24,
  environmentCount: 1,
  deploymentFrequency: { definition: "Promotions that moved a new release into the environment, per day.", deployments: 23, perDay: 0.77, rollbacks: 2, reapplies: 1 },
  leadTime: { definition: "From the release's commit to its promotion.", measuredChanges: 19, unknownChanges: 4, unknownReleases: 3, medianMs: 32 * MIN, meanMs: 51 * MIN },
  changeFailureRate: { definition: "Deployments rolled back within 24 hours.", changes: 23, failedChanges: 2, rate: 2 / 23 },
  timeToRestore: { definition: "From a failed deployment to the promotion that rolled it back.", source: "release_ledger", failures: 2, ongoing: 0, medianMs: 27 * MIN, meanMs: 27 * MIN },
  alertTimeToRestore: null,
};
