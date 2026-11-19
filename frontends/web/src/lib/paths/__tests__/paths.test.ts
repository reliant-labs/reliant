// Copyright (c) 2025 Reliant Labs

/**
 * Every builder returns the URL the app used before it existed (U1 changes no
 * URL), and the router treats it as the same URL the old `to` + `params` form
 * built: `navigate({ to: runPath(id) })` must land exactly where
 * `navigate({ to: "/workflows/runs/$runId", params: { runId: id } })` did,
 * awkward ids included.
 */

import { createMemoryHistory, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { describe, expect, it } from "vitest";

import { consoleBase, consolePath, orgPath, orgUsagePath } from "@/components/Forge/Console/paths";

import * as paths from "..";

/** An id that needs encoding: a workflow ref, or a name a person typed. */
const AWKWARD = "builtin://get it/right?#%";

interface Row {
  built: string;
  /** Today's URL, as the app spelled it. */
  url: string;
  /** The route template and params the call sites used to pass, where there was one. */
  template?: string;
  params?: Record<string, string>;
}

const ROWS: Record<string, Row> = {
  homePath: { built: paths.homePath(), url: "/", template: "/" },
  projectPath: { built: paths.projectPath("p-1"), url: "/project/p-1", template: "/project/$projectId", params: { projectId: "p-1" } },
  inboxPath: { built: paths.inboxPath(), url: "/inbox", template: "/inbox" },
  appsPath: { built: paths.appsPath(), url: "/apps", template: "/apps" },
  sitesPath: { built: paths.sitesPath(), url: "/sites", template: "/sites" },
  usagePath: { built: paths.usagePath(), url: "/usage", template: "/usage" },
  forgeLandingPath: { built: paths.forgeLandingPath(), url: "/forge", template: "/forge" },
  forgeDemoPath: { built: paths.forgeDemoPath(), url: "/forge/demo", template: "/forge/demo" },
  workflowsPath: { built: paths.workflowsPath(), url: "/workflows", template: "/workflows" },
  workflowLibraryPath: { built: paths.workflowLibraryPath(), url: "/workflows/library", template: "/workflows/library" },
  workflowDetailPath: {
    built: paths.workflowDetailPath("builtin://autopilot-oncall"),
    url: "/workflows/library/builtin%3A%2F%2Fautopilot-oncall",
    template: "/workflows/library/$workflowRef",
    params: { workflowRef: "builtin://autopilot-oncall" },
  },
  runsPath: { built: paths.runsPath(), url: "/workflows/runs", template: "/workflows/runs" },
  runPath: { built: paths.runPath("c-1"), url: "/workflows/runs/c-1", template: "/workflows/runs/$runId", params: { runId: "c-1" } },
  automationsPath: { built: paths.automationsPath(), url: "/workflows/automations", template: "/workflows/automations" },
  automationPath: {
    built: paths.automationPath("t-1"),
    url: "/workflows/automations/t-1",
    template: "/workflows/automations/$triggerId",
    params: { triggerId: "t-1" },
  },
  newWorkflowPath: { built: paths.newWorkflowPath(), url: "/workflow/new", template: "/workflow/new" },
  workflowBuilderPath: {
    built: paths.workflowBuilderPath("fix-my-pr"),
    url: "/workflow/fix-my-pr",
    template: "/workflow/$workflowName",
    params: { workflowName: "fix-my-pr" },
  },
  settingsPath: { built: paths.settingsPath(), url: "/settings", template: "/settings" },
  "settingsPath(section)": {
    built: paths.settingsPath("org-ai-accounts"),
    url: "/settings/org-ai-accounts",
    template: "/settings/$section",
    params: { section: "org-ai-accounts" },
  },
  forgeSettingsPath: {
    built: paths.forgeSettingsPath("members"),
    url: "/settings/members",
    template: "/settings/$section",
    params: { section: "members" },
  },
  machineSettingsPath: {
    built: paths.machineSettingsPath("d-1"),
    url: "/settings/machines/d-1",
    template: "/settings/machines/$machineId",
    params: { machineId: "d-1" },
  },
  onboardingPath: { built: paths.onboardingPath(), url: "/onboarding", template: "/onboarding" },
  authPath: { built: paths.authPath(), url: "/auth", template: "/auth" },
  proxyAuthPath: { built: paths.proxyAuthPath(), url: "/auth/proxy", template: "/auth/proxy" },
  verifyEmailPath: { built: paths.verifyEmailPath(), url: "/verify-email", template: "/verify-email" },
  resetPasswordPath: { built: paths.resetPasswordPath(), url: "/reset-password", template: "/reset-password" },
  upgradePath: { built: paths.upgradePath(), url: "/upgrade", template: "/upgrade" },
  claimPath: { built: paths.claimPath(), url: "/claim", template: "/claim" },
  acceptInvitePath: { built: paths.acceptInvitePath(), url: "/accept-invite", template: "/accept-invite" },
  oauthCliPath: { built: paths.oauthCliPath(), url: "/oauth/cli", template: "/oauth/cli" },
  oauthDevicePath: { built: paths.oauthDevicePath(), url: "/oauth/device", template: "/oauth/device" },
  oauthConsentPath: { built: paths.oauthConsentPath(), url: "/oauth/consent", template: "/oauth/consent" },
  connectionOAuthCallbackPath: { built: paths.connectionOAuthCallbackPath(), url: "/connections/oauth/callback", template: "/connections/oauth/callback" },
  mobileHomePath: { built: paths.mobileHomePath(), url: "/m", template: "/m" },
  mobileChatsPath: { built: paths.mobileChatsPath(), url: "/m/chats", template: "/m/chats" },
  mobileChatPath: { built: paths.mobileChatPath("c-1"), url: "/m/chats/c-1", template: "/m/chats/$chatId", params: { chatId: "c-1" } },
  mobileChatWorkflowPath: {
    built: paths.mobileChatWorkflowPath("c-1"),
    url: "/m/chats/c-1/workflow",
    template: "/m/chats/$chatId/workflow",
    params: { chatId: "c-1" },
  },
  mobileNewChatPath: { built: paths.mobileNewChatPath(), url: "/m/new", template: "/m/new" },
  mobileMachinesPath: { built: paths.mobileMachinesPath(), url: "/m/daemons", template: "/m/daemons" },
  mobileMachinePath: { built: paths.mobileMachinePath("d-1"), url: "/m/daemons/d-1", template: "/m/daemons/$daemonId", params: { daemonId: "d-1" } },
  mobileAccountPath: { built: paths.mobileAccountPath(), url: "/m/account", template: "/m/account" },
  mobileProjectsPath: { built: paths.mobileProjectsPath(), url: "/m/projects", template: "/m/projects" },
  mobileSearchPath: { built: paths.mobileSearchPath(), url: "/m/search", template: "/m/search" },
  mobileWorkflowsPath: { built: paths.mobileWorkflowsPath(), url: "/m/workflows", template: "/m/workflows" },
  mobileWorkflowPath: {
    built: paths.mobileWorkflowPath("fix-my-pr"),
    url: "/m/workflows/fix-my-pr",
    template: "/m/workflows/$workflowName",
    params: { workflowName: "fix-my-pr" },
  },
  mobileInboxPath: { built: paths.mobileInboxPath(), url: "/m/inbox", template: "/m/inbox" },
  mobileRunsPath: { built: paths.mobileRunsPath(), url: "/m/runs", template: "/m/runs" },
  mobileAutomationPath: {
    built: paths.mobileAutomationPath("t-1"),
    url: "/m/automations/t-1",
    template: "/m/automations/$triggerId",
    params: { triggerId: "t-1" },
  },
  mobileDiagnosticsPath: { built: paths.mobileDiagnosticsPath(), url: "/m/diagnostics", template: "/m/diagnostics" },
  mobileSettingsPath: { built: paths.mobileSettingsPath(), url: "/m/settings", template: "/m/settings" },
  mobileGitHubPath: { built: paths.mobileGitHubPath(), url: "/m/github", template: "/m/github" },
  // The console's builders (Console/paths.ts) go through the same withOrg.
  consoleBase: { built: consoleBase("shop"), url: "/forge/p/shop", template: "/forge/p/$project", params: { project: "shop" } },
  "consolePath(environment)": {
    built: consolePath("shop", { kind: "environment", env: "prod" }),
    url: "/forge/p/shop/environments/prod",
    template: "/forge/p/$project/environments/$env",
    params: { project: "shop", env: "prod" },
  },
  "orgPath(observability)": { built: orgPath({ kind: "observability" }), url: "/forge/observability", template: "/forge/observability" },
  "orgPath(obs-view)": {
    built: orgPath({ kind: "obs-view", view: "logs" }),
    url: "/forge/observability/logs",
    template: "/forge/observability/$view",
    params: { view: "logs" },
  },
  orgUsagePath: { built: orgUsagePath(), url: "/settings/billing", template: "/settings/$section", params: { section: "billing" } },
};

/** Builders that take an id, with the template the old call sites passed it to. */
const ID_BUILDERS: [string, (id: string) => string, string, string][] = [
  ["projectPath", paths.projectPath, "/project/$projectId", "projectId"],
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

/** A router with each template as a route, as the app's route tree declares them. */
function routerOf(templates: string[]) {
  const root = createRootRoute();
  const children = [...new Set(templates)].map((path) => createRoute({ getParentRoute: () => root, path }));
  return createRouter({ routeTree: root.addChildren(children), history: createMemoryHistory({ initialEntries: ["/"] }) });
}

const router = routerOf([...Object.values(ROWS).flatMap((row) => (row.template ? [row.template] : [])), ...ID_BUILDERS.map(([, , template]) => template)]);

// The router's types know only the app's registered tree; this one is built here.
const hrefOf = (options: { to: string; params?: Record<string, string> }) => router.buildLocation(options as never).href;

describe("lib/paths", () => {
  it("covers every builder it exports", () => {
    const builders = Object.entries(paths)
      .filter(([name, value]) => typeof value === "function" && name.endsWith("Path"))
      .map(([name]) => name);
    const tested = new Set(Object.keys(ROWS).map((key) => key.replace(/\(.*$/, "")));
    expect(builders.filter((name) => !tested.has(name))).toEqual([]);
  });

  it.each(Object.entries(ROWS))("%s is today's URL", (_name, row) => {
    expect(row.built).toBe(row.url);
  });

  it.each(Object.entries(ROWS).filter(([, row]) => row.template))("%s routes where its old `to` did", (_name, row) => {
    expect(hrefOf({ to: row.built })).toBe(hrefOf({ to: row.template!, params: row.params }));
    expect(hrefOf({ to: row.built })).toBe(row.url);
  });

  it.each(ID_BUILDERS)("%s encodes an awkward id as the router encodes a param", (_name, build, template, param) => {
    expect(hrefOf({ to: build(AWKWARD) })).toBe(hrefOf({ to: template, params: { [param]: AWKWARD } }));
  });

  it("joins a search the way the router writes one", () => {
    expect(paths.withSearch(paths.runsPath(), { project: "p-1", allProjects: true, empty: "" })).toBe(
      hrefOf({ to: "/workflows/runs", search: { project: "p-1", allProjects: true } } as never),
    );
  });

  it("puts no org in any path yet (U3 does)", () => {
    expect(paths.withOrg("/inbox", { org: "acme" })).toBe("/inbox");
    expect(paths.inboxPath({ org: "acme" })).toBe("/inbox");
    expect(consoleBase("shop", { org: "acme" })).toBe("/forge/p/shop");
  });
});
