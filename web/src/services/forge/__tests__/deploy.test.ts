// Copyright (c) 2025 Reliant Labs

/**
 * THE DECODERS AND THE TOKEN, tested directly.
 *
 * Every enum reader here falls back in ONE safe direction, and these tests assert
 * the direction rather than the happy path — a decoder that lands on `ready`,
 * `allow`, `digest` or `ran` for a value it does not recognise is the whole
 * failure mode this module is shaped against.
 */

import { describe, expect, it } from "vitest";

import {
  blockingFindings,
  deployBlockers,
  deployCertainty,
  deployModeOf,
  deployModeWrites,
  deployTokenFor,
  describeDeployToken,
  guardVerdictOf,
  isMultiCluster,
  pinningOf,
  preflightStatusOf,
  rolloutCertainty,
  rolloutModeOf,
  rolloutStateOf,
  rolloutTally,
  targetContexts,
  type ForgeDeployReport,
} from "../deploy";

describe("every decoder falls back safely", () => {
  it("reads a mode, never defaulting a real apply to a preview", () => {
    expect(deployModeOf("apply")).toBe("apply");
    expect(deployModeOf("dry_run")).toBe("dry_run");
    expect(deployModeOf("rollback")).toBe("rollback");
    // A mode from a newer forge must not read as one of the read-only ones.
    expect(deployModeOf("reconcile")).toBe("unknown");
    expect(deployModeOf(undefined)).toBe("unknown");
    expect(deployModeOf("reconcile")).not.toBe("dry_run");

    expect(deployModeWrites("apply")).toBe(true);
    expect(deployModeWrites("rollback")).toBe(true);
    expect(deployModeWrites("dry_run")).toBe(false);
    expect(deployModeWrites("unknown")).toBe(false);
  });

  it("reads a guard verdict, NEVER defaulting to allow", () => {
    expect(guardVerdictOf("allow")).toBe("allow");
    expect(guardVerdictOf("refuse")).toBe("refuse");
    // An unevaluated or unrecognised guard must not read as permission.
    expect(guardVerdictOf(undefined)).toBe("unknown");
    expect(guardVerdictOf("maybe")).toBe("unknown");
    expect(guardVerdictOf("maybe")).not.toBe("allow");
  });

  it("reads pinning, NEVER defaulting to digest", () => {
    expect(pinningOf("digest")).toBe("digest");
    expect(pinningOf("tag")).toBe("tag");
    // Claiming digest-pinned would assert a safety property forge never verified.
    expect(pinningOf("oci-ref")).toBe("unknown");
    expect(pinningOf(undefined)).not.toBe("digest");
  });

  it("reads a preflight status, NEVER defaulting to ran", () => {
    expect(preflightStatusOf("ran")).toBe("ran");
    expect(preflightStatusOf("skipped_flag")).toBe("skipped_flag");
    // An unchecked deploy must not present as a checked one.
    expect(preflightStatusOf("skipped_future_reason")).toBe("unknown");
    expect(preflightStatusOf(undefined)).not.toBe("ran");
  });

  it("reads a rollout mode, NEVER defaulting to wait", () => {
    expect(rolloutModeOf("skip")).toBe("skip");
    expect(rolloutModeOf("warn")).toBe("warn");
    // Naming a mode forge did not run would misrepresent how hard it looked.
    expect(rolloutModeOf("eventually")).toBe("unknown");
    expect(rolloutModeOf(undefined)).not.toBe("wait");
  });

  it("reads a rollout state, NEVER defaulting to ready", () => {
    expect(rolloutStateOf("ready")).toBe("ready");
    expect(rolloutStateOf("failed")).toBe("failed");
    expect(rolloutStateOf("timed_out")).toBe("timed_out");
    expect(rolloutStateOf("not_waited")).toBe("not_waited");
    // THE one wrong answer: a state that fell back to ready would report a stuck
    // rollout as healthy.
    expect(rolloutStateOf("converging")).toBe("unknown");
    expect(rolloutStateOf("converging")).not.toBe("ready");
    expect(rolloutStateOf(undefined)).not.toBe("ready");
  });

  it("groups the four states into three certainty levels", () => {
    expect(rolloutCertainty("ready")).toBe("known-good");
    expect(rolloutCertainty("failed")).toBe("known-bad");
    // The three that establish nothing share a level and must not borrow either
    // certain one.
    expect(rolloutCertainty("timed_out")).toBe("unknown");
    expect(rolloutCertainty("not_waited")).toBe("unknown");
    expect(rolloutCertainty("unknown")).toBe("unknown");
  });
});

