// Copyright (c) 2025 Reliant Labs

/**
 * Every builder, in both modes of the flag `web.org_routes` (control-plane
 * docs/design/unified-shell-and-projects.md D12):
 *
 *   off  the URL the app used before lib/paths existed, byte for byte, even
 *        with an org known (U1 changed no URL and U3 changes none with the flag
 *        off), and the router treats it as the same URL the old `to` +
 *        `params` form built: `navigate({ to: runPath(id) })` lands exactly
 *        where `navigate({ to: "/workflows/runs/$runId", params: { runId: id } })`
 *        did, awkward ids included;
 *   on   the org form (ORG_URLS) for an org-scoped page — `/o/<org>` in
 *        front, the console at `/o/<org>/apps/<app>`, the org Observability at
 *        `/o/<org>/observability` — routed where the org tree's template with
 *        the same params routes; every other page keeps today's URL.
 */

import { createMemoryHistory, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { consoleBase, consolePath, orgPath, orgUsagePath } from "@/components/Forge/Console/paths";
import { resetShellForTests } from "@/lib/shell";
import { commitRouteOrgScope, useRouteOrgScope } from "@/lib/routeOrg";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { setRuntimeConfig } from "@/test/runtimeConfig";

import * as paths from "..";
import { orgFreePath, orgOfPath, orgSwitchPath, withOrg } from "../org";

/** An id that needs encoding: a workflow ref, or a name a person typed. */
const AWKWARD = "builtin://get it/right?#%";

interface Row {
  build: () => string;
  /** Today's URL, as the app spelled it: every builder's URL with the flag off. */
  url: string;
  /** The route template and params the call sites used to pass, where there was one. */
  template?: string;
  params?: Record<string, string>;
}

const ROWS: Record<string, Row> = {
  homePath: { build: () => paths.homePath(), url: "/", template: "/" },
  // Always the org form: a way into one of the person's orgs by name (the org routes are always mounted).
  orgHomePath: { build: () => paths.orgHomePath("acme"), url: "/o/acme", template: "/o/$org", params: { org: "acme" } },
  projectChatsPath: { build: () => paths.projectChatsPath("p-1"), url: "/project/p-1", template: "/project/$projectId", params: { projectId: "p-1" } },
  projectsPath: { build: () => paths.projectsPath(), url: "/projects", template: "/projects" },
  projectPath: { build: () => paths.projectPath("p-1"), url: "/projects/p-1", template: "/projects/$projectId", params: { projectId: "p-1" } },
  "projectPath(tab)": {
    build: () => paths.projectPath("p-1", "settings"),
    url: "/projects/p-1/settings",
    template: "/projects/$projectId/settings",
    params: { projectId: "p-1" },
  },
  appPath: { build: () => paths.appPath("shop"), url: "/forge/p/shop", template: "/forge/p/$project", params: { project: "shop" } },
  // New in U5 (R1–R3): no URL before them, so their org-less form is the one they were born with.
  chatPath: { build: () => paths.chatPath("c-1"), url: "/c/c-1", template: "/c/$chatId", params: { chatId: "c-1" } },
  threadPath: {
    build: () => paths.threadPath("c-1", "t-1"),
    url: "/c/c-1/t/t-1",
    template: "/c/$chatId/t/$threadId",
    params: { chatId: "c-1", threadId: "t-1" },
  },
  newChatPath: { build: () => paths.newChatPath(), url: "/new", template: "/new" },
  inboxPath: { build: () => paths.inboxPath(), url: "/inbox", template: "/inbox" },
  gtmPath: { build: () => paths.gtmPath("ideas"), url: "/gtm/ideas", template: "/gtm/ideas" },
  gtmIdeaPath: { build: () => paths.gtmIdeaPath("idea-1"), url: "/gtm/ideas/idea-1", template: "/gtm/ideas/$ideaId", params: { ideaId: "idea-1" } },
  gtmLibraryPath: { build: () => paths.gtmLibraryPath("e-1"), url: "/gtm/strategy?view=library&entry=e-1" },
  usagePath: { build: () => paths.usagePath(), url: "/usage", template: "/usage" },
  usageBillingPath: { build: () => paths.usageBillingPath(), url: "/usage", template: "/usage" },
  connectorsPath: { build: () => paths.connectorsPath(), url: "/connectors", template: "/connectors" },
  forgeLandingPath: { build: () => paths.forgeLandingPath(), url: "/forge", template: "/forge" },
  forgeDemoPath: { build: () => paths.forgeDemoPath(), url: "/forge/demo", template: "/forge/demo" },
  workflowsPath: { build: () => paths.workflowsPath(), url: "/workflows", template: "/workflows" },
  workflowLibraryPath: { build: () => paths.workflowLibraryPath(), url: "/workflows/library", template: "/workflows/library" },
  workflowDetailPath: {
    build: () => paths.workflowDetailPath("builtin://autopilot-oncall"),
    url: "/workflows/library/builtin%3A%2F%2Fautopilot-oncall",
    template: "/workflows/library/$workflowRef",
    params: { workflowRef: "builtin://autopilot-oncall" },
  },
  runsPath: { build: () => paths.runsPath(), url: "/workflows/runs", template: "/workflows/runs" },
  runPath: { build: () => paths.runPath("c-1"), url: "/workflows/runs/c-1", template: "/workflows/runs/$runId", params: { runId: "c-1" } },
  automationsPath: { build: () => paths.automationsPath(), url: "/workflows/automations", template: "/workflows/automations" },
  automationPath: {
    build: () => paths.automationPath("t-1"),
    url: "/workflows/automations/t-1",
    template: "/workflows/automations/$triggerId",
    params: { triggerId: "t-1" },
  },
  newWorkflowPath: { build: () => paths.newWorkflowPath(), url: "/workflow/new", template: "/workflow/new" },
  workflowBuilderPath: {
    build: () => paths.workflowBuilderPath("fix-my-pr"),
    url: "/workflow/fix-my-pr",
    template: "/workflow/$workflowName",
    params: { workflowName: "fix-my-pr" },
  },
  settingsPath: { build: () => paths.settingsPath(), url: "/settings", template: "/settings" },
  "settingsPath(section)": {
    build: () => paths.settingsPath("org-ai-accounts"),
    url: "/settings/org-ai-accounts",
    template: "/settings/$section",
    params: { section: "org-ai-accounts" },
  },
  sandboxesPath: {
    build: () => paths.sandboxesPath(),
    url: "/settings/machines",
    template: "/settings/$section",
    params: { section: "machines" },
  },
  machineSettingsPath: {
    build: () => paths.machineSettingsPath("d-1"),
    url: "/settings/machines/d-1",
    template: "/settings/machines/$machineId",
    params: { machineId: "d-1" },
  },
  onboardingPath: { build: () => paths.onboardingPath(), url: "/onboarding", template: "/onboarding" },
  authPath: { build: () => paths.authPath(), url: "/auth", template: "/auth" },
  proxyAuthPath: { build: () => paths.proxyAuthPath(), url: "/auth/proxy", template: "/auth/proxy" },
  forgeSignInPath: { build: () => paths.forgeSignInPath(), url: "/auth/sign-in", template: "/auth/sign-in" },
  forgeForgotPasswordPath: { build: () => paths.forgeForgotPasswordPath(), url: "/auth/forgot", template: "/auth/forgot" },
  forgeResetPasswordPath: { build: () => paths.forgeResetPasswordPath(), url: "/auth/reset", template: "/auth/reset" },
  forgeVerifyEmailPath: { build: () => paths.forgeVerifyEmailPath(), url: "/auth/verify", template: "/auth/verify" },
  forgeMagicLinkPath: { build: () => paths.forgeMagicLinkPath(), url: "/auth/magic", template: "/auth/magic" },
  forgeSocialCallbackPath: {
    build: () => paths.forgeSocialCallbackPath(),
    url: "/auth/social/callback",
    template: "/auth/social/callback",
  },
  confirmEmailChangePath: {
    build: () => paths.confirmEmailChangePath(),
    url: "/auth/account/email/confirm",
    template: "/auth/account/email/confirm",
  },
  verifyEmailPath: { build: () => paths.verifyEmailPath(), url: "/verify-email", template: "/verify-email" },
  resetPasswordPath: { build: () => paths.resetPasswordPath(), url: "/reset-password", template: "/reset-password" },
  upgradePath: { build: () => paths.upgradePath(), url: "/upgrade", template: "/upgrade" },
  claimPath: { build: () => paths.claimPath(), url: "/claim", template: "/claim" },
  acceptInvitePath: { build: () => paths.acceptInvitePath(), url: "/accept-invite", template: "/accept-invite" },
  oauthCliPath: { build: () => paths.oauthCliPath(), url: "/oauth/cli", template: "/oauth/cli" },
  oauthDevicePath: { build: () => paths.oauthDevicePath(), url: "/oauth/device", template: "/oauth/device" },
  oauthConsentPath: { build: () => paths.oauthConsentPath(), url: "/oauth/consent", template: "/oauth/consent" },
  connectionOAuthCallbackPath: { build: () => paths.connectionOAuthCallbackPath(), url: "/connections/oauth/callback", template: "/connections/oauth/callback" },
  mobileHomePath: { build: () => paths.mobileHomePath(), url: "/m", template: "/m" },
  mobileChatsPath: { build: () => paths.mobileChatsPath(), url: "/m/chats", template: "/m/chats" },
  mobileChatPath: { build: () => paths.mobileChatPath("c-1"), url: "/m/chats/c-1", template: "/m/chats/$chatId", params: { chatId: "c-1" } },
  mobileChatWorkflowPath: {
    build: () => paths.mobileChatWorkflowPath("c-1"),
    url: "/m/chats/c-1/workflow",
    template: "/m/chats/$chatId/workflow",
    params: { chatId: "c-1" },
  },
  mobileNewChatPath: { build: () => paths.mobileNewChatPath(), url: "/m/new", template: "/m/new" },
  mobileMachinesPath: { build: () => paths.mobileMachinesPath(), url: "/m/daemons", template: "/m/daemons" },
  mobileMachinePath: { build: () => paths.mobileMachinePath("d-1"), url: "/m/daemons/d-1", template: "/m/daemons/$daemonId", params: { daemonId: "d-1" } },
  mobileAccountPath: { build: () => paths.mobileAccountPath(), url: "/m/account", template: "/m/account" },
  mobileProjectsPath: { build: () => paths.mobileProjectsPath(), url: "/m/projects", template: "/m/projects" },
  mobileSearchPath: { build: () => paths.mobileSearchPath(), url: "/m/search", template: "/m/search" },
  mobileWorkflowsPath: { build: () => paths.mobileWorkflowsPath(), url: "/m/workflows", template: "/m/workflows" },
  mobileWorkflowPath: {
    build: () => paths.mobileWorkflowPath("fix-my-pr"),
    url: "/m/workflows/fix-my-pr",
    template: "/m/workflows/$workflowName",
    params: { workflowName: "fix-my-pr" },
  },
  mobileInboxPath: { build: () => paths.mobileInboxPath(), url: "/m/inbox", template: "/m/inbox" },
  mobileRunsPath: { build: () => paths.mobileRunsPath(), url: "/m/runs", template: "/m/runs" },
  mobileAutomationPath: {
    build: () => paths.mobileAutomationPath("t-1"),
    url: "/m/automations/t-1",
    template: "/m/automations/$triggerId",
    params: { triggerId: "t-1" },
  },
  mobileDiagnosticsPath: { build: () => paths.mobileDiagnosticsPath(), url: "/m/diagnostics", template: "/m/diagnostics" },
  mobileSettingsPath: { build: () => paths.mobileSettingsPath(), url: "/m/settings", template: "/m/settings" },
  mobileGitHubPath: { build: () => paths.mobileGitHubPath(), url: "/m/github", template: "/m/github" },
  // The console's builders (Console/paths.ts) go through the same withOrg.
  consoleBase: { build: () => consoleBase("shop"), url: "/forge/p/shop", template: "/forge/p/$project", params: { project: "shop" } },
  "consolePath(environment)": {
    build: () => consolePath("shop", { kind: "environment", env: "prod" }),
    url: "/forge/p/shop/environments/prod",
    template: "/forge/p/$project/environments/$env",
    params: { project: "shop", env: "prod" },
  },
  "orgPath(observability)": { build: () => orgPath({ kind: "observability" }), url: "/forge/observability", template: "/forge/observability" },
  "orgPath(obs-view)": {
    build: () => orgPath({ kind: "obs-view", view: "logs" }),
    url: "/forge/observability/logs",
    template: "/forge/observability/$view",
    params: { view: "logs" },
  },
  orgUsagePath: { build: () => orgUsagePath(), url: "/settings/billing", template: "/settings/$section", params: { section: "billing" } },
};


/**
 * With the flag on, in the org `acme`: each org-scoped builder's URL. A row not
 * named here is not org-scoped (yet: settings and machines U8, mobile U15,
 * onboarding R11; never: sign-in, OAuth, claims, invitations) and keeps
 * today's URL in both modes.
 */
const ORG_URLS: Record<string, string> = {
  homePath: "/o/acme",
  projectChatsPath: "/o/acme/project/p-1",
  projectsPath: "/o/acme/projects",
  projectPath: "/o/acme/projects/p-1",
  "projectPath(tab)": "/o/acme/projects/p-1/settings",
  appPath: "/o/acme/apps/shop",
  chatPath: "/o/acme/c/c-1",
  threadPath: "/o/acme/c/c-1/t/t-1",
  newChatPath: "/o/acme/new",
  inboxPath: "/o/acme/inbox",
  gtmPath: "/o/acme/gtm/ideas",
  gtmIdeaPath: "/o/acme/gtm/ideas/idea-1",
  gtmLibraryPath: "/o/acme/gtm/strategy?view=library&entry=e-1",
  usagePath: "/o/acme/usage",
  usageBillingPath: "/o/acme/usage",
  connectorsPath: "/o/acme/connectors",
  forgeLandingPath: "/o/acme/forge",
  workflowsPath: "/o/acme/workflows",
  workflowLibraryPath: "/o/acme/workflows/library",
  workflowDetailPath: "/o/acme/workflows/library/builtin%3A%2F%2Fautopilot-oncall",
  runsPath: "/o/acme/workflows/runs",
  runPath: "/o/acme/workflows/runs/c-1",
  automationsPath: "/o/acme/workflows/automations",
  automationPath: "/o/acme/workflows/automations/t-1",
  newWorkflowPath: "/o/acme/workflow/new",
  workflowBuilderPath: "/o/acme/workflow/fix-my-pr",
  consoleBase: "/o/acme/apps/shop",
  "consolePath(environment)": "/o/acme/apps/shop/environments/prod",
  "orgPath(observability)": "/o/acme/observability",
  "orgPath(obs-view)": "/o/acme/observability/logs",
};

/** Builders that take an id, with the template the old call sites passed it to. */
const ID_BUILDERS: [string, (id: string) => string, string, string][] = [
  ["projectChatsPath", paths.projectChatsPath, "/project/$projectId", "projectId"],
  ["projectPath", (id) => paths.projectPath(id), "/projects/$projectId", "projectId"],
  ["appPath", (id) => paths.appPath(id), "/forge/p/$project", "project"],
  ["chatPath", paths.chatPath, "/c/$chatId", "chatId"],
  ["threadPath(thread)", (id) => paths.threadPath("c-1", id), "/c/c-1/t/$threadId", "threadId"],
  ["threadPath(chat)", (id) => paths.threadPath(id, "t-1"), "/c/$chatId/t/t-1", "chatId"],
  ["workflowDetailPath", paths.workflowDetailPath, "/workflows/library/$workflowRef", "workflowRef"],
  ["runPath", paths.runPath, "/workflows/runs/$runId", "runId"],
  ["automationPath", paths.automationPath, "/workflows/automations/$triggerId", "triggerId"],
  ["workflowBuilderPath", paths.workflowBuilderPath, "/workflow/$workflowName", "workflowName"],
  ["machineSettingsPath", paths.machineSettingsPath, "/settings/machines/$machineId", "machineId"],
  ["mobileChatPath", paths.mobileChatPath, "/m/chats/$chatId", "chatId"],
  ["mobileMachinePath", paths.mobileMachinePath, "/m/daemons/$daemonId", "daemonId"],
  ["mobileWorkflowPath", paths.mobileWorkflowPath, "/m/workflows/$workflowName", "workflowName"],
  ["mobileAutomationPath", paths.mobileAutomationPath, "/m/automations/$triggerId", "triggerId"],
  ["consoleBase", consoleBase, "/forge/p/$project", "project"],
];

/** A legacy template's place in the org tree (routes.tsx, forgeConsoleRoutes.tsx `form: 'org'`). */
function orgTemplateOf(template: string): string {
  if (template === "/") return "/o/$org";
  if (template.startsWith("/forge/p/")) return `/o/$org/apps/${template.slice("/forge/p/".length)}`;
  if (template.startsWith("/forge/observability")) return `/o/$org${template.slice("/forge".length)}`;
  return `/o/$org${template}`;
}

/** A router with each template as a route, as the app's route trees declare them, both forms. */
function routerOf(templates: string[]) {
  const root = createRootRoute();
  const children = [...new Set(templates)].map((path) => createRoute({ getParentRoute: () => root, path }));
  return createRouter({ routeTree: root.addChildren(children), history: createMemoryHistory({ initialEntries: ["/"] }) });
}

const legacyTemplates = [...Object.values(ROWS).flatMap((row) => (row.template ? [row.template] : [])), ...ID_BUILDERS.map(([, , template]) => template)];
const router = routerOf([...legacyTemplates, ...legacyTemplates.map(orgTemplateOf)]);

// The router's types know only the app's registered tree; this one is built here.
const hrefOf = (options: { to: string; params?: Record<string, string> }) => router.buildLocation(options as never).href;

/** The org the tab is in: what a builder names with no route org and no `ctx.org`. */
function bindTabOrg() {
  useActiveOrgStore.setState({ effectiveId: "org-1", segments: { "org-1": "acme", "org-2": "side" } });
}

afterEach(() => {
  resetShellForTests();
  useRouteOrgScope.setState({ scope: { kind: "legacy" } });
  useActiveOrgStore.setState({ effectiveId: null, segments: {} });
  delete (globalThis as { __RELIANT_ROUTER?: unknown }).__RELIANT_ROUTER;
});

describe("lib/paths", () => {
  it("covers every builder it exports", () => {
    const builders = Object.entries(paths)
      .filter(([name, value]) => typeof value === "function" && name.endsWith("Path"))
      .map(([name]) => name);
    const tested = new Set(Object.keys(ROWS).map((key) => key.replace(/\(.*$/, "")));
    expect(builders.filter((name) => !tested.has(name))).toEqual([]);
  });

  it("names only rows that exist in the org table", () => {
    expect(Object.keys(ORG_URLS).filter((name) => !(name in ROWS))).toEqual([]);
  });

  it("narrows Projects to its apps or its sites, the way the router writes the search", () => {
    expect(paths.projectsHref()).toBe("/projects");
    expect(paths.projectsHref("apps")).toBe(hrefOf({ to: "/projects", search: { show: "apps" } } as never));
    expect(paths.projectsHref("sites")).toBe("/projects?show=sites");
  });

  it("opens Connectors on a tab, Integrations by default", () => {
    expect(paths.connectorsPath("integrations")).toBe("/connectors");
    expect(paths.connectorsPath("mcp")).toBe(hrefOf({ to: "/connectors", search: { tab: "mcp" } } as never));
  });

  it("joins a search the way the router writes one", () => {
    expect(paths.withSearch(paths.runsPath(), { project: "p-1", allProjects: true, empty: "" })).toBe(
      hrefOf({ to: "/workflows/runs", search: { project: "p-1", allProjects: true } } as never),
    );
  });
});

describe.each([
  { mode: "flag off", on: false },
  { mode: "flag on (web.org_routes)", on: true },
])("every builder, $mode", ({ on }) => {
  beforeEach(() => {
    setRuntimeConfig(on ? { ORG_ROUTES: "true" } : {});
    resetShellForTests("reliant");
    // An org is known in both modes: with the flag off it must change nothing.
    bindTabOrg();
  });

  const orgScoped = (name: string) => on && name in ORG_URLS;
  const expected = (name: string, row: Row) => (orgScoped(name) ? ORG_URLS[name]! : row.url);

  it.each(Object.entries(ROWS))("%s", (name, row) => {
    expect(row.build()).toBe(expected(name, row));
  });

  it.each(Object.entries(ROWS).filter(([, row]) => row.template))("%s routes where its template does", (name, row) => {
    const template = orgScoped(name) ? orgTemplateOf(row.template!) : row.template!;
    const params = orgScoped(name) ? { ...row.params, org: "acme" } : row.params;
    expect(hrefOf({ to: row.build() })).toBe(hrefOf({ to: template, params }));
    expect(hrefOf({ to: row.build() })).toBe(expected(name, row));
  });

  it.each(ID_BUILDERS)("%s encodes an awkward id as the router encodes a param", (name, build, template, param) => {
    const inOrg = orgScoped(name.replace(/\(.*$/, ""));
    expect(hrefOf({ to: build(AWKWARD) })).toBe(
      hrefOf({ to: inOrg ? orgTemplateOf(template) : template, params: { [param]: AWKWARD, ...(inOrg ? { org: "acme" } : {}) } }),
    );
  });
});

describe("the org segment", () => {
  beforeEach(() => {
    resetShellForTests("reliant");
    bindTabOrg();
  });

  it("is in no path with the flag off, whichever org a link names", () => {
    expect(withOrg("/inbox", { org: "side" })).toBe("/inbox");
    expect(paths.inboxPath({ org: "side" })).toBe("/inbox");
    expect(consoleBase("shop", { org: "side" })).toBe("/forge/p/shop");
  });

  describe("with the flag on", () => {
    beforeEach(() => setRuntimeConfig({ ORG_ROUTES: "true" }));

    it("names the org a link asks for", () => {
      expect(paths.inboxPath({ org: "side" })).toBe("/o/side/inbox");
      expect(consolePath("shop", { kind: "releases" }, { org: "side" })).toBe("/o/side/apps/shop/releases");
      expect(orgPath({ kind: "obs-view", view: "traces" }, { org: "side" })).toBe("/o/side/observability/traces");
    });

    it("names the org of the route on screen before the tab's", () => {
      commitRouteOrgScope({ kind: "member", org: { id: "org-2", slug: "side" } });
      expect(paths.runsPath()).toBe("/o/side/workflows/runs");
    });

    it("puts an org without a slug in by its id", () => {
      useActiveOrgStore.setState({ effectiveId: "org 3", segments: { "org 3": "org 3" } });
      expect(paths.homePath()).toBe("/o/org%203");
    });

    it("keeps today's org-less form for a link that must not name the tab's org ({ org: null })", () => {
      expect(paths.runsPath({ org: null })).toBe("/workflows/runs");
      expect(paths.automationPath("t-1", { org: null })).toBe("/workflows/automations/t-1");
      expect(paths.runsPath()).toBe("/o/acme/workflows/runs");
    });

    it("carries an already-encoded segment byte for byte (encodedSegment)", () => {
      for (const raw of ["a%2Fb", "a%252Fb", "builtin%3A%2F%2Fget-it-right", "raw:colon"]) {
        expect(paths.workflowDetailPath(paths.encodedSegment(raw))).toBe(`/o/acme/workflows/library/${raw}`);
        expect(paths.mobileChatPath(paths.encodedSegment(raw))).toBe(`/m/chats/${raw}`);
      }
      // A canonical encoding is what the plain builder makes of the decoded value.
      expect(paths.workflowDetailPath(paths.encodedSegment("builtin%3A%2F%2Fget-it-right"))).toBe(paths.workflowDetailPath("builtin://get-it-right"));
    });

    it("keeps today's path while no org is known: it forwards once one is", () => {
      useActiveOrgStore.setState({ effectiveId: null, segments: {} });
      expect(paths.inboxPath()).toBe("/inbox");
      useActiveOrgStore.setState({ effectiveId: "org-9", segments: {} });
      expect(paths.inboxPath()).toBe("/inbox");
    });

    it("builds the org form on the forge host too, which mounts the same tree (U6)", () => {
      resetShellForTests("forge");
      expect(paths.projectsHref("apps")).toBe("/o/acme/projects?show=apps");
      expect(paths.projectsHref("sites")).toBe("/o/acme/projects?show=sites");
      expect(consoleBase("shop")).toBe("/o/acme/apps/shop");
    });

    it("never changes a path that is not org-scoped", () => {
      expect(paths.settingsPath("billing")).toBe("/settings/billing");
      expect(paths.mobileChatPath("c-1")).toBe("/m/chats/c-1");
      expect(paths.onboardingPath()).toBe("/onboarding");
      expect(orgUsagePath()).toBe("/settings/billing");
    });
  });

  it.each(Object.entries(ORG_URLS))("reads %s back to today's path", (name, url) => {
    expect(orgFreePath(url)).toBe(ROWS[name]!.url);
    expect(orgOfPath(url)).toBe("acme");
  });

  it("leaves a path outside the org tree as it is", () => {
    for (const path of ["/", "/inbox", "/oauth/cli", "/o", "/forge/p/shop", "/settings/members"]) {
      expect(orgFreePath(path)).toBe(path);
      expect(orgOfPath(path)).toBeNull();
    }
    expect(orgFreePath("/o/acme/")).toBe("/");
  });

  it.each([
    ["/o/acme", "/o/side"],
    ["/o/acme/inbox", "/o/side/inbox"],
    ["/o/acme/project/p-1", "/o/side"],
    ["/o/acme/projects", "/o/side/projects"],
    ["/o/acme/projects/p-1", "/o/side/projects"],
    ["/o/acme/projects/p-1/settings", "/o/side/projects"],
    ["/o/acme/projects/new", "/o/side/projects/new"],
    ["/o/acme/c/c-1", "/o/side"],
    ["/o/acme/c/c-1/t/t-1", "/o/side"],
    ["/o/acme/new", "/o/side/new"],
    ["/o/acme/apps", "/o/side/apps"],
    ["/o/acme/apps/shop/releases/v1", "/o/side/apps"],
    ["/o/acme/observability/logs", "/o/side/observability/logs"],
    ["/o/acme/workflows/library", "/o/side/workflows/library"],
    ["/o/acme/workflows/library/my-flow", "/o/side/workflows/library"],
    ["/o/acme/workflows/library/builtin%3A%2F%2Fget-it-right", "/o/side/workflows/library/builtin%3A%2F%2Fget-it-right"],
    ["/o/acme/workflows/runs/c-1", "/o/side/workflows/runs"],
    ["/o/acme/workflows/automations/t-1", "/o/side/workflows/library"],
    ["/o/acme/workflow/my-flow", "/o/side/workflows/library"],
    ["/o/acme/workflow/new", "/o/side/workflow/new"],
  ])("the switcher opens %s in another org at %s, minus the old org's ids", (from, to) => {
    expect(orgSwitchPath(from, "side")).toBe(to);
  });

  it("names the checkout behind an app's console when the link knows it (`?project=`), and nothing when not", () => {
    expect(paths.consoleCheckoutSearch("p-1")).toEqual({ project: "p-1" });
    expect(paths.consoleCheckoutSearch(undefined)).toBeUndefined();
    expect(paths.withMoreSearch("/forge/p/shop/releases?env=prod", { project: "p-1" })).toBe("/forge/p/shop/releases?env=prod&project=p-1");
    expect(paths.withMoreSearch("/forge/p/shop", { project: "p-1" })).toBe("/forge/p/shop?project=p-1");
  });

  it("builds the GTM strategy library's URL, open on one entry", () => {
    expect(paths.gtmLibraryPath()).toBe(hrefOf({ to: "/gtm/strategy", search: { view: "library" } } as never));
    expect(paths.gtmLibraryPath("founder-led-calls")).toBe(hrefOf({ to: "/gtm/strategy", search: { view: "library", entry: "founder-led-calls" } } as never));
  });
});
