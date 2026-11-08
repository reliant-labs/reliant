// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { AlertInstance } from "@/services/controlPlane/observability/alertingTypes";
import type { ReleaseComparison, ReleasePromotion } from "@/services/forge/releases";

import {
  attentionItems,
  declarationLinks,
  deployHistory,
  digestOf,
  filterServices,
  formatMillicores,
  formatQuantityBytes,
  formatSeconds,
  githubRepoOf,
  jobRows,
  lastExitOf,
  needsAttention,
  previewsCell,
  replicasText,
  serviceRows,
  specGapNote,
  stripCell,
  workloadChange,
  workloadDigestOf,
  workloadReleaseRows,
  worstUsage,
} from "../workloadModel";
import { FIXTURE_JOBS, STRIP_ENVS, fixtureWorkloadList } from "../__fixtures__/workloadFixtures";

const NOW = Date.UTC(2026, 9, 20, 12);
const { workloads } = fixtureWorkloadList(NOW);
const ENVS = STRIP_ENVS;

describe("the health verdict is the server's", () => {
  it("lists what needs attention from the verdict alone, never from restarts", () => {
    // litellm restarted twice and is degraded; web never restarted and is healthy;
    // a healthy workload with restarts stays healthy (the server's rule), and pending is a rollout, not a fault.
    expect(needsAttention({ health: "degraded" })).toBe(true);
    expect(needsAttention({ health: "crash_looping" })).toBe(true);
    expect(needsAttention({ health: "pending" })).toBe(false);
    expect(needsAttention({ health: "healthy" })).toBe(false);
    expect(needsAttention({ health: "unknown" })).toBe(false);
  });
});

describe("replicas", () => {
  it("reads ready of desired, ready alone when nothing says how many are wanted, and a dash when nothing is known", () => {
    expect(replicasText({ readyReplicas: 1, desiredReplicas: 2 })).toBe("1/2");
    expect(replicasText({ readyReplicas: null, desiredReplicas: 2 })).toBe("0/2");
    expect(replicasText({ readyReplicas: 3, desiredReplicas: null })).toBe("3");
    expect(replicasText({ readyReplicas: null, desiredReplicas: null })).toBe("—");
  });
});

describe("rows", () => {
  it("keeps Jobs out of Services and sorts by name, then environment order", () => {
    const all = serviceRows([...workloads, { ...workloads[0]!, workload: "nightly", kind: "cronjob" }], "all", ENVS);
    expect(all.some((w) => w.workload === "nightly")).toBe(false);
    expect(all.filter((w) => w.workload === "admin-server").map((w) => w.envName)).toEqual(["prod", "e2e", "dev", "staging", "pr-981"]);
    expect(serviceRows(workloads, "prod", ENVS).map((w) => w.workload)).toEqual(["admin-server", "docs", "litellm", "reliant-api-server", "reliant-temporal-worker", "web"]);
  });

  it("lists what a self-managed environment declares as unobserved, never as healthy or missing", () => {
    const staging = serviceRows(workloads, "staging", ENVS);
    expect(staging.map((w) => [w.workload, w.unobserved, w.health])).toEqual([
      ["admin-server", true, "unknown"],
      ["reliant-api-server", true, "unknown"],
      ["reliant-temporal-worker", true, "unknown"],
    ]);
    // A hosted environment's declaration is not listed: the platform observes it.
    expect(serviceRows(workloads, "prod", ENVS).some((w) => w.unobserved)).toBe(false);
    // A declared workload that does report is listed once, observed.
    const reporting = serviceRows([...workloads, { ...workloads[0]!, envId: "env-staging", envName: "staging" }], "staging", ENVS);
    expect(reporting.filter((w) => w.workload === "admin-server").map((w) => !!w.unobserved)).toEqual([false]);
  });

  it("collapses Job runs to the latest per environment, counting the runs and failures behind it", () => {
    const rows = jobRows(FIXTURE_JOBS, "all");
    const e2e = rows.find((row) => row.envName === "e2e")!;
    expect(e2e.latest.result).toBe("failed");
    expect(e2e).toMatchObject({ runs: 2, failedRuns: 1 });
    expect(jobRows(FIXTURE_JOBS, "prod")).toHaveLength(1);
  });

  it("filters by text (name or release), needs-attention and kind", () => {
    const prod = serviceRows(workloads, "prod", ENVS);
    expect(filterServices(prod, { q: "TEMPORAL" }).map((w) => w.workload)).toEqual(["reliant-temporal-worker"]);
    expect(filterServices(prod, { status: "attention" }).map((w) => w.workload)).toEqual(["litellm", "reliant-temporal-worker"]);
    expect(filterServices(prod, { kind: "statefulset" }).map((w) => w.workload)).toEqual(["reliant-temporal-worker"]);
  });

  it("names the most recent exit and the container closest to its limit", () => {
    const w = workloads.find((x) => x.workload === "reliant-temporal-worker")!;
    expect(lastExitOf(w)?.reason).toBe("OOMKilled");
    expect(worstUsage(w, "memory")?.limitUtilizationLatest).toBeGreaterThan(0.95);
    expect(lastExitOf(workloads.find((x) => x.workload === "web")!)).toBeNull();
  });
});

