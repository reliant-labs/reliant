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
 *   handlers/github.ts  Alex's GitHub connection and the org's GitHub App
 *                       installations
 *
 * A separate entry on purpose: the demo bundle imports ./index.ts and never
 * this file, so reliant's generated services stay out of it (the demo bundle
 * guard fails otherwise; __tests__/appLayer.test.ts pins the boundary).
 */
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { BillingService } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { GitCredentialService } from "@/gen/controlplane/services/git_credential/v1/git_credential_pb";
import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";
import { ForgeService } from "@/gen/reliant/v1/forge_pb";
import { MCPService } from "@/gen/reliant/v1/mcp_pb";
import { ProjectService } from "@/gen/reliant/v1/project_pb";
import { SettingsService } from "@/gen/reliant/v1/settings_pb";
import { StreamingService } from "@/gen/reliant/v1/streaming_pb";
import { WorktreeService } from "@/gen/reliant/v1/worktree_pb";

import { defineFixturePack, type FixturePack } from "../transport";
import { accountScenarioHandlers } from "./handlers/account";
import { billingScenarioHandlers } from "./handlers/billing";
import { githubScenarioHandlers } from "./handlers/github";
import { reliantScenarioHandlers } from "./handlers/reliant";
import { streamingImpl } from "./handlers/reliant/streaming";
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
 *   SettingsService/GetConfigHealth
 *                                  replaces a stale legacy Reliant LLM key as
 *                                  it reads (drivers.HealReliantKey)
 *   GitCredentialService/GetGitCredential
 *                                  renews an expiring GitHub App user token
 *                                  and stores it as it reads
 *                                  (gitcredential's svc.Get, s.current)
 *   UserService/GetCurrentUser,    write on read, per cp#3513's audit
 *   BillingService/GetCurrentUserWalletOverview,
 *   BillingService/GetCurrentUserReliantOverview
 *
 * Three are ForgeService reports relayed from the daemon, which the fixture
 * answers from the story (handlers/reliant/forge.ts) and runs nothing for:
 *
 *   ForgeService/GetTopology       stores the project's forge name and
 *                                  refreshes its forge apps as it reads
 *   ForgeService/GetEnvStatus,     run a forge CLI command on the machine,
 *   ForgeService/GetEnvShape       not yet pinned to a read-only argv
 *
 * Three are reads kept POST-only because the response carries a secret or a
 * bearer link, and the fixture's are fake (`.invalid`, or none):
 *
 *   MCPService/ListServers         each server's config headers and env as
 *                                  stored (reliant#3466); the Connectors
 *                                  page's MCP tab reads it. The fixture's one
 *                                  server has an `.invalid` URL and a header
 *                                  that says it is not a secret
 *
 *   BillingService/ListCurrentUserInvoices            Stripe's hosted-invoice
 *                                                     and PDF URLs
 *   BillingService/GetCurrentUserComputeSubscription  a management URL
 *
 * The fixture never runs any of those effects: it only answers the read.
 * None of the twelve machine-waking reads (FileSystemService, the
 * WorktreeService git reads) is answered here.
 *
 * They are the app pack's `answers`, so a read-only fixture transport lets
 * exactly these through, with no name heuristic.
 * Only a pure fixture transport honours them (fixture mode is one; the
 * demo's gateway passthrough is not, and never serves this pack).
 */
export const APP_LAYER_ANSWERS = [
  "reliant.v1.ProjectService/GetProject",
  "reliant.v1.WorktreeService/ListWorktrees",
  "reliant.v1.SettingsService/GetConfigHealth",
  "reliant.v1.ForgeService/GetTopology",
  "reliant.v1.ForgeService/GetEnvStatus",
  "reliant.v1.ForgeService/GetEnvShape",
  "reliant.v1.MCPService/ListServers",
  "controlplane.v1.GitCredentialService/GetGitCredential",
  "controlplane.v1.UserService/GetCurrentUser",
  "controlplane.v1.BillingService/GetCurrentUserWalletOverview",
  "controlplane.v1.BillingService/GetCurrentUserReliantOverview",
  "controlplane.v1.BillingService/ListCurrentUserInvoices",
  "controlplane.v1.BillingService/GetCurrentUserComputeSubscription",
] as const;

/** The services APP_LAYER_ANSWERS names, so the transport checks each name against the proto. */
const ANSWERED_SERVICES = [ProjectService, WorktreeService, SettingsService, ForgeService, MCPService, GitCredentialService, UserService, BillingService];

/** The app layer's own answers, "pkg.Service/Method" keyed. */
export function appLayerScenarioHandlers(): MockHandlers {
  return { ...reliantScenarioHandlers(), ...billingScenarioHandlers(), ...accountScenarioHandlers(), ...githubScenarioHandlers() };
}

/**
 * The app pack: the demo pack with the app layer on top (an app-layer answer
 * wins), its clock pinned to `now` when given (shopPack's rule: before
 * anything reads the pack). Serve it on the pack clock:
 * `createFixtureTransport({ pack: shopAppPack({ now }), now: demoNow, readOnly: true })`.
 *
 * The open streams are routes, not scenario handlers: a route gets the
 * call's HandlerContext, so a stream ends when the page drops it (a route
 * beats a scenario handler in the same layer).
 */
export function shopAppPack(options: ShopPackOptions = {}): FixturePack {
  return defineFixturePack({
    name: "shop-app",
    extends: [shopPack(options)],
    scenario: appLayerScenarioHandlers,
    routes: (router) => router.service(StreamingService, streamingImpl),
    answers: APP_LAYER_ANSWERS,
    services: ANSWERED_SERVICES,
  });
}
