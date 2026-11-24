// Copyright (c) 2025 Reliant Labs

/**
 * ForgeService: what Alex's machine says forge says about shop's checkout,
 * the daemon's side of the console (`forge env status --json` and friends,
 * relayed as report JSON): the three environments shop declares, each on the
 * release RUNNING says, and the releases the file ledger holds. The control
 * plane's side of the same story is ../console.ts.
 */
import { ForgeService } from "@/gen/reliant/v1/forge_pb";

import { DEMO_PROJECT, DEMO_ENVS } from "../scope";
import { DEMO_RELEASES, DEMO_SERVICE_NAMES, RUNNING } from "../../shopStory";
import { isoAgo } from "../../workspace";
import { serve } from "../serve";

const META = { isForgeProject: true, supported: true, forgeVersion: "v1.8.0", exitCode: 0 };

const reply = (report: unknown) => ({ meta: META, reportJson: JSON.stringify(report) });

function topology() {
  return {
    project: DEMO_PROJECT,
    latest_release: DEMO_RELEASES[0].version,
    releases: DEMO_RELEASES.map((release) => release.version),
    environments: DEMO_ENVS.map((env) => ({
      env: env.name,
      declared: true,
      bound: true,
      destination: "cluster",
      release: RUNNING[env.id].version,
      promoted_at: isoAgo(RUNNING[env.id].age),
      namespace: `shop-${env.name}`,
      images: DEMO_SERVICE_NAMES.map((image) => ({ image, state: "verified" })),
    })),
  };
}

const envOf = (name: string) => DEMO_ENVS.find((env) => env.name === name);

export function forgeHandlers() {
  return serve(ForgeService, {
    getTopology: () => reply(topology()),
    getEnvStatus: (req) => {
      const env = envOf(req.env);
      return reply(env ? { ok: true, env: env.name, bound: true, release: RUNNING[env.id].version, runtime: { env: env.name, destination: "cluster", services: [], checks: [] } } : { ok: false, env: req.env, bound: false });
    },
    getEnvShape: (req) => reply({ project: DEMO_PROJECT, env: req.env, kind: "self_managed", shape: { kind: "self_managed", workloads: [], secrets: [] } }),
  });
}
