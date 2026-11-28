// Copyright (c) 2025 Reliant Labs

/**
 * The app pack's NAMED overlays: variants of the shop world a surface asks
 * for by name, because a name is all a URL (`?fixtures=no-daemons`) or a
 * harness init script can carry. Each is a pack layered on top of the app
 * pack (shopAppPack), so it overrides only the keys it answers.
 *
 * Only these names are honoured: install.ts allow-lists `?fixtures=` and
 * `window.__forgeFixtures.overlays` against this table and ignores the rest.
 *
 * The screenshot harness (e2e/visual) names one per surface variant
 * (surfaces.visual.ts), and always `empty-reads`; the hosted preview names
 * none unless its URL does. Editing any of these, or the pack under them,
 * moves the visual baselines: that is the one-world design, not a side effect.
 */
import { toJson, type DescService } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import type { AnyServiceDefaults } from "@reliantlabs/forge-web-runtime/mock-transport";

import { ChatService, ListChatsRequestSchema, StartChatRequestSchema, type ListChatsRequest, type StartChatRequest } from "@/gen/reliant/v1/chat_pb";
import { DaemonRegistryService } from "@/gen/reliant/v1/daemon_registry_pb";
import { ListProjectsWithAppsResponseSchema, ProjectInstallState, ProjectService } from "@/gen/reliant/v1/project_pb";
import { SettingsService, type ListSettingsResponse } from "@/gen/reliant/v1/settings_pb";
import { StreamingService, type StreamChatUpdatesRequest } from "@/gen/reliant/v1/streaming_pb";
import { OrganizationService, type ListMembersResponse } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { TestsService } from "@/gen/controlplane/services/tests/v1/tests_pb";

import { fail, pending, withEmptyReads } from "../overlay";
import { defineFixturePack, type FixturePack } from "../transport";
import catalogJson from "./data/catalog.json?raw";
import { accountScenarioHandlers } from "./handlers/account";
import { chatHandlers } from "./handlers/reliant/chats";
import { serve } from "./handlers/serve";
import { settingsHandlers } from "./handlers/reliant/settings";
import { chatStreamEvents, untilAborted } from "./handlers/reliant/streaming";
import { DATA_ANCHOR, packJson } from "./packJson";
import { agentsChatHandlers, agentsChatStream } from "./overlays/agents";
import { chartHoverHandlers, sandboxesHandlers } from "./overlays/dashboards";
import { SHOP_READ_ONLY_MESSAGE } from "./index";
import { DEMO_ORG, MINUTE } from "./shopStory";
import { APP_USER, CHAT_IDS, chats, DAEMON_IDS, DAEMON_NAMES, docsProject, isoAgo, PROJECT_IDS, shopProject } from "./workspace";

/** An overlay, built when it is selected (after the pack clock is pinned). */
export type ShopOverlay = () => FixturePack;

const HOUR = 3_600_000;
const DAY = 24 * HOUR;
/** An instant as data/*.json writes it: relative to DATA_ANCHOR, shifted onto the pack clock by packJson. */
const at = (ago: number) => new Date(DATA_ANCHOR - ago).toISOString();

// ── The project catalog's variants (data/catalog.json) ──────────────────────

type Json = { [key: string]: unknown };
type CatalogJson = { projects: { apps: (Json & { name: string; hosted?: Json })[]; [key: string]: unknown }[]; unlinkedApps: Json[] };
const catalogWith = (edit: (catalog: CatalogJson) => void) => () => {
  const catalog = JSON.parse(catalogJson) as CatalogJson;
  edit(catalog);
  return packJson(ListProjectsWithAppsResponseSchema, catalog);
};

const SHOP_SOURCE = { remote: "github.com/acme/shop", subpath: "", commit: "9c2e41d07a3f", lastSeenAt: at(3 * HOUR) };
/** shop deployed from two repositories (the D5 warning). */
const twoRepositories = () => ({
  name: "shop", createdAt: at(40 * DAY), environments: ["prod", "staging"], hasStaticSite: false, lastDeployedAt: at(2 * HOUR),
  sources: [SHOP_SOURCE, { remote: "github.com/alex/shop", subpath: "apps/web", commit: "9f8e7d6c5b4a39281706", lastSeenAt: at(2 * DAY) }],
  warning: "shop is deployed from two repositories: github.com/acme/shop and github.com/alex/shop. The next deploy from either replaces what the other shipped.",
});

