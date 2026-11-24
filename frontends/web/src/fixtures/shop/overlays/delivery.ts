// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's overlays for Releases and Workloads
 * (/forge-releases-preview, /forge-workloads-preview), by the preview's
 * `?case=` id: the ledger and workload states shop's story never reaches.
 * Each is put over the pack's app layer, under the real console.
 */

import { Code } from "@connectrpc/connect";

import { DeploySecretPosture, DeployTier } from "@/gen/controlplane/controlplane/v1/deploy_pb";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { DomainService } from "@/gen/controlplane/services/domain/v1/domain_pb";
import { ObservabilityService, WorkloadHealth } from "@/gen/controlplane/services/observability/v1/observability_pb";

import { emptying, failing, refining, stalling } from "../../overlay";
import type { FixturePack } from "../../transport";
import { consoleScenarioHandlers } from "../handlers/console";
import { observabilityHandlers } from "../handlers/observability";
import { createShopApi } from "../model/shopApi";

const deploy = DeployService.method;
const obs = ObservabilityService.method;
const story = consoleScenarioHandlers();

/** The pack's telemetry answers, built at the first answer: building them reads the pack clock, which no module may do at import. */
let telemetryHandlers: Record<string, unknown> | null = null;
const telemetry = () => (telemetryHandlers ??= observabilityHandlers(createShopApi()) as Record<string, unknown>);

/** Every read the Releases pages make of the ledger. */
const LEDGER = [deploy.listReleases, deploy.getRelease, deploy.compareReleases, deploy.listPromotions, deploy.getRollout, deploy.getBundle, deploy.listDeployAttempts, deploy.getDeployAttempt];

/** The Overview's and a workload page's telemetry reads. */
const WORKLOADS = [obs.listWorkloads, obs.getWorkload];

/** prod, as the roster reads it, with one fact of its kind turned. */
function prodAs(name: string, turn: (environment: { hostedSlot: boolean; secretPosture: DeploySecretPosture }) => void): FixturePack {
  return refining(name, deploy.getLiveView, story, (view) => {
    for (const live of view.environments) if (live.environment?.name === "prod") turn(live.environment);
  });
}

export const deliveryOverlays = {
  releases: {
    empty: emptying("releases.empty", [deploy.listReleases, deploy.getRelease, deploy.listPromotions, deploy.listDeployAttempts]),
    loading: stalling("releases.loading", LEDGER),
    error: failing("releases.error", LEDGER, Code.Unavailable, "control plane unavailable"),
    "ledger-failed": failing("releases.ledger-failed", deploy.listPromotions, Code.Unavailable, "ledger unavailable"),
    "compare-fails": failing("releases.compare-fails", deploy.compareReleases, Code.DeadlineExceeded, "compare timed out"),
  },
  workloads: {
    /** The worker crash-looping in every environment: the Overview's red row, and a workload worth restarting. */
    "crash-looping": refining("workloads.crash-looping", obs.listWorkloads, telemetry, (list) => {
      for (const row of list.workloads) {
        if (row.workload !== "worker") continue;
        Object.assign(row, { health: WorkloadHealth.CRASH_LOOPING, healthReason: "CrashLoopBackOff", restartsInWindow: 14n, readyReplicas: 0 });
      }
    }),
    empty: emptying("workloads.empty", WORKLOADS),
    loading: stalling("workloads.loading", WORKLOADS),
    error: failing("workloads.error", WORKLOADS, Code.Unavailable, "upstream connect error"),
    "not-set-up": failing("workloads.not-set-up", WORKLOADS, Code.FailedPrecondition, "observability is not set up for this organization yet"),
    /** prod on the team's own cluster: no hosted slot, so no restart. */
    "self-managed": prodAs("workloads.self-managed", (environment) => {
      environment.hostedSlot = false;
    }),
    /** prod as a local stack (`forge env up`): readable secrets, so no restart. */
    local: prodAs("workloads.local", (environment) => {
      environment.secretPosture = DeploySecretPosture.READABLE;
    }),
    /** Restart and stop reach the control plane, which refuses them. */
    "action-fails": failing("workloads.action-fails", [deploy.restartWorkload, deploy.scale], Code.PermissionDenied, "the caller may not write deployments in this organization"),
    "config-denied": failing("workloads.config-denied", deploy.getWorkloadConfig, Code.PermissionDenied, "config read denied"),
    "config-unsupported": failing("workloads.config-unsupported", deploy.getWorkloadConfig, Code.Unimplemented, "old server"),
    "no-envs": emptying("workloads.no-envs", [deploy.getLiveView, deploy.listEnvironments]),
    "no-frontends": refining("workloads.no-frontends", deploy.getStatus, story, (status) => {
      status.deployments = status.deployments.filter((row) => row.deployment?.tier !== DeployTier.STATIC);
    }),
    "frontends-loading": stalling("workloads.frontends-loading", deploy.getStatus),
    "domains-unreadable": failing("workloads.domains-unreadable", DomainService.method.listDomains, Code.Unavailable, "custom domains unreachable"),
  },
} satisfies Record<string, Record<string, FixturePack | readonly FixturePack[]>>;