describe("needs attention", () => {
  const alert: AlertInstance = {
    envId: "env-prod",
    rule: "ReliantTemporalWorkerOOM",
    fingerprint: "f1",
    labels: { workload: "reliant-temporal-worker" },
    annotations: {},
    state: "firing",
    activeAtMs: NOW - 60 * 60_000,
    firingAtMs: NOW - 52 * 60_000,
    lastEvalAtMs: NOW,
    lastValue: 1,
  };

  it("puts firing alerts first, then the worst workloads, then jobs whose latest run failed", () => {
    const items = attentionItems(serviceRows(workloads, "all", ENVS), jobRows(FIXTURE_JOBS, "all"), [alert], (id) => (id === "env-prod" ? "prod" : undefined), NOW);
    expect(items.map((item) => `${item.kind}:${item.subject}:${item.envName}`)).toEqual([
      "alert:ReliantTemporalWorkerOOM:prod",
      "workload:reliant-temporal-worker:prod",
      "workload:litellm:prod",
      "job:control-plane-migrate:e2e",
    ]);
    expect(items[0]).toMatchObject({ detail: "for 52m", workload: null });
    expect(items[1]).toMatchObject({ what: "crash-looping", detail: expect.stringContaining("Restarting repeatedly (14 times"), tone: "danger" });
    // the Job controller's word is not the headline
    expect(items[3]).toMatchObject({ what: "failed", detail: "Failed after exhausting its retries" });
  });

  it("leaves a job that failed and then succeeded off the list", () => {
    const recovered = [{ ...FIXTURE_JOBS[3]!, startedAtMs: NOW }, ...FIXTURE_JOBS];
    const items = attentionItems([], jobRows(recovered, "e2e"), [], () => undefined, NOW);
    expect(items).toEqual([]);
  });
});

describe("the environment strip", () => {
  const jobs = jobRows(FIXTURE_JOBS, "all");
  const env = (name: string) => STRIP_ENVS.find((candidate) => candidate.name === name)!;

  it("shows each environment's worst state", () => {
    expect(stripCell(env("prod"), workloads, jobs)).toMatchObject({ tone: "danger", label: "1 failing", services: 6 });
    expect(stripCell(env("e2e"), workloads, jobs)).toMatchObject({ tone: "danger", label: "1 job failed", failedJobs: 1 });
    expect(stripCell(env("dev"), workloads, jobs)).toMatchObject({ tone: "success", label: "Healthy" });
  });

  it("says No data for an environment nothing reports from, rather than Healthy", () => {
    expect(stripCell(env("pr-986"), workloads, jobs)).toMatchObject({ tone: "neutral", label: "No data", silent: true });
  });

  it("calls a silent self-managed environment Unobserved, counting what it declares", () => {
    expect(stripCell(env("staging"), workloads, jobs)).toMatchObject({ tone: "neutral", label: "Unobserved", services: 3 });
  });

  it("folds the previews into one cell", () => {
    const cell = previewsCell(STRIP_ENVS.filter((candidate) => candidate.preview), workloads, jobs)!;
    expect(cell).toMatchObject({ tone: "success", label: "Healthy", services: 1 });
    expect(previewsCell([], workloads, jobs)).toBeNull();
  });
});

