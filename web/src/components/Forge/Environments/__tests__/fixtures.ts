// Copyright (c) 2025 Reliant Labs

/**
 * FIXTURES CAPTURED FROM A REAL CLUSTER, not invented.
 *
 * Both documents below were produced by running
 * `forge env status prod --json` against control-plane's actual GKE prod, at
 * the commit that introduced the `workloads` key. The reachable one is the
 * sixteen-workload reading; the unreachable one is the SAME command with an
 * empty KUBECONFIG, which is how the unknown posture was obtained rather than
 * guessed at.
 *
 * That provenance is the point. The bug these tests guard — a prod inventory
 * rendered from the host-process list — was invisible to a test suite whose
 * fixtures were written by the same person who wrote the renderer. Real output
 * carries the shapes nobody would think to invent: a Job that OMITS
 * `desired_replicas` rather than setting it to zero, and an unreachable
 * cluster that still lists all sixteen workloads with `ready_replicas: 0`.
 */

import type { ForgeEnvStatusReport } from "@/services/forge/status";
import type { ForgeClusterInventory } from "@/services/forge/workloads";

const PROD_CLUSTER = "gke_reliant-labs-475814_us-central1_prod";
const PROD_NAMESPACE = "control-plane-prod";

/** A healthy Deployment, as forge emits it. */
function deployment(
  name: string,
  desired: number,
  ready: number,
  restarts = 0
): ForgeClusterInventory["workloads"] extends (infer T)[] | undefined ? T : never {
  return {
    name,
    kind: "deployment",
    cluster: PROD_CLUSTER,
    namespace: PROD_NAMESPACE,
    status: ready >= desired ? "pass" : "fail",
    desired_replicas: desired,
    ready_replicas: ready,
    restarts,
    pods: Array.from({ length: ready }, (_, i) => ({
      name: `${name}-5c6d99d494-${i}abcd`,
      ready: true,
      containers_ready: 1,
      containers: 1,
      phase: "Running",
      restarts,
    })),
  };
}

/**
 * A Job, verbatim in the shape that matters: NO `desired_replicas` key at all,
 * `ephemeral: true`, and an empty pod list because it completed and was reaped.
 */
function job(name: string) {
  return {
    name,
    kind: "job",
    cluster: PROD_CLUSTER,
    namespace: PROD_NAMESPACE,
    status: "pass",
    ready_replicas: 0,
    restarts: 0,
    ephemeral: true,
    pods: [],
  };
}

/**
 * Control-plane prod as it really reads: 14 deployments + 2 jobs = 16
 * workloads, beside exactly 2 host services. The pair of numbers IS the bug
 * this screen fixes.
 */
export function prodReachable(): ForgeEnvStatusReport {
  return {
    env: "prod",
    // The host-process list, kept at its real size so a test can assert the
    // screen does not render THESE.
    services: [
      { name: "admin-server", kind: "host", port: 8080, listening: true },
      { name: "internal-console", kind: "frontend", port: 3000, listening: true },
    ],
    workloads: {
      status: "pass",
      env: "prod",
      clusters: [
        {
          cluster: PROD_CLUSTER,
          namespace: PROD_NAMESPACE,
          status: "pass",
          rendered_workloads: 16,
        },
      ],
      workloads: [
        deployment("admin-api", 1, 1),
        deployment("admin-server", 2, 2),
        job("control-plane-idp-provision-0ca6dbb170"),
        job("control-plane-migrate-aaa90288d6"),
        deployment("control-plane-workers", 1, 1),
        deployment("daemon-gateway", 1, 1, 1),
        deployment("internal-console", 1, 1),
        deployment("litellm", 1, 1),
        deployment("nats", 1, 1),
        deployment("reliant-api-server", 2, 2, 2),
        deployment("reliant-temporal-worker", 2, 2, 1),
        deployment("temporal", 1, 1),
        deployment("temporal-ui", 1, 1),
        deployment("workspace-controller", 2, 2),
        deployment("workspace-proxy", 2, 2),
        deployment("zitadel", 1, 1),
      ],
    },
  };
}