describe("targetContexts", () => {
  it("returns every declared cluster, sorted and de-duplicated", () => {
    const report: ForgeDeployReport = {
      guard: { declared_context: "k3d-control-plane" },
      target: {
        kube_context: "k3d-control-plane",
        all_kube_contexts: ["k3d-cp-daemon", "k3d-control-plane"],
      },
    };
    expect(targetContexts(report)).toEqual(["k3d-control-plane", "k3d-cp-daemon"]);
    expect(isMultiCluster(report)).toBe(true);
  });

  it("folds in a context that only kube_context or the guard carries", () => {
    // Under-reporting the blast radius is the one error this must not make: the
    // env-wide context and the guard's arrive by different routes in forge.
    expect(
      targetContexts({
        guard: { declared_context: "gke-prod" },
        target: { kube_context: "", all_kube_contexts: [] },
      })
    ).toEqual(["gke-prod"]);

    expect(
      targetContexts({
        guard: {},
        target: { kube_context: "gke-prod", all_kube_contexts: ["gke-other"] },
      })
    ).toEqual(["gke-other", "gke-prod"]);
  });

  it("never reports the ambient current_context as a target", () => {
    const contexts = targetContexts({
      guard: { declared_context: "gke-prod", current_context: "k3d-control-plane" },
      target: { kube_context: "gke-prod", all_kube_contexts: ["gke-prod"] },
    });
    expect(contexts).toEqual(["gke-prod"]);
    expect(contexts).not.toContain("k3d-control-plane");
  });

  it("returns nothing for an env that declares no cluster", () => {
    expect(targetContexts({ guard: {}, target: {} })).toEqual([]);
    expect(isMultiCluster({ guard: {}, target: {} })).toBe(false);
    expect(targetContexts(null)).toEqual([]);
  });
});

describe("blockers", () => {
  const clean: ForgeDeployReport = {
    mode: "dry_run",
    guard: { declared_context: "gke-prod", verdict: "allow" },
    target: { kube_context: "gke-prod", all_kube_contexts: ["gke-prod"] },
    release: "v1.5.15",
    preflight: { status: "ran", findings: [], blocking: 0 },
  };

  it("finds none on a clean dry-run plan", () => {
    expect(deployBlockers(clean)).toEqual([]);
  });

  it("reads blocking off each finding, not off the section's count", () => {
    const report: ForgeDeployReport = {
      ...clean,
      preflight: {
        status: "ran",
        // The count deliberately disagrees: the findings are what justify a stop.
        blocking: 0,
        findings: [
          { check: "missing_secret_key", blocking: true },
          { check: "image_arch", blocking: false },
        ],
      },
    };
    expect(blockingFindings(report)).toHaveLength(1);
    expect(deployBlockers(report).map((blocker) => blocker.kind)).toContain("preflight-blocking");
  });

  it("reports ALL blockers, not the first", () => {
    const kinds = deployBlockers({
      mode: "apply",
      guard: { declared_context: "", verdict: "refuse" },
      target: {},
      preflight: { status: "ran", findings: [{ check: "missing_image", blocking: true }] },
    }).map((blocker) => blocker.kind);

    expect(kinds).toContain("not-a-preview");
    expect(kinds).toContain("guard-refused");
    expect(kinds).toContain("no-declared-cluster");
    expect(kinds).toContain("preflight-blocking");
  });

  it("treats a missing document as unconfirmable", () => {
    expect(deployBlockers(null)).toHaveLength(1);
  });
});

