// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's APP LAYER: the shop world as the signed-in reliant app
 * reads it, for the surfaces that render the whole app (the screenshot
 * harness, the hosted preview). It is the demo pack (./index.ts shopPack)
 * plus the answers the app reads beside the forge console:
 *
 *   handlers/reliant/   the reliant services: projects, worktrees, chats and
 *                       their transcripts, the open streams, workflows and
 *                       runs, settings and models, machines (workspace.ts
 *                       tells that side of the story)
 *   handlers/billing.ts BillingService and ObservabilityBillingService
 *   handlers/account.ts UserService (who is signed in)
 *
 * A separate entry on purpose: the demo bundle imports ./index.ts and never
 * this file, so reliant's generated services stay out of it (the demo bundle
 * guard fails otherwise; __tests__/appLayer.test.ts pins the boundary).
 */
import type { MockHandlers, MockScenario } from "@reliantlabs/forge-web-runtime/mock-transport";

import { defineFixturePack, type FixtureContext, type FixturePack, type FixtureScenario } from "../transport";
import { accountScenarioHandlers } from "./handlers/account";
import { billingScenarioHandlers } from "./handlers/billing";
import { reliantScenarioHandlers } from "./handlers/reliant";
import { shopPack, type ShopPackOptions } from "./index";

/**
 * RPCs the app layer answers although the transport's read-only guard
 * would refuse them (no `idempotency_level = NO_SIDE_EFFECTS`): the pack's
 * `answers` list. Fixture-only, never the demo pack's, each name checked
 * against the proto.
 *
 * Each is a server-side write by the read-only audit (reliant's, and
 * control-plane's in cp#3513), and so stays unannotated:
 *
 *   ProjectService/GetProject      reconciles the stored is_git_repo flag
 *                                  from a live daemon check, and sends a
 *                                  project-opened analytics event
 *   WorktreeService/ListWorktrees  creates the project's main worktree row
 *                                  when it has none
 *   UserService/GetCurrentUser,    write on read, per cp#3513's audit
 *   BillingService/GetCurrentUserWalletOverview,
 *   BillingService/GetCurrentUserReliantOverview
 *
 * The fixture never runs any of those effects: it only answers the read.
 * None of the twelve machine-waking reads (FileSystemService, the
 * WorktreeService git reads) is answered here.
 *
 * TODO(P2.1a): pass this as `shopAppPack`'s `answers` once
 * src/fixtures/transport.ts takes the option (branch
 * refactor/fixture-transport-router). Until then the deprecated isDemoRead
 * classifier lets all five through by their names.
 */
export const APP_LAYER_ANSWERS = [
  "reliant.v1.ProjectService/GetProject",
  "reliant.v1.WorktreeService/ListWorktrees",
  "controlplane.v1.UserService/GetCurrentUser",
  "controlplane.v1.BillingService/GetCurrentUserWalletOverview",
  "controlplane.v1.BillingService/GetCurrentUserReliantOverview",
] as const;

/** The app layer's own answers, "pkg.Service/Method" keyed. */
export function appLayerScenarioHandlers(): MockHandlers {
  return { ...reliantScenarioHandlers(), ...billingScenarioHandlers(), ...accountScenarioHandlers() };
}

const handlersOf = (scenario: FixtureScenario | undefined): MockScenario["handlers"] =>
  !scenario ? {} : typeof (scenario as Partial<MockScenario>).name === "string" ? (scenario as MockScenario).handlers : (scenario as MockScenario["handlers"]);

/**
 * The app pack: the demo pack with the app layer on top (an app-layer answer
 * wins), its clock pinned to `now` when given (shopPack's rule: before
 * anything reads the pack). Serve it on the pack clock:
 * `createFixtureTransport({ pack: shopAppPack({ now }), now: demoNow, readOnly })`.
 *
 * The layers are composed here, as one scenario, until the local fixture
 * transport takes a pack's `extends` (P2.1a); then this becomes
 * `defineFixturePack({ name: "shop-app", extends: [shopPack({ now })], scenario: appLayerScenarioHandlers })`.
 */
export function shopAppPack(options: ShopPackOptions = {}): FixturePack {
  const base = shopPack(options);
  return defineFixturePack({
    name: "shop-app",
    scenario: (ctx: FixtureContext) => {
      const baseScenario = typeof base.scenario === "function" ? base.scenario(ctx) : base.scenario;
      return { name: "shop-app", handlers: { ...handlersOf(baseScenario), ...appLayerScenarioHandlers() } };
    },
  });
}
