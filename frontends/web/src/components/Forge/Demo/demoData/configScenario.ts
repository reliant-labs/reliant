// Copyright (c) 2025 Reliant Labs

/** Recorded, synthetic configuration: one immutable projection for YAML/tree,
 * never a fetch of config.js or an OCI registry. The digest identifies this
 * fake public bundle, NOT a secret or a secret-derived fingerprint.
 */
import type { JsonObject } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { dump } from "js-yaml";

import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";

import { DEMO_WORKLOADS, demoWorkloadName } from "./consoleScenario";
import { completed } from "./demoServiceDefaults";
import { DEMO_ENVS } from "./demoScope";
import { mockService } from "@reliantlabs/forge-web-runtime/mock-transport";
import { DEMO_RELEASES, demoBundleDigest, demoBundleId, RUNNING } from "./shopStory";

export const DEMO_WITHHELD = "[withheld]";
export { demoBundleDigest, demoBundleId };

export function configScenarioHandlers() {
  return completed(DeployService, mockService(DeployService, {
    getWorkloadConfig: (req) => {
      const env = DEMO_ENVS.find((e) => e.id === req.environmentId);
      const workload = DEMO_WORKLOADS.find((w) => w.name === demoWorkloadName(req.workload));
      if (!env || !workload) throw new ConnectError("No such sample workload.", Code.NotFound);
      if (req.bundleId !== undefined && req.releaseVersion !== undefined) throw new ConnectError("Choose one sample selector.", Code.InvalidArgument);
      const version = req.bundleId !== undefined
        ? DEMO_RELEASES.find((r) => demoBundleId(env.id, r.version) === req.bundleId)?.version
        : req.releaseVersion ?? RUNNING[env.id].version;
      const releaseIndex = DEMO_RELEASES.findIndex((r) => r.version === version);
      if (!version || releaseIndex < 0) throw new ConnectError("No such recorded sample bundle.", Code.NotFound);
      const objects: JsonObject[] = [{
        apiVersion: "v1", kind: "ConfigMap", metadata: { name: `${workload.name}-config`, namespace: `shop-${env.name}` },
        data: {
          LOG_LEVEL: env.name === "dev" ? "debug" : "info", ORDER_TIMEOUT_MS: version === "v1.8.4" ? "5000" : "3000",
          DATABASE_URL: DEMO_WITHHELD, PAYMENTS_API_KEY: DEMO_WITHHELD,
        },
      }];
      // Both public representations originate from exactly these redacted objects.
      return {
        // Match the addressed alias for the real adapter's scope check; the
        // immutable object projection above always uses the canonical name.
        environmentId: env.id, workload: req.workload, bundleId: demoBundleId(env.id, version), releaseVersion: version,
        bundleDigest: demoBundleDigest(env.id, version),
        objects, renderedYaml: objects.map((object) => dump(object, { sortKeys: true, lineWidth: -1 })).join("---\n"),
        frontendConfigUnsupportedReason: workload.name === "shop-web" ? "Sample only: no deployed config.js is fetched." : "",
      };
    },
  }));
}