/** For the app's Code card: shop deployed from two repositories, also held by Alex's fork and a teammate's shared project. */
const appCodeCatalog = catalogWith((catalog) => {
  for (const row of catalog.projects) {
    for (const app of row.apps) if (app.name === "shop" && app.hosted) app.hosted = twoRepositories();
  }
  const project = (id: string, extra: Record<string, unknown>) => ({
    id, userId: "00000000-0000-4000-8000-00000000a1ec", name: "shop", path: "/home/alex/code/shop-experiments", isGitRepo: true, defaultBranch: "main",
    createdAt: at(30 * DAY), updatedAt: at(2 * DAY), lastActive: at(2 * DAY), orgId: DEMO_ORG, ...extra,
  });
  catalog.projects.push(
    {
      project: project("shop-proj-shop-experiments", { name: "shop-experiments", remoteUrl: "https://github.com/alex/shop.git" }),
      repos: [{ id: "r-exp", name: "shop", relativePath: "", remote: "github.com/alex/shop" }],
      apps: [{ name: "shop", repoId: "r-exp", subpath: "apps/web", link: "PROJECT_APP_LINK_NAME", hosted: twoRepositories() }],
      checkouts: [{ daemonId: "shop-d-cloud", daemonName: "cloud-machine", installState: "PROJECT_INSTALL_STATE_INSTALLED" }],
    },
    {
      project: project("shop-proj-shop-shared", { viewerAccess: true, userId: "user-sam", path: "", updatedAt: at(DAY), lastActive: at(DAY) }),
      repos: [{ id: "r-sam", name: "shop", relativePath: "", remote: "" }],
      apps: [{ name: "shop", repoId: "r-sam", subpath: "", link: "PROJECT_APP_LINK_NAME", hosted: twoRepositories() }],
      checkouts: [],
    },
  );
});

/** For the Projects page and a project's page: a second app with no code here (cut from a subdirectory of another repo). */
const projectsPageCatalog = catalogWith((catalog) => {
  catalog.unlinkedApps.push({
    name: "ledger-worker", createdAt: at(40 * DAY), environments: ["prod", "staging"], hasStaticSite: false, warning: "", lastDeployedAt: at(4 * DAY),
    sources: [{ remote: "github.com/acme/ledger", subpath: "worker", commit: "d00d123", lastSeenAt: at(4 * DAY) }],
  });
});

// ── Settings: two pins in the project selector ──────────────────────────────

const LIST_SETTINGS = `${SettingsService.typeName}/ListSettings`;
const LIST_MEMBERS = `${OrganizationService.typeName}/ListMembers`;

/** Alex's saved settings plus `sidebar.projects`, pinning shop and the app-only status-page. */
async function pinnedSettings(req: unknown) {
  const saved = (await settingsHandlers()[LIST_SETTINGS]!(req)) as ListSettingsResponse;
  const value = JSON.stringify({ [DEMO_ORG]: { pinned: [PROJECT_IDS.shop, "app:status-page"] } });
  const pins = { id: "sidebar.projects", key: "sidebar.projects", value, valueType: "string", createdAt: isoAgo(DAY), updatedAt: isoAgo(DAY) };
  return { settings: [...saved.settings, pins], total: saved.total + 1 };
}

const pinsOverlay = (name: string) => defineFixturePack({ name, scenario: serve(SettingsService, { listSettings: pinnedSettings }) });

// ── The Setup Guide: shown, with two items left ─────────────────────────────

/**
 * Alex's saved settings with the Setup Guide `panel` (collapsed or expanded)
 * and six of its eight items done (a teammate and the tour left). Saved on
 * the server, as the guide reads it: the server's answer wins, so the page has
 * nothing to heal and writes nothing back. A teammate is left because Alex's
 * org has nobody else in it yet: the roster is Alex alone (the pack's has the
 * team, which the guide would detect and save).
 */