describe("the declaration (Scale opens a pull request)", () => {
  it("recognizes the repository forms a bundle records", () => {
    expect(githubRepoOf("https://github.com/reliant-labs/control-plane.git")).toBe("reliant-labs/control-plane");
    expect(githubRepoOf("git@github.com:reliant-labs/control-plane.git")).toBe("reliant-labs/control-plane");
    expect(githubRepoOf("github.com/acme/shop")).toBe("acme/shop");
    expect(githubRepoOf("https://gitlab.com/acme/shop")).toBeNull();
    expect(githubRepoOf("")).toBeNull();
  });

  it("edits only on the repository's default branch, and views at the declaration's own commit", () => {
    const declared = { sourceRepo: "https://github.com/acme/shop.git", sourceCommit: "992bb09", sourcePath: "deploy/kcl/prod/main.k" };
    expect(declarationLinks(declared, "main")).toEqual({
      edit: "https://github.com/acme/shop/edit/main/deploy/kcl/prod/main.k",
      view: "https://github.com/acme/shop/blob/992bb09/deploy/kcl/prod/main.k",
    });
    // Default branch unknown (or still being asked): no editor link, nothing guessed; the file can still be viewed.
    expect(declarationLinks(declared, null)).toEqual({ edit: null, view: "https://github.com/acme/shop/blob/992bb09/deploy/kcl/prod/main.k" });
    expect(declarationLinks(declared, undefined).edit).toBeNull();
    expect(declarationLinks({ ...declared, sourceRepo: "" }, "main")).toEqual({ edit: null, view: null });
  });

  it("reads the digest of a pinned image, and nothing from an unpinned one", () => {
    expect(digestOf("ghcr.io/acme/api@sha256:abc")).toBe("sha256:abc");
    expect(digestOf("ghcr.io/acme/api:v1")).toBe("");
  });
});

describe("deploy history", () => {
  const p = (id: string, releaseVersion: string, targets: string[] = [], by = "user-sean"): ReleasePromotion => ({
    id,
    environmentId: "env-prod",
    releaseVersion,
    fromEnvironmentName: "",
    promotedByUserId: by,
    promotedByActor: "",
    approvedBy: "",
    note: "",
    createdAt: "2026-10-20T12:00:00Z",
    targets,
    gates: [],
    bundleId: "",
    bundleDigest: "",
    resolvedArtifacts: {},
  });
  // Cut order, newest first: v3 is the newest release.
  const rank = new Map([["v3", 0], ["v2", 1], ["v1", 2]]);
  const names = new Map([["user-sean", "sean"]]);

  it("keeps the promotions that moved the workload, marking a move to an older release as a rollback", () => {
    // Newest first: v2 replaced v3 (a rollback), v3 replaced v2, a scoped deploy of another component, v2.
    const ledger = [p("4", "v2"), p("3", "v3"), p("2", "v3", ["web"]), p("1", "v2")];
    const entries = deployHistory(ledger, "api", rank, names);
    expect(entries.map((e) => [e.id, e.version, e.rollback, e.scoped])).toEqual([
      ["4", "v2", true, false],
      ["3", "v3", false, false],
      ["1", "v2", false, false],
    ]);
    expect(entries[0]!.by).toBe("sean");
    // A scoped deploy that named the workload is in its history, and says so.
    expect(deployHistory([p("5", "v3", ["api"])], "api", rank, names)[0]).toMatchObject({ scoped: true, rollback: false });
  });

  it("does not judge a release outside the loaded list", () => {
    expect(deployHistory([p("2", "v0"), p("1", "v3")], "api", rank, names)[0]!.rollback).toBe(false);
  });

  it("gives each release row the release it replaced for this workload, its checks and its frozen artifacts", () => {
    const failed = { name: "smoke", status: "failed" as const, rawStatus: "", summary: "", url: "", recordedBy: "", source: "recorded" as const };
    const ledger = [
      { ...p("4", "v2"), gates: [failed], resolvedArtifacts: { api: "sha256:2222" } },
      p("3", "v3"),
      p("2", "v3", ["web"]),
      p("1", "v2"),
    ];
    const rows = workloadReleaseRows(ledger, "api", rank, names);
    // The scoped deploy of another component is not in api's chain: v3 replaced v2, and v2 replaced v3.
    expect(rows.map((r) => [r.version, r.previousVersion, r.rollback])).toEqual([
      ["v2", "v3", true],
      ["v3", "v2", false],
      ["v2", null, false],
    ]);
    expect(rows[0]!.checks).toMatchObject({ failed: 1, total: 1 });
    expect(rows[1]!.checks).toBeNull();
    expect(rows[0]!.resolvedArtifacts).toEqual({ api: "sha256:2222" });
  });
});

