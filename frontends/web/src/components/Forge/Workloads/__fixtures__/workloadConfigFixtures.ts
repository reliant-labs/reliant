// Copyright (c) 2025 Reliant Labs
import { Code, ConnectError } from "@connectrpc/connect";
import { dump } from "js-yaml";
import type { JsonObject } from "@bufbuild/protobuf";
import type { ConfigReader, WorkloadConfig } from "@/services/forge/workloadConfig";

/** Only fake server-redacted bytes; the preview never reaches a real values endpoint. */
export function createConfigFixtureReader(state: "values" | "denied" | "unsupported" = "values"): ConfigReader {
  return async (request, signal): Promise<WorkloadConfig> => {
    if (signal.aborted) throw new ConnectError("cancelled", Code.Canceled);
    if (state === "denied") throw new ConnectError("config read denied", Code.PermissionDenied);
    if (state === "unsupported") throw new ConnectError("old server", Code.Unimplemented);
    const historical = !!request.releaseVersion;
    const mode = historical ? "previous" : "production";
    const objects: JsonObject[] = [
      { apiVersion: "v1", kind: "ConfigMap", metadata: { name: request.workload }, data: { MODE: mode, TOKEN: "forge.dev/redacted: withheld" } },
      { apiVersion: "apps/v1", kind: "Deployment", metadata: { name: request.workload }, spec: { template: { spec: { containers: [{ name: request.workload, env: [{ name: "DB_PASSWORD", valueFrom: { secretKeyRef: { name: "database", key: "password" } } }] }] } } } },
    ];
    return {
      environmentId: request.environmentId,
      workload: request.workload,
      bundleId: request.bundleId ?? (historical ? "fixture-historical-bundle" : "fixture-current-bundle"),
      bundleDigest: `sha256:${(historical ? "a" : "b").repeat(64)}`,
      releaseVersion: request.releaseVersion ?? "v1.42.0",
      objects,
      renderedYaml: objects.map((object) => dump(object, { noRefs: true })).join("---\n"),
      frontendConfigUnsupportedReason: "Static frontend runtime config requires a verified artifact resolver; no URL is fetched.",
    };
  };
}