describe("deployTokenFor", () => {
  const plan: ForgeDeployReport = {
    env: "prod",
    mode: "dry_run",
    guard: { declared_context: "gke-prod", current_context: "k3d-control-plane", verdict: "allow" },
    target: { kube_context: "gke-prod", all_kube_contexts: ["gke-prod"] },
    release: "v1.5.15",
  };

  it("takes the DECLARED context, never the ambient one", () => {
    const token = deployTokenFor(plan);
    expect(token).toEqual({
      expectedDeclaredContext: "gke-prod",
      expectedCurrentRelease: "v1.5.15",
    });
    expect(token?.expectedDeclaredContext).not.toBe("k3d-control-plane");
  });

  it("asserts expectUnbound for an env with no binding", () => {
    expect(deployTokenFor({ ...plan, release: "" })).toEqual({
      expectedDeclaredContext: "gke-prod",
      expectUnbound: true,
    });
    expect(deployTokenFor({ ...plan, release: undefined })).toEqual({
      expectedDeclaredContext: "gke-prod",
      expectUnbound: true,
    });
  });

  it("returns null rather than guessing, in every case that cannot support a claim", () => {
    // Not a preview.
    expect(deployTokenFor({ ...plan, mode: "apply" })).toBeNull();
    expect(deployTokenFor({ ...plan, mode: "rollback" })).toBeNull();
    expect(deployTokenFor({ ...plan, mode: "explain" })).toBeNull();
    // Forge's guard refused.
    expect(
      deployTokenFor({ ...plan, guard: { declared_context: "gke-prod", verdict: "refuse" } })
    ).toBeNull();
    // No cluster to name — and the request has no "unspecified" spelling.
    expect(deployTokenFor({ ...plan, guard: { declared_context: "", verdict: "allow" } })).toBeNull();
    expect(deployTokenFor({ ...plan, guard: { declared_context: "   ", verdict: "allow" } })).toBeNull();
    expect(deployTokenFor(null)).toBeNull();
  });

  it("does NOT gate on blocking preflight findings — those are a separate question", () => {
    // The claim is perfectly makeable; the deploy would simply fail. Conflating
    // them would give the operator the wrong copy.
    const token = deployTokenFor({
      ...plan,
      preflight: { status: "ran", findings: [{ check: "missing_secret_key", blocking: true }] },
    });
    expect(token).not.toBeNull();
    // And the blocker list is what stops it.
    expect(
      deployBlockers({
        ...plan,
        preflight: { status: "ran", findings: [{ check: "missing_secret_key", blocking: true }] },
      })
    ).toHaveLength(1);
  });

  it("describes the claim with the cluster first", () => {
    const described = describeDeployToken(deployTokenFor(plan)!);
    expect(described).toContain("gke-prod");
    expect(described).toContain("v1.5.15");
    expect(described.indexOf("gke-prod")).toBeLessThan(described.indexOf("v1.5.15"));

    expect(describeDeployToken(deployTokenFor({ ...plan, release: "" })!)).toMatch(
      /no release binding/i
    );
  });
});

describe("the deploy's verdict", () => {
  function withRollout(
    results: Array<{ kind: string; name: string; state: string }>,
    overrides: Partial<ForgeDeployReport> = {}
  ): ForgeDeployReport {
    return {
      mode: "apply",
      ok: true,
      rollout: { mode: "wait", results, ready: 0, failed: 0, timed_out: 0, not_waited: 0 },
      ...overrides,
    };
  }

  it("is known-good only when every resource became ready and forge exited zero", () => {
    expect(
      deployCertainty(
        withRollout([
          { kind: "Deployment", name: "a", state: "ready" },
          { kind: "Deployment", name: "b", state: "ready" },
        ])
      )
    ).toBe("known-good");
  });

  it("is known-bad on a failure, checked before any unknown", () => {
    // A failure alongside an unknown is still a failure.
    expect(
      deployCertainty(
        withRollout([
          { kind: "Deployment", name: "a", state: "failed" },
          { kind: "Deployment", name: "b", state: "timed_out" },
        ])
      )
    ).toBe("known-bad");
    expect(deployCertainty(withRollout([], { ok: false }))).toBe("known-bad");
  });

  it("is UNKNOWN for a timeout, never success", () => {
    expect(
      deployCertainty(
        withRollout([
          { kind: "Deployment", name: "a", state: "ready" },
          { kind: "Deployment", name: "b", state: "timed_out" },
        ])
      )
    ).toBe("unknown");
  });

  it("is UNKNOWN under rollout mode skip, even though forge exited zero", () => {
    // Every resource is legitimately not_waited — and nothing was established.
    const report = withRollout([{ kind: "Deployment", name: "a", state: "not_waited" }], {
      rollout: {
        mode: "skip",
        results: [{ kind: "Deployment", name: "a", state: "not_waited" }],
        ready: 0,
        failed: 0,
        timed_out: 0,
        not_waited: 1,
      },
    });
    expect(deployCertainty(report)).toBe("unknown");
    expect(deployCertainty(report)).not.toBe("known-good");
  });

  it("is UNKNOWN for a report that waited on nothing at all", () => {
    expect(deployCertainty(withRollout([]))).toBe("unknown");
    expect(deployCertainty(null)).toBe("unknown");
  });

  it("is UNKNOWN for a state this build does not recognise", () => {
    expect(deployCertainty(withRollout([{ kind: "Deployment", name: "a", state: "future" }]))).toBe(
      "unknown"
    );
  });

  it("tallies from the rendered results, falling back to the flat counts", () => {
    expect(
      rolloutTally(
        withRollout([
          { kind: "Deployment", name: "a", state: "ready" },
          { kind: "Deployment", name: "b", state: "timed_out" },
          { kind: "Deployment", name: "c", state: "future" },
        ])
      )
    ).toEqual({ ready: 1, failed: 0, timedOut: 1, notWaited: 0, unknown: 1, total: 3 });

    expect(
      rolloutTally({
        rollout: { mode: "wait", results: [], ready: 2, failed: 1, timed_out: 0, not_waited: 3 },
      })
    ).toEqual({ ready: 2, failed: 1, timedOut: 0, notWaited: 3, unknown: 0, total: 6 });
  });
});