function setupGuide(panel: "collapsed" | "expanded"): FixturePack {
  const done = ["connect-machine", "add-api-key", "start-chat", "add-repo", "run-workflow", "deploy-app"];
  const replace: Record<string, string> = { "onboarding.checklist.panel_state": panel, "onboarding.checklist.completed_items": JSON.stringify(done) };
  return defineFixturePack({
    name: `setup-guide-${panel}`,
    scenario: {
      ...serve(SettingsService, {
        listSettings: async (req) => {
          const saved = (await settingsHandlers()[LIST_SETTINGS]!(req)) as ListSettingsResponse;
          const settings = saved.settings.map((s) => (s.key in replace ? { ...s, value: replace[s.key] } : s));
          return { settings, total: settings.length };
        },
      }),
      ...serve(OrganizationService, {
        listMembers: async (req) => {
          const roster = (await accountScenarioHandlers()[LIST_MEMBERS]!(req)) as ListMembersResponse;
          const members = roster.members.filter((m) => m.userId === APP_USER.id);
          return { members, nextPageToken: "", seats: { members: members.length, pendingInvitations: 0, limit: roster.seats?.limit ?? 0 } };
        },
      }),
    },
  });
}

// ── A chat's projects, and where a project is checked out ───────────────────

/**
 * The fixture chat (cart totals) with docs attached beside shop: the
 * composer's project chips. Every other chat works in its own project alone.
 */
function chatProjects(req: { chatId: string }) {
  const chat = chats().find((c) => c.id === req.chatId);
  const own = chat?.projectId === PROJECT_IDS.docs ? docsProject() : shopProject();
  const daemonId = chat?.activeDaemonId || DAEMON_IDS.cloud;
  const entry = (p: typeof own, primary: boolean) => ({ projectId: p.id, name: p.name, primary, daemonId, path: p.path, attachedAt: primary ? "" : isoAgo(20 * MINUTE) });
  return { projects: [entry(own, true), ...(req.chatId === CHAT_IDS.cartTotals ? [entry(docsProject(), false)] : [])] };
}

/** shop is checked out on the cloud machine; docs only on a machine that is gone. */
const newChatInstalls = () => ({
  projectDaemons: [
    { projectId: PROJECT_IDS.shop, daemonId: DAEMON_IDS.cloud, path: "/home/workspace/projects/shop", clonedAt: "2026-11-01T00:00:00Z", installState: ProjectInstallState.INSTALLED, installError: "", daemonName: DAEMON_NAMES.cloud },
    { projectId: PROJECT_IDS.docs, daemonId: "shop-d-retired", path: "/home/alex/code/docs", clonedAt: "2026-11-01T00:00:00Z", installState: ProjectInstallState.INSTALLED, installError: "", daemonName: "old-laptop" },
  ],
});

// ── The phone's project pickers: what the app asks for ──────────────────────

/** Requests a harness flow test asserts on, as proto JSON, by "Service/Method" (`window.__visualRequests`). */
function noteRequest(key: string, json: unknown): void {
  const w = window as { __visualRequests?: Record<string, unknown[]> };
  ((w.__visualRequests ??= {})[key] ??= []).push(json);
}

const LIST_CHATS = `${ChatService.typeName}/ListChats`;
const START_CHAT = `${ChatService.typeName}/StartChat`;

/**
 * The phone's project pickers (e2e/visual/mobile-project-pickers.visual.ts):
 * both projects checked out on the cloud machine, so either can start a chat,
 * and every ListChats and StartChat request noted for the test to read.
 * StartChat stays REFUSED, exactly as the read-only guard refuses it (same
 * code and message, so the probe records it as refused): it is named in
 * `answers` only so its request reaches this layer to be noted. Nothing is
 * ever answered for a write.
 */
