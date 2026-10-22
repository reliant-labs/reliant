// Copyright (c) 2025 Reliant Labs

/**
 * The cluster-inventory projection, tested at the layer where the certainty
 * discipline is actually decided.
 *
 * Every test here is a distinction that an obvious simplification would erase:
 * status-vs-length, omitted-vs-zero, absent-key-vs-empty-array,
 * empty-cluster-vs-current-context. Each one was a real failure mode of the
 * screen this replaces, or is explicitly called out in forge's own
 * clusterinventory.go as a thing consumers get wrong.
 */

import { describe, expect, it } from "vitest";

import {
  distinctScopes,
  findingsOf,
  inventoryOf,
  inventorySentence,
  isMeasured,
  podsOf,
  posture,
  replicas,
  scopeOf,
  statusTally,
  workloadsOf,
  type ForgeClusterInventory,
} from "../workloads";

describe("posture — the branch that must happen before anything else", () => {
  it("treats an absent workloads key as 'nobody was asked', not 'deploys nothing'", () => {
    expect(posture(inventoryOf({}))).toBe("not-reported");
    expect(posture(null)).toBe("not-reported");
    expect(isMeasured(null)).toBe(false);
  });

  it("treats a full workload list with unknown status as NOT an inventory", () => {
    const inventory: ForgeClusterInventory = {
      status: "unknown",
      clusters: [{ cluster: "gke-prod", status: "unknown", rendered_workloads: 16 }],
      workloads: Array.from({ length: 16 }, (_, i) => ({
        name: `w${i}`,
        status: "unknown",
        ready_replicas: 0,
      })),
    };
    // Sixteen rows, and still not a reading. This is the whole rule.
    expect(workloadsOf(inventory)).toHaveLength(16);
    expect(posture(inventory)).toBe("undetermined");
    expect(isMeasured(inventory)).toBe(false);
  });

  it("only calls an env empty when forge passed AND probed no scopes", () => {
    expect(posture({ status: "pass", clusters: [], workloads: [] })).toBe("nothing");
    // Passed, probed a cluster, and listed nothing: a hole, not an empty env.
    expect(
      posture({ status: "pass", clusters: [{ cluster: "gke-prod", status: "pass" }], workloads: [] })
    ).toBe("inconsistent");
  });

  it("falls to undetermined for a status this build does not recognise", () => {
    // Guessing 'measured' for an unreadable status is indistinguishable from a
    // verified reading, which is the one wrong direction to fail in.
    expect(posture({ status: "partially-degraded", workloads: [{ name: "a" }] })).toBe(
      "undetermined"
    );
  });
});

describe("replicas — an omitted count is not a zero", () => {
  it("reports no-replicas for a Job, which omits desired_replicas entirely", () => {
    const reading = replicas({ kind: "job", status: "pass", ready_replicas: 0, ephemeral: true });
    expect(reading).toEqual({ kind: "no-replicas", ephemeral: true });
  });

  it("reports unmeasured for an unknown workload rather than 0/N", () => {
    const reading = replicas({ status: "unknown", desired_replicas: 2, ready_replicas: 0 });
    expect(reading.kind).toBe("unmeasured");
  });

  it("counts, and flags short, when the numbers are real", () => {
    expect(replicas({ status: "pass", desired_replicas: 2, ready_replicas: 2 })).toEqual({
      kind: "counted",
      ready: 2,
      desired: 2,
      short: false,
    });
    expect(replicas({ status: "fail", desired_replicas: 2, ready_replicas: 0 })).toEqual({
      kind: "counted",
      ready: 0,
      desired: 2,
      short: true,
    });
  });
});

describe("scopeOf — an empty cluster is reported, never guessed", () => {
  it("marks a workload the render could not route as unrouted", () => {
    expect(scopeOf({ name: "orphan", cluster: "" })).toEqual({
      routed: false,
      cluster: "",
      namespace: "",
    });
  });

  it("carries a real context through verbatim", () => {
    expect(scopeOf({ cluster: "gke-prod", namespace: "control-plane-prod" })).toEqual({
      routed: true,
      cluster: "gke-prod",
      namespace: "control-plane-prod",
    });
  });
});

describe("statusTally — an unrecognised status never counts as a pass", () => {
  it("sums the rendered rows and sends anything unreadable to unknown", () => {
    const tally = statusTally({
      status: "warn",
      workloads: [
        { name: "a", status: "pass" },
        { name: "b", status: "fail" },
        { name: "c", status: "warn" },
        { name: "d", status: "something-new" },
        { name: "e" },
      ],
    });
    expect(tally).toEqual({ pass: 1, fail: 1, warn: 1, skip: 0, unknown: 2 });
  });
});

describe("projections tolerate a malformed document rather than throwing", () => {
  it("keeps an unnamed workload, positionally labelled", () => {
    const rows = workloadsOf({ workloads: [{ kind: "deployment" }] });
    expect(rows[0].name).toBe("Workload 1");
  });

  it("survives nulls and non-objects in every array", () => {
    const inventory = {
      status: "pass",
      clusters: [null, { cluster: "a" }],
      workloads: [null, "nope", { name: "real", pods: [null, { name: "p" }] }],
    } as unknown as ForgeClusterInventory;
    expect(workloadsOf(inventory)).toHaveLength(1);
    expect(podsOf(workloadsOf(inventory)[0])).toHaveLength(1);
    expect(findingsOf({ findings: [null, "", "real"] as unknown as string[] })).toEqual(["real"]);
  });

  it("rejects a workloads key that is an array rather than a document", () => {
    // A forge that regressed to a bare array must not be read as an inventory.
    expect(inventoryOf({ workloads: [] as unknown as ForgeClusterInventory })).toBeNull();
  });
});

describe("distinctScopes decides whether rows need to say where they are", () => {
  it("counts one for a single-cluster env and two when the env spans clusters", () => {
    expect(
      distinctScopes({
        status: "pass",
        workloads: [
          { name: "a", cluster: "k3d-a", namespace: "ns" },
          { name: "b", cluster: "k3d-a", namespace: "ns" },
        ],
      })
    ).toBe(1);
    expect(
      distinctScopes({
        status: "pass",
        workloads: [
          { name: "a", cluster: "k3d-a", namespace: "ns" },
          { name: "b", cluster: "k3d-b", namespace: "ns" },
        ],
      })
    ).toBe(2);
  });
});

describe("inventorySentence always names what was counted and where", () => {
  it("states the count with its cluster for a measured env", () => {
    const sentence = inventorySentence(
      {
        status: "pass",
        clusters: [{ cluster: "gke-prod", namespace: "cp-prod", status: "pass" }],
        workloads: [{ name: "a" }, { name: "b" }],
      },
      "prod"
    );
    expect(sentence).toContain("2 workloads");
    expect(sentence).toContain("gke-prod/cp-prod");
  });

  it("says unknown is neither healthy nor broken", () => {
    const sentence = inventorySentence(
      {
        status: "unknown",
        clusters: [{ cluster: "gke-prod", namespace: "cp-prod", status: "unknown" }],
        workloads: [{ name: "a" }],
      },
      "prod"
    );
    expect(sentence).toContain("could not read");
    expect(sentence).toContain("neither healthy nor broken");
  });

  it("refuses to let a missing key read as an empty environment", () => {
    expect(inventorySentence(null, "prod")).toContain("not a statement that prod deploys nothing");
  });
});