const KUBECTL_ERROR = `kubectl --context ${PROD_CLUSTER}: error: context "${PROD_CLUSTER}" does not exist`;

/**
 * The SAME prod, unreadable. Status unknown, and every one of the sixteen
 * workloads is STILL listed — each with status unknown, ready_replicas 0 and
 * no pods. This is the fixture that catches a renderer which branches on
 * `workloads.length` instead of on `status`: such a renderer paints sixteen
 * healthy-looking rows with 0/1 ready, which reads as a total outage rather
 * than as an unread cluster.
 */
export function prodUnreachable(): ForgeEnvStatusReport {
  const base = prodReachable();
  const inventory = base.workloads as ForgeClusterInventory;
  return {
    ...base,
    workloads: {
      status: "unknown",
      env: "prod",
      clusters: [
        {
          cluster: PROD_CLUSTER,
          namespace: PROD_NAMESPACE,
          status: "unknown",
          error: KUBECTL_ERROR,
          rendered_workloads: 16,
        },
      ],
      workloads: (inventory.workloads ?? []).map((workload) => ({
        ...workload,
        status: "unknown",
        ready_replicas: 0,
        restarts: 0,
        pods: [],
        findings: [
          `could not list pods on ${PROD_CLUSTER}/${PROD_NAMESPACE}: ${KUBECTL_ERROR}`,
        ],
      })),
    },
  };
}

export const PROD_UNREACHABLE_ERROR = KUBECTL_ERROR;

/** An env that genuinely deploys nothing: pass, and no workloads. */
export function deploysNothing(): ForgeEnvStatusReport {
  return {
    env: "e2e",
    services: [],
    workloads: { status: "pass", env: "e2e", clusters: [], workloads: [] },
  };
}

/** A forge predating the workloads key. The key is absent entirely. */
export function noWorkloadsKey(): ForgeEnvStatusReport {
  return {
    env: "prod",
    services: [{ name: "admin-server", kind: "host", port: 8080, listening: true }],
  };
}

/**
 * control-plane's real dev: TWO clusters (k3d-control-plane and k3d-cp-daemon)
 * under one environment, which is what makes a per-row cluster column
 * necessary rather than decorative. Plus an unrouted workload — empty
 * `cluster` — which forge emits when the render does not say where something
 * deploys, and which must never be attributed to a current context.
 */
export function devTwoClusters(): ForgeEnvStatusReport {
  return {
    env: "dev",
    services: [],
    workloads: {
      status: "warn",
      env: "dev",
      clusters: [
        {
          cluster: "k3d-control-plane",
          namespace: "control-plane-dev",
          status: "pass",
          rendered_workloads: 3,
        },
        {
          cluster: "k3d-cp-daemon",
          namespace: "control-plane-dev",
          status: "pass",
          rendered_workloads: 1,
        },
      ],
      workloads: [
        {
          name: "daemon-gateway",
          kind: "deployment",
          cluster: "k3d-control-plane",
          namespace: "control-plane-dev",
          status: "pass",
          desired_replicas: 1,
          ready_replicas: 1,
          restarts: 0,
          pods: [
            {
              name: "daemon-gateway-5c7c557f67-wpsjv",
              ready: true,
              containers_ready: 1,
              containers: 1,
              phase: "Running",
              restarts: 0,
            },
          ],
        },
        {
          name: "workspace-controller",
          kind: "deployment",
          cluster: "k3d-cp-daemon",
          namespace: "control-plane-dev",
          status: "fail",
          desired_replicas: 1,
          ready_replicas: 0,
          restarts: 37,
          pods: [
            {
              name: "workspace-controller-68f4b78b66-8hz6j",
              ready: false,
              containers_ready: 0,
              containers: 1,
              phase: "Running",
              restarts: 37,
            },
          ],
          findings: ["0/1 Ready CrashLoopBackOff last=OOMKilled(exit 137) restarts=37"],
        },
        {
          name: "orphaned-worker",
          kind: "deployment",
          cluster: "",
          namespace: "",
          status: "unknown",
          desired_replicas: 1,
          ready_replicas: 0,
          restarts: 0,
          pods: [],
        },
      ],
    },
  };
}