function projectPickers(): FixturePack {
  const listChats = chatHandlers()[LIST_CHATS]!;
  return defineFixturePack({
    name: "project-pickers",
    services: [ChatService],
    answers: [START_CHAT],
    scenario: {
      ...serve(ProjectService, {
        listProjectDaemons: () => ({
          projectDaemons: [PROJECT_IDS.shop, PROJECT_IDS.docs].map((projectId) => ({
            projectId, daemonId: DAEMON_IDS.cloud, path: "/home/workspace/projects/x", clonedAt: "2026-11-01T00:00:00Z",
            installState: ProjectInstallState.INSTALLED, installError: "", daemonName: DAEMON_NAMES.cloud,
          })),
        }),
      }),
      ...serve(ChatService, {
        listChats: (req: ListChatsRequest) => {
          noteRequest(LIST_CHATS, toJson(ListChatsRequestSchema, req));
          return listChats(req) as never;
        },
        startChat: (req: StartChatRequest) => {
          noteRequest(START_CHAT, toJson(StartChatRequestSchema, req));
          throw new ConnectError(SHOP_READ_ONLY_MESSAGE, Code.PermissionDenied);
        },
      }),
    },
  });
}

// ── Empty reads (plan D6) ───────────────────────────────────────────────────

/** Every service the app has a generated client for: reliant's and the control plane's. */
const APP_SERVICES: readonly DescService[] = Object.values(
  import.meta.glob<Record<string, unknown>>(["/src/gen/reliant/**/*_pb.ts", "/src/gen/controlplane/**/*_pb.ts"], { eager: true }),
).flatMap((mod) => Object.values(mod).filter((value): value is DescService => (value as { kind?: unknown } | null)?.kind === "service"));

/** Note an empty answer in the probe's `unknown`, as an Unimplemented read is (install.ts). */
function noteUnknown(key: string): void {
  const probe = (window as { __forgeFixtures?: { unknown?: string[] } }).__forgeFixtures;
  if (probe?.unknown && !probe.unknown.includes(key)) probe.unknown.push(key);
}

/**
 * A read no layer answers gets the empty response (a server stream: one
 * empty message, held open) instead of Unimplemented, as the old fetch
 * shim's `{}` did (overlay.ts withEmptyReads), and is
 * still recorded in the probe's `unknown` (VISUAL_FXLOG prints it). Writes
 * never get here: the read-only guard refuses them first, and only a read the
 * proto declares NO_SIDE_EFFECTS gets an empty answer (the rule the guard
 * applies).
 *
 * The screenshot harness only: a read nobody wrote a fixture for renders as
 * "nothing there", not an error. Its layer holds only `defaults`, and no other
 * layer of the shop pack declares any, so every route and scenario handler
 * wins over it wherever it sits in the overlay list.
 */
function emptyReads(): FixturePack {
  const base = withEmptyReads({ readOnly: true }, APP_SERVICES).pack!;
  const defaults = (base.defaults ?? []).map((entry) => {
    const service = (entry as AnyServiceDefaults).$service;
    const out: Record<string, unknown> = { $service: service };
    for (const method of service.methods) {
      const handler = (entry as unknown as Record<string, ((...args: unknown[]) => unknown) | undefined>)[method.localName];
      if (!handler) continue;
      const key = `${service.typeName}/${method.name}`;
      out[method.localName] = (...args: unknown[]) => {
        noteUnknown(key);
        return handler(...args);
      };
    }
    return out as unknown as AnyServiceDefaults;
  });
  return defineFixturePack({ name: "empty-reads", defaults });
}

// ── The table ───────────────────────────────────────────────────────────────

/** The read the harness's own self-test never answers (harness.visual.ts). */
export const PENDING_LIST_CHATS = "pending:reliant.v1.ChatService/ListChats";

