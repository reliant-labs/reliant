// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's overlays for the whole forge console (/forge-pane-preview),
 * by the preview's `?case=` id: the states of the control plane's and the
 * daemon's answers that shop's story never reaches.
 */

import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { Code } from "@connectrpc/connect";

import { DeployHoldKind, DeployHoldSchema, DeployRolloutPhase } from "@/gen/controlplane/controlplane/v1/deploy_pb";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { DomainService } from "@/gen/controlplane/services/domain/v1/domain_pb";
import { ForgeService } from "@/gen/reliant/v1/forge_pb";

import { emptying, failing, refining } from "../../overlay";
import type { FixturePack } from "../../transport";
import { consoleScenarioHandlers } from "../handlers/console";
import { DEMO_PROJECT } from "../handlers/scope";
import { demoNow, MINUTE } from "../shopStory";

const deploy = DeployService.method;

/**
 * prod's newest deploy, QUEUED on billing: accepted and recorded, waiting
 * for a compute plan, while v1.8.3 keeps running. `canResolve`: the viewer
 * can set the plan up (an org admin) or cannot (a member).
 */
function queued(canResolve: boolean): FixturePack {
  return refining(`console.queued${canResolve ? "" : "-member"}`, deploy.getLiveView, consoleScenarioHandlers(), (view) => {
    const heldSince = timestampFromMs(demoNow() - 6 * MINUTE);
    const promotionId = "demo-promo-prod-v1.8.4-queued";
    for (const live of view.environments) {
      if (live.environment?.name !== "prod") continue;
      live.environment.currentPromotionId = promotionId;
      live.environment.holds = [
        create(DeployHoldSchema, {
          kind: DeployHoldKind.BILLING,
          promotionId,
          reason: "this runs compute (3 workloads) and the organization has no active compute plan",
          fix: "Subscribe to a Reliant Compute plan in Reliant → Settings → Billing (an org admin can). The deploy starts automatically once the plan is active; nothing needs to be re-run.",
          actionUrl: `https://app.reliant.dev/forge/p/${DEMO_PROJECT}/environments/prod`,
          callerCanResolve: canResolve,
          heldSince,
        }),
      ];
      if (live.currentPromotion) Object.assign(live.currentPromotion, { id: promotionId, releaseVersion: "v1.8.4", promotedByActor: "maya", createdAt: heldSince });
      live.phase = DeployRolloutPhase.HELD;
      // Queued, the previous release is still what runs, so it does not match the queued intent.
      if (live.drift) live.drift.state = "drifted";
    }
  });
}

export const consoleOverlays = {
  /** No environment registered with the control plane: forge's file ledger is all there is. */
  unregistered: emptying("console.unregistered", [deploy.getLiveView, deploy.listEnvironments, DomainService.method.listDomains]),
  /** The daemon is not connected: every ForgeService read fails as the gateway answers it. */
  "daemon-offline": failing("console.daemon-offline", ForgeService, Code.Unavailable, "no daemon connected for user"),
  queued: queued(true),
  "queued-member": queued(false),
} satisfies Record<string, FixturePack | readonly FixturePack[]>;
