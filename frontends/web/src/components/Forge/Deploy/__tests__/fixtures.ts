// Copyright (c) 2025 Reliant Labs

/**
 * Deploy fixtures, built from REAL forge output shapes.
 *
 * NOTHING IN THIS DIRECTORY REACHES A DAEMON, A CLUSTER, OR A REAL PROJECT, and
 * for this surface that is not test hygiene — it is the whole safety story. A
 * deploy applies manifests to a live cluster; control-plane's prod declares
 * gke_reliant-labs-475814_us-central1_prod, and unlike a promote there is nothing
 * in git to restore.
 *
 * The fixtures mirror the reference project's actual states, because the nesting
 * is the part a hand-written fixture tends to flatten:
 *
 *   prodPlan       one cluster, digest-pinned, clean preflight. The ordinary case.
 *   devPlan        TWO declared clusters (k3d-control-plane and k3d-cp-daemon) —
 *                  control-plane's dev env really does — plus the two BLOCKING
 *                  preflight findings this worktree's dev env produces from its
 *                  unprovisioned secrets, which make `dry-run` exit 1.
 *   taggedPlan     an image shipping by mutable tag, as prod's internal-console
 *                  does today with `:latest`.
 */

import { ForgeReachability } from "@/gen/reliant/v1/forge_pb";
import type { ForgeReportMeta } from "@/gen/reliant/v1/forge_pb";
import type { ForgeDeployReport, DeployRefusal } from "@/services/forge/deploy";
import type { ForgeOutcome } from "@/services/forge/topology";

export function meta(overrides: Partial<ForgeReportMeta> = {}): ForgeReportMeta {
  return {
    isForgeProject: true,
    supported: true,
    forgeVersion: "v0.1.15",
    unsupportedReason: "",
    exitCode: 0,
    reachability: ForgeReachability.UNSPECIFIED,
    unreachableReason: "",
    ...overrides,
  } as ForgeReportMeta;
}

export function planOutcome(plan: ForgeDeployReport): ForgeOutcome<ForgeDeployReport> {
  return { kind: "report", meta: meta(), report: plan };
}

/**
 * prod: ONE cluster, digest-pinned, clean preflight, rollout mode wait. The plan
 * a confirm is legitimately offered from.
 *
 * Note guard.current_context is a DIFFERENT cluster from declared_context, which
 * is the ordinary situation — your kubectl points at k3d while you deploy to GKE —
 * and is exactly the pair a UI must not mix up.
 */
export function prodPlan(overrides: Partial<ForgeDeployReport> = {}): ForgeDeployReport {
  return {
    env: "prod",
    mode: "dry_run",
    guard: {
      declared_context: "gke_reliant-labs-475814_us-central1_prod",
      current_context: "k3d-control-plane",
      verdict: "allow",
      reason: "context_declared",
    },
    target: {
      kube_context: "gke_reliant-labs-475814_us-central1_prod",
      namespace: "control-plane-prod",
      all_kube_contexts: ["gke_reliant-labs-475814_us-central1_prod"],
    },
    release: "v1.5.15",
    image_tag: "v1.5.15",
    tag_source: "release",
    preflight: { status: "ran", findings: [], blocking: 0 },
    images: {
      images: [
        {
          reference: "us-central1-docker.pkg.dev/p/r/admin-server@sha256:abc123",
          repository: "us-central1-docker.pkg.dev/p/r/admin-server",
          pinning: "digest",
        },
      ],
      digest_count: 1,
      tag_count: 0,
      no_digest_requested: false,
    },
    resources: [{ api_version: "apps/v1", kind: "Deployment", name: "admin-server" }],
    rollout: {
      mode: "wait",
      timeout_seconds: 300,
      results: [],
      ready: 0,
      failed: 0,
      timed_out: 0,
      not_waited: 0,
    },
    ok: true,
    exit_code: 0,
    ...overrides,
  };
}

/**
 * dev: TWO declared clusters, and TWO BLOCKING preflight findings.
 *
 * Both halves are real. control-plane's dev env declares k3d-control-plane and
 * k3d-cp-daemon, each group applying to its own context — and an earlier version
 * of the backend reported only the env-wide one, which would have let a dialog
 * omit a cluster the deploy was about to write to. The findings are this
 * worktree's own unprovisioned secrets, which make `forge env deploy dev
 * --dry-run` exit 1.
 */
