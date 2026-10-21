// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { AlertInstance } from "@/services/controlPlane/observability/alertingTypes";

import {
  attentionItems,
  declarationLinks,
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
  stripCell,
  worstUsage,
} from "../workloadModel";
import { FIXTURE_JOBS, STRIP_ENVS, fixtureWorkloadList } from "../__fixtures__/workloadFixtures";

const NOW = Date.UTC(2026, 9, 20, 12);
const { workloads } = fixtureWorkloadList(NOW);
const ORDER = STRIP_ENVS.map((env) => env.name);

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
    const all = serviceRows([...workloads, { ...workloads[0]!, workload: "nightly", kind: "cronjob" }], "all", ORDER);
    expect(all.some((w) => w.workload === "nightly")).toBe(false);
    expect(all.filter((w) => w.workload === "admin-server").map((w) => w.envName)).toEqual(["prod", "e2e", "dev", "pr-981"]);
    expect(serviceRows(workloads, "prod", ORDER).map((w) => w.workload)).toEqual(["admin-server", "litellm", "reliant-api-server", "reliant-temporal-worker", "web"]);
  });

  it("collapses Job runs to the latest per environment, counting the runs and failures behind it", () => {
    const rows = jobRows(FIXTURE_JOBS, "all");
    const e2e = rows.find((row) => row.envName === "e2e")!;
    expect(e2e.latest.result).toBe("failed");
    expect(e2e).toMatchObject({ runs: 2, failedRuns: 1 });
    expect(jobRows(FIXTURE_JOBS, "prod")).toHaveLength(1);
  });

  it("filters by text (name or release), needs-attention and kind", () => {
    const prod = serviceRows(workloads, "prod", ORDER);
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
    const items = attentionItems(serviceRows(workloads, "all", ORDER), jobRows(FIXTURE_JOBS, "all"), [alert], (id) => (id === "env-prod" ? "prod" : undefined), NOW);
    expect(items.map((item) => `${item.kind}:${item.subject}:${item.envName}`)).toEqual([
      "alert:ReliantTemporalWorkerOOM:prod",
      "workload:reliant-temporal-worker:prod",
      "workload:litellm:prod",
      "job:control-plane-migrate:e2e",
    ]);
    expect(items[0]).toMatchObject({ detail: "for 52m", workload: null });
    expect(items[1]).toMatchObject({ what: "crash-looping", detail: "restarted 14x, last exit OOMKilled", tone: "danger" });
    expect(items[3]).toMatchObject({ what: "failed", detail: "BackoffLimitExceeded" });
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
    expect(stripCell(env("prod"), workloads, jobs)).toMatchObject({ tone: "danger", label: "1 failing", services: 5 });
    expect(stripCell(env("e2e"), workloads, jobs)).toMatchObject({ tone: "danger", label: "1 job failed", failedJobs: 1 });
    expect(stripCell(env("dev"), workloads, jobs)).toMatchObject({ tone: "success", label: "Healthy" });
  });

  it("says No data for an environment nothing reports from, rather than Healthy", () => {
    expect(stripCell(env("pr-986"), workloads, jobs)).toMatchObject({ tone: "neutral", label: "No data", silent: true });
  });

  it("folds the previews into one cell", () => {
    const cell = previewsCell(STRIP_ENVS.filter((candidate) => candidate.preview), workloads, jobs)!;
    expect(cell).toMatchObject({ tone: "success", label: "Healthy", services: 1 });
    expect(previewsCell([], workloads, jobs)).toBeNull();
  });
});

describe("the declaration (D6: Scale opens a pull request)", () => {
  it("recognizes the repository forms a bundle records", () => {
    expect(githubRepoOf("https://github.com/reliant-labs/control-plane.git")).toBe("reliant-labs/control-plane");
    expect(githubRepoOf("git@github.com:reliant-labs/control-plane.git")).toBe("reliant-labs/control-plane");
    expect(githubRepoOf("github.com/acme/shop")).toBe("acme/shop");
    expect(githubRepoOf("https://gitlab.com/acme/shop")).toBeNull();
    expect(githubRepoOf("")).toBeNull();
  });

  it("edits on the release's branch (saving proposes a PR) and views at the rendered commit", () => {
    const declared = { sourceRepo: "https://github.com/acme/shop.git", sourceCommit: "992bb09", sourcePath: "deploy/kcl/prod/main.k" };
    expect(declarationLinks(declared, "main")).toEqual({
      edit: "https://github.com/acme/shop/edit/main/deploy/kcl/prod/main.k",
      view: "https://github.com/acme/shop/blob/992bb09/deploy/kcl/prod/main.k",
    });
    // No branch: nothing to open a pull request against, but the file can still be viewed.
    expect(declarationLinks(declared, null).edit).toBeNull();
    expect(declarationLinks({ ...declared, sourceRepo: "" }, "main")).toEqual({ edit: null, view: null });
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
