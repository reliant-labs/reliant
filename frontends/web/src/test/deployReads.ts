// Copyright (c) 2026 Reliant Labs

/**
 * DeployService's per-environment reads (GetEnvironmentShape, GetStatus) as
 * mockTransport handlers, for tests that describe what an environment
 * declares and runs in the console's own view models (LiveShape,
 * CloudEnvStatus). Each view model is encoded as the wire message the control
 * plane sends, so the generated hooks and the real mapping
 * (services/forge/live.ts toLiveShape, cloudEnvs.ts toCloudEnvStatus) read it
 * back the way they read a real answer.
 */

import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { JsonObject } from "@bufbuild/protobuf";

import {
  DeployCustomDomainState,
  DeployObservedState,
  DeployRunState,
  DeployServingPath,
  DeploySuspendReason,
  DeployTier,
  DeployVerdict,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";
import type { GetDeployEnvironmentShapeRequest, GetDeploymentStatusRequest } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import type { MockHandlers } from "@/lib/test-utils";
import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import type { LiveShape } from "@/services/forge/live";
import type { ForgeHostedWorkload, HostedDomain } from "@/services/forge/topology";

/** A declared shape as a test writes it: every LiveShape field optional. */
export type ShapeLike = Partial<LiveShape>;

/** `shape` as GetEnvironmentShape's `declared_shape` carries it: forge's snake_case render document. */
export function wireShape(shape: ShapeLike): JsonObject {
  const out: JsonObject = {};
  if (shape.kind !== undefined) out.kind = shape.kind;
  if (shape.workloads) out.workloads = shape.workloads.map((w) => ({ name: w.name, runtime: w.runtime, cluster: w.cluster }));
  if (shape.secrets) out.secrets = shape.secrets.map((s) => ({ name: s.name, provider: s.provider, declared_by: [...s.declaredBy] }));
  if (shape.domains) out.domains = [...shape.domains];
  if (shape.clusters) out.clusters = [...shape.clusters];
  if (shape.alerts) {
    out.alerts = shape.alerts.map((rule) => ({
      alert: rule.alert,
      expr: rule.expr,
      for: rule.for,
      interval: rule.interval,
      keep_firing_for: rule.keepFiringFor,
      labels: rule.labels,
      annotations: rule.annotations,
      source: rule.source,
      default_id: rule.defaultId,
    }));
  }
  if (shape.alertRoutes) out.alert_routes = shape.alertRoutes.map((r) => ({ match: r.match, receiver: r.receiver, continue: r.continue }));
  if (shape.observability) out.observability_policy = { enabled: shape.observability.enabled, disabled: [...shape.observability.disabled] };
  return out;
}

/** GetEnvironmentShape answering `shapeOf(environmentId)`; null is "never declared" (no shape). */
export function environmentShapeHandlers(shapeOf: (environmentId: string) => ShapeLike | null | Promise<ShapeLike | null>): MockHandlers {
  return {
    "controlplane.v1.DeployService/GetEnvironmentShape": async (req: unknown) => {
      const shape = await shapeOf((req as GetDeployEnvironmentShapeRequest).environmentId);
      return shape ? { declaredShape: wireShape(shape) } : {};
    },
  };
}

/** Enum value by its lower-case or kebab-case name: `"pending-dns"` → `PENDING_DNS`. */
function enumOf<E extends Record<string, string | number>>(e: E, name: string | undefined): number {
  const value = e[(name ?? "").toUpperCase().replace(/-/g, "_") as keyof E];
  return typeof value === "number" ? value : 0;
}

function wireDomain(domain: HostedDomain) {
  return {
    domain: domain.hostname,
    state: enumOf(DeployCustomDomainState, domain.state),
    servingPath: enumOf(DeployServingPath, domain.serving_path),
  };
}

function wireWorkload(w: ForgeHostedWorkload) {
  return {
    verdict: enumOf(DeployVerdict, w.verdict),
    verdictReason: w.verdict_reason ?? "",
    desiredDigest: w.desired_digest ?? "",
    drifted: w.drifted === true,
    deployment: {
      id: w.deployment_id ?? "",
      name: w.name ?? "",
      tier: enumOf(DeployTier, w.tier),
      runState: enumOf(DeployRunState, w.declared_run_state),
      observed: {
        state: enumOf(DeployObservedState, w.observed_state),
        url: w.url ?? "",
        imageDigest: w.observed_digest ?? "",
        lastError: w.last_error ?? "",
        suspendReason: enumOf(DeploySuspendReason, w.suspend_reason),
        domains: (w.domains ?? []).map(wireDomain),
      },
    },
  };
}

/** A status as a test writes it; `currentPromotion` is not encoded (no test here reads one). */
export type StatusLike = Pick<CloudEnvStatus, "verdict" | "workloads"> & Partial<CloudEnvStatus>;

/** `status` as GetStatus answers it. */
export function wireStatus(status: StatusLike) {
  const observedAt = status.observedAt ? timestampFromDate(new Date(status.observedAt)) : undefined;
  return {
    environmentVerdict: enumOf(DeployVerdict, status.verdict),
    deployments: status.workloads.map((w) => ({ ...wireWorkload(w), observedAt })),
  };
}

/** GetStatus answering `statusOf(request)`. */
export function environmentStatusHandlers(statusOf: (req: GetDeploymentStatusRequest) => StatusLike | Promise<StatusLike>): MockHandlers {
  return {
    "controlplane.v1.DeployService/GetStatus": async (req: unknown) => wireStatus(await statusOf(req as GetDeploymentStatusRequest)),
  };
}
