// Copyright (c) 2025 Reliant Labs

/** Values never enter a query cache. Only an explicit user action calls this adapter. */
import type { JsonObject } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";

export type ConfigSelector = { bundleId: string; releaseVersion?: never } | { releaseVersion: string; bundleId?: never } | { bundleId?: never; releaseVersion?: never };
export type WorkloadConfigRequest = ConfigSelector & { environmentId: string; workload: string; orgId?: string };
export interface WorkloadConfig {
  environmentId: string;
  workload: string;
  bundleId: string;
  /** Verified immutable OCI manifest digest, not a secret fingerprint. Empty on older servers. */
  bundleDigest: string;
  releaseVersion: string;
  objects: JsonObject[];
  renderedYaml: string;
  frontendConfigUnsupportedReason: string;
}
export type ConfigReader = (request: WorkloadConfigRequest, signal: AbortSignal) => Promise<WorkloadConfig>;

/** Reject wrong-scope/stale responses before rendering or offering copy/download. Server authorization remains authoritative. */
export function validateWorkloadConfig(result: WorkloadConfig, request: WorkloadConfigRequest): WorkloadConfig {
  if (result.environmentId !== request.environmentId || result.workload !== request.workload || !result.bundleId ||
      (request.bundleId !== undefined && result.bundleId !== request.bundleId) ||
      (request.releaseVersion !== undefined && result.releaseVersion !== request.releaseVersion) ||
      !Array.isArray(result.objects) || result.objects.some((object) => !object || typeof object !== "object" || Array.isArray(object)) ||
      typeof result.renderedYaml !== "string" || typeof result.frontendConfigUnsupportedReason !== "string") {
    throw new Error("Configuration response did not match the requested environment, workload or bundle.");
  }
  // A legacy bundle redaction may carry a value-derived digest. The viewer
  // contract promises fixed withheld markers; refuse the whole response if
  // that promise is violated, including the copy/download encoding.
  if (/forge\.dev\/redacted:\s*sha256:/i.test(result.renderedYaml) || /forge\.dev\/redacted:\s*sha256:/i.test(JSON.stringify(result.objects))) {
    throw new Error("Configuration uses unsupported secret redaction markers.");
  }
  return result;
}

export const getWorkloadConfig: ConfigReader = async (request, signal) => {
  const { environmentId, workload, bundleId, releaseVersion, orgId } = request;
  if (!environmentId || !workload || (bundleId !== undefined && !bundleId) || (releaseVersion !== undefined && !releaseVersion) || (bundleId !== undefined && releaseVersion !== undefined)) {
    throw new Error("Choose an environment, workload and at most one nonempty bundle or release selector.");
  }
  const result = await getControlPlaneClient(DeployService).getWorkloadConfig({ environmentId, workload, bundleId, releaseVersion, ...(orgId ? { orgId } : {}) }, { signal });
  return validateWorkloadConfig({
    environmentId: result.environmentId,
    workload: result.workload,
    bundleId: result.bundleId,
    bundleDigest: result.bundleDigest,
    releaseVersion: result.releaseVersion,
    objects: result.objects,
    renderedYaml: result.renderedYaml,
    frontendConfigUnsupportedReason: result.frontendConfigUnsupportedReason,
  }, request);
};

/** Do not echo arbitrary transport details: an intermediary could quote sensitive bytes. */
export function configReadError(error: unknown): string {
  switch (ConnectError.from(error).code) {
    case Code.PermissionDenied:
    case Code.Unauthenticated:
      return "Config values require deploy:read and deploy:config:read. Ask an organization administrator for access. The names-only view remains available.";
    case Code.Unimplemented:
      return "This server does not support configuration values yet. The names-only view remains available.";
    case Code.NotFound:
      return "No configuration was found for this workload and release in this environment. Nothing from another environment is shown.";
    case Code.FailedPrecondition:
      return "This recorded bundle cannot be read or verified by the server. No artifact is downloaded by your browser.";
    default:
      return "Configuration could not be read. Try again; the names-only view remains available.";
  }
}