describe("what a release changed in one workload", () => {
  const comparison = (objects: Partial<ReleaseComparison["objects"]>): ReleaseComparison => ({
    from: null,
    to: null,
    commits: { state: "computed", reverts: false, commits: [], total: 0, truncated: false, releases: [] },
    artifacts: [],
    unchangedArtifacts: 0,
    objects: { state: "compared", detail: "", deltas: [], unchanged: 0, summariesMissing: false, workload: "", ...objects },
  });
  const delta = (over: Partial<ReleaseComparison["objects"]["deltas"][number]>) => ({
    change: "modified" as const,
    cluster: "hub",
    kind: "Deployment",
    namespace: "cp",
    name: "api",
    workload: "api",
    summary: "",
    images: [],
    ...over,
  });
  const row = { version: "v3", previousVersion: "v2" };

  it("names the first release and a re-promotion without a comparison", () => {
    expect(workloadChange({ version: "v1", previousVersion: null }, undefined, "api")).toEqual({ kind: "first" });
    expect(workloadChange({ version: "v1", previousVersion: "v1" }, undefined, "api")).toEqual({ kind: "same-release" });
    expect(workloadChange(row, undefined, "api")).toBeNull();
  });

  it("keeps only this workload's objects, even from a server that compared every object", () => {
    const change = workloadChange(
      row,
      comparison({
        deltas: [
          delta({ summary: "image api; env FOO added", images: [{ artifact: "api", from: "sha256:1111", to: "sha256:2222" }] }),
          delta({ kind: "ConfigMap", name: "api", workload: "", change: "added", summary: "keys a" }),
          delta({ name: "web", workload: "web", summary: "image web" }),
          delta({ kind: "Service", name: "unrelated", workload: "" }),
        ],
      }),
      "api"
    );
    expect(change).toEqual({
      kind: "changed",
      lines: ["modified Deployment api: image api; env FOO added", "added ConfigMap api: keys a"],
      image: { artifact: "api", from: "sha256:1111", to: "sha256:2222" },
    });
    expect(workloadDigestOf({ resolvedArtifacts: {} }, change, "api")).toBe("sha256:2222");
  });

  it("says nothing changed, and passes on why objects could not be compared", () => {
    expect(workloadChange(row, comparison({ deltas: [delta({ name: "web", workload: "web" })], workload: "api", unchanged: 3 }), "api")).toEqual({ kind: "unchanged", unchanged: 3 });
    expect(workloadChange(row, comparison({ state: "bundle-missing", detail: "no bundle for v2" }), "api")).toEqual({ kind: "not-compared", detail: "no bundle for v2" });
  });

  it("takes the digest from the frozen artifact named like the workload, and never guesses another", () => {
    expect(workloadDigestOf({ resolvedArtifacts: { api: "sha256:abcd" } }, { kind: "unchanged", unchanged: 0 }, "api")).toBe("sha256:abcd");
    expect(workloadDigestOf({ resolvedArtifacts: { "control-plane": "sha256:abcd" } }, null, "api")).toBe("");
    expect(workloadDigestOf({ resolvedArtifacts: { api: "0123abc" } }, null, "api")).toBe("");
  });
});

describe("the Config tab's not-recorded note", () => {
  it("tells the truth for each reason, and promises a deploy fixes it only where one does", () => {
    expect(specGapNote("pending", "api", "prod")).toMatch(/has not read the manifests .* without a deploy/);
    expect(specGapNote("unreadable", "api", "prod")).toMatch(/registry Reliant cannot read.*A deploy that pushes its bundle to the platform registry records the rest/);
    expect(specGapNote("not-in-manifests", "api", "prod")).toMatch(/names api, but its rendered manifests hold no object/);
    for (const gap of ["pending", "not-in-manifests", "unknown", null] as const) {
      expect(specGapNote(gap, "api", "prod")).not.toMatch(/next deploy/i);
    }
  });
});

describe("formatting", () => {
  it("spells resources as Kubernetes does, and an undeclared zero as nothing", () => {
    expect(formatMillicores(250)).toBe("250m");
    expect(formatMillicores(1000)).toBe("1 core");
    expect(formatMillicores(1500)).toBe("1.5 cores");
    expect(formatMillicores(0)).toBe("");
    expect(formatQuantityBytes(512 * 1024 * 1024)).toBe("512Mi");
    expect(formatQuantityBytes(1024 ** 3)).toBe("1Gi");
    expect(formatQuantityBytes(0)).toBe("");
  });

  it("reads a Job's duration", () => {
    expect(formatSeconds(41)).toBe("41s");
    expect(formatSeconds(312)).toBe("5m 12s");
    expect(formatSeconds(3600)).toBe("1h");
  });
});