export function devPlan(overrides: Partial<ForgeDeployReport> = {}): ForgeDeployReport {
  return {
    env: "dev",
    mode: "dry_run",
    guard: {
      declared_context: "k3d-control-plane",
      current_context: "k3d-control-plane",
      verdict: "allow",
      reason: "context_declared",
    },
    target: {
      kube_context: "k3d-control-plane",
      namespace: "control-plane-dev",
      all_kube_contexts: ["k3d-control-plane", "k3d-cp-daemon"],
    },
    preflight: {
      status: "ran",
      blocking: 2,
      findings: [
        {
          check: "missing_secret_key",
          subject: "control-plane-dev/admin-server-secrets",
          keys: ["STRIPE_SECRET_KEY"],
          detail: "the Secret exists but does not carry this key",
          blocking: true,
        },
        {
          check: "missing_secret_key",
          subject: "control-plane-dev/litellm-secrets",
          keys: ["LITELLM_MASTER_KEY"],
          blocking: true,
        },
        {
          check: "image_arch",
          subject: "ghcr.io/org/api:dev",
          detail: "the image's architecture could not be read — advisory only",
          blocking: false,
        },
      ],
    },
    images: {
      images: [
        {
          reference: "ghcr.io/org/admin-server@sha256:def456",
          repository: "ghcr.io/org/admin-server",
          pinning: "digest",
        },
      ],
      digest_count: 1,
      tag_count: 0,
      no_digest_requested: false,
    },
    resources: [{ api_version: "apps/v1", kind: "Deployment", name: "admin-server" }],
    rollout: {
      mode: "wait",
      timeout_seconds: 300,
      results: [],
      ready: 0,
      failed: 0,
      timed_out: 0,
      not_waited: 0,
    },
    ok: false,
    exit_code: 1,
    ...overrides,
  };
}

/** prod, but internal-console ships `:latest` — a mutable tag, as it really does. */
export function taggedPlan(overrides: Partial<ForgeDeployReport> = {}): ForgeDeployReport {
  return prodPlan({
    images: {
      images: [
        {
          reference: "us-central1-docker.pkg.dev/p/r/admin-server@sha256:abc123",
          repository: "us-central1-docker.pkg.dev/p/r/admin-server",
          pinning: "digest",
        },
        {
          reference: "us-central1-docker.pkg.dev/p/r/internal-console:latest",
          repository: "us-central1-docker.pkg.dev/p/r/internal-console",
          pinning: "tag",
        },
      ],
      digest_count: 1,
      tag_count: 1,
      no_digest_requested: false,
    },
    ...overrides,
  });
}

/** An env that declares no cluster: host-only / compose. No token can come from it. */
export function noClusterPlan(overrides: Partial<ForgeDeployReport> = {}): ForgeDeployReport {
  return prodPlan({
    env: "local",
    guard: {
      declared_context: "",
      current_context: "k3d-control-plane",
      verdict: "allow",
      reason: "no_cluster_declared",
    },
    target: { kube_context: "", namespace: "control-plane-local", all_kube_contexts: [] },
    ...overrides,
  });
}

/** forge's own guard refusing: the declared context is not in the kubeconfig. */
export function guardRefusedPlan(overrides: Partial<ForgeDeployReport> = {}): ForgeDeployReport {
  return prodPlan({
    guard: {
      declared_context: "gke_reliant-labs-475814_us-central1_prod",
      current_context: "k3d-control-plane",
      verdict: "refuse",
      reason: "declared_context_missing",
      fix: "Run: gcloud container clusters get-credentials prod --region us-central1",
      available_contexts: ["k3d-control-plane", "k3d-cp-daemon"],
    },
    ok: false,
    exit_code: 1,
    ...overrides,
  });
}

/**
 * A FINISHED apply report, parameterised by rollout so the three-outcome rule can
 * be driven directly. mode is "apply" — bytes moved.
 */
export function appliedReport(
  rollout: ForgeDeployReport["rollout"],
  overrides: Partial<ForgeDeployReport> = {}
): ForgeDeployReport {
  return prodPlan({ mode: "apply", rollout, ...overrides });
}

export function refusal(overrides: Partial<DeployRefusal> = {}): DeployRefusal {
  return {
    reason: "stale-declared-context",
    detail: "prod now declares k3d-control-plane, not gke_reliant-labs-475814_us-central1_prod",
    expectedDeclaredContext: "gke_reliant-labs-475814_us-central1_prod",
    actualDeclaredContext: "k3d-control-plane",
    expectedCurrentRelease: "v1.5.15",
    expectedUnbound: false,
    actualCurrentRelease: "v1.5.15",
    actualBound: true,
    guardVerdict: "",
    guardReason: "",
    guardFix: "",
    runningHandle: "",
    ...overrides,
  };
}