export const SHOP_OVERLAYS: Readonly<Record<string, ShopOverlay>> = {
  /** Alex has no machine: the daemonless home and the no-sandbox agents page. */
  "no-daemons": () => defineFixturePack({ name: "no-daemons", scenario: serve(DaemonRegistryService, { listDaemons: () => ({ daemons: [] }) }) }),
  /** A control plane that does not serve TestsService: every read answers Unimplemented. */
  "tests-unimplemented": () =>
    defineFixturePack({
      name: "tests-unimplemented",
      scenario: Object.fromEntries(
        TestsService.methods.map((m) => [`${TestsService.typeName}/${m.name}`, fail(Code.Unimplemented, "TestsService is not served here")]),
      ),
    }),
  /**
   * The chat stream without the client-only failed send: on the mobile chat
   * screen whether it survives depends on the order the snapshot and the
   * message list land in.
   */
  "chat-no-failed-send": () =>
    defineFixturePack({
      name: "chat-no-failed-send",
      routes: (router) =>
        router.service(StreamingService, {
          async *streamChatUpdates(req: StreamChatUpdatesRequest, ctx) {
            yield* chatStreamEvents(req.chatId, { failedSend: false });
            await untilAborted(ctx.signal);
          },
        }),
    }),
  /** Two pins (shop, and the app-only status-page) in the selector. Pins also move the sidebar, so only these surfaces see them. */
  "project-selector": () => pinsOverlay("project-selector"),
  /** The Projects page and a project's page: the selector's pins, and a second app with no code here. */
  "projects-page": () =>
    defineFixturePack({
      name: "projects-page",
      extends: [pinsOverlay("project-selector")],
      scenario: serve(ProjectService, { listProjectsWithApps: projectsPageCatalog }),
    }),
  /** The app's Code card: shop deployed from two repositories, held by Alex's fork and a teammate's shared project. */
  "app-code-card": () => defineFixturePack({ name: "app-code-card", scenario: serve(ProjectService, { listProjectsWithApps: appCodeCatalog }) }),
  /** The Setup Guide open, six of eight done (Onboarding/OnboardingChecklist.tsx). */
  "setup-guide-expanded": () => setupGuide("expanded"),
  /** The Setup Guide collapsed to its pill, six of eight done. */
  "setup-guide-collapsed": () => setupGuide("collapsed"),
  /** The fixture chat with docs attached beside shop: the composer's project chips. */
  "chat-attached-projects": () => defineFixturePack({ name: "chat-attached-projects", scenario: serve(ChatService, { listChatProjects: chatProjects }) }),
  /** Where Alex's projects are checked out, for the phone's new chat: shop on the cloud machine, docs on a machine that is gone. */
  "new-chat-installs": () => defineFixturePack({ name: "new-chat-installs", scenario: serve(ProjectService, { listProjectDaemons: newChatInstalls }) }),
  /** A dashboard opened by URL, never listed: a figure and a two-series line, for the chart hover surfaces (overlays/dashboards.ts). */
  "chart-hover": () => defineFixturePack({ name: "chart-hover", scenario: chartHoverHandlers }),
  /** A chat opened by URL, never listed: one turn that starts twelve sub-agents, 8 done, 3 running, 1 failed (overlays/agents.ts). */
  "chat-agents": () =>
    defineFixturePack({
      name: "chat-agents",
      scenario: agentsChatHandlers(),
      routes: (router) => router.service(StreamingService, { streamChatUpdates: (req: StreamChatUpdatesRequest, ctx) => agentsChatStream(req, ctx) }),
    }),
  /** The platform's Sandboxes dashboard opened by URL under shop's release dashboards (overlays/dashboards.ts). */
  "sandboxes-dashboard": () => defineFixturePack({ name: "sandboxes-dashboard", scenario: sandboxesHandlers }),
  /** The phone's project pickers: both projects on the cloud machine, ListChats and StartChat requests noted, StartChat still refused. */
  "project-pickers": projectPickers,
  /** Every read no fixture answers gets the empty response (the screenshot harness only). */
  "empty-reads": emptyReads,
  /** The sidebar's chat list never answers: the harness proves settle() fails at its deadline. */
  [PENDING_LIST_CHATS]: () =>
    defineFixturePack({ name: PENDING_LIST_CHATS, routes: (router) => router.service(ChatService, { listChats: (_req, ctx) => pending(ctx.signal) }) }),
};

/**
 * Console preview overlays, keyed by the preview's `?case=` id and layered
 * with FixturePreview. Kept separate from the URL-selectable app overlays.
 */
export { browserOverlays } from "./overlays/browsers";
export { consoleOverlays } from "./overlays/console";
export { deliveryOverlays } from "./overlays/delivery";
export { observabilityOverlays, readCap, retainedCatalog } from "./overlays/observability";
