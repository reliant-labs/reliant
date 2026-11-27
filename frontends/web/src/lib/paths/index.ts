// Copyright (c) 2025 Reliant Labs

/**
 * EVERY APP DESTINATION'S PATH, built in one place.
 *
 * A link, a `navigate`, a redirect or a `window.location` hop to a page of
 * this app names its target with one of these builders, never with a string:
 * `navigate({ to: runPath(chatId) })`, `<Link to={settingsPath("ai")}>`,
 * `href={inboxPath()}`. The guard `__tests__/navPaths.guard.test.ts` fails on
 * a path literal anywhere else. The console's pages are built the same way, by
 * `components/Forge/Console/paths.ts` (consolePath, orgPath).
 *
 * Why: the unified-shell design (control-plane
 * docs/design/unified-shell-and-projects.md, U3) puts the org in the URL of
 * every org-scoped page. With every path built here, that is a change to
 * `withOrg` (./org.ts) and to the few builders whose org form is not their
 * path behind a prefix, rather than to ~300 call sites.
 *
 * Each builder returns a PATH (no query string): TanStack's `to` takes the
 * search separately (`navigate({ to: settingsPath("billing"), search: { tab } })`),
 * and `withSearch` (./search.ts) joins the two for an href. Path segments are
 * encoded with encodeURIComponent, which is what the router does to a `$param`,
 * so `to: runPath(id)` and the old `to: "/workflows/runs/$runId", params` are
 * the same URL (`paths.test.ts` holds every builder to today's URL).
 */

import type { SettingsSection } from "../../routeSchemas";
import { orgRootPath, withOrg, type PathContext } from "./org";
import { withSearch } from "./search";

export { withOrg, type PathContext } from "./org";
export { withMoreSearch, withSearch } from "./search";

/**
 * A path segment exactly as a URL already carries it, percent-encoded: for a
 * redirect that moves a segment it captured from one path into another
 * (a retired path's id, a desktop path's mobile twin) byte for byte. Decoding
 * and re-encoding it could change the bytes (`%2F`, `%252F`, a raw `:`) of a
 * link someone printed.
 */
export interface EncodedSegment {
  readonly encoded: string;
}

/** `encoded`, a segment as captured from a URL, for a builder that takes one (see EncodedSegment). */
export function encodedSegment(encoded: string): EncodedSegment {
  return { encoded };
}

/** A builder's path segment: a value to encode, or one already encoded. */
export type Segment = string | EncodedSegment;

const seg = (value: Segment): string => (typeof value === "string" ? encodeURIComponent(value) : value.encoded);

// ── Chats and projects ──────────────────────────────────────────────────────

/** The chat home: every project's chats (`/o/<org>` in the org form). */
export function homePath(ctx?: PathContext): string {
  return withOrg("/", ctx);
}

/**
 * An org's home, `/o/<org>`, whatever the flag says: a way into one of the
 * person's orgs by name (the "no access" page's list). The org routes are
 * always mounted, so it resolves with the flag off too.
 */
export function orgHomePath(org: string): string {
  return orgRootPath(org);
}

/**
 * One chat (R1, control-plane docs/design/unified-shell-and-projects.md §3.1,
 * §9): `/c/<chatId>`, `/o/<org>/c/<chatId>` in the org form. Flat, not under
 * its project: chat ids are global, and a chat can move between projects.
 * Its view state rides in the search (routeSchemas.ts, ChatViewSearch).
 */
export function chatPath(chatId: Segment, ctx?: PathContext): string {
  return withOrg(`/c/${seg(chatId)}`, ctx);
}

/** One thread of a chat, in full view (R2): a spawned agent's or a workflow step's. */
export function threadPath(chatId: Segment, threadId: Segment, ctx?: PathContext): string {
  return withOrg(`/c/${seg(chatId)}/t/${seg(threadId)}`, ctx);
}

/**
 * The new-chat view (R3). Its draft context — the project, machine and
 * workspace it starts in — rides in the search (`?project=&machine=&ws=`,
 * routeSchemas.ts NewChatSearch), so a refresh keeps it.
 */
export function newChatPath(ctx?: PathContext): string {
  return withOrg("/new", ctx);
}

/**
 * The chat home filtered to one project (the sidebar's project filter,
 * `/project/<id>`). Not the project's page: that is projectPath. The design
 * (R6) moves this filter back into the sidebar; until then it is how a
 * chat's project is opened.
 */
export function projectChatsPath(projectId: string, ctx?: PathContext): string {
  return withOrg(`/project/${seg(projectId)}`, ctx);
}

// ── Projects (the merged list, §8.3–§8.4) ──────────────────────────────────

/** The person's projects: their apps and sites, and their code (components/Projects/ProjectsPage.tsx). */
export function projectsPath(ctx?: PathContext): string {
  return withOrg("/projects", ctx);
}

/** What Projects lists: everything (no filter), only the apps, or only the apps with a live site. */
export type ProjectsShow = "apps" | "sites";

/** Projects as an href, narrowed to its apps or its sites (`?show=`); every project without one. */
export function projectsHref(show?: ProjectsShow, ctx?: PathContext): string {
  return withSearch(projectsPath(ctx), { show });
}

/** A project page's sub-pages (design §4, R7–R9, R18); the page itself has none. */
export type ProjectPageTab = "settings" | "workspaces" | "move" | "delete";

/** A project's page: its code, checkouts, apps and chats; `tab` one of its sub-pages. */
export function projectPath(projectId: string, tab?: ProjectPageTab, ctx?: PathContext): string {
  return withOrg(`/projects/${seg(projectId)}${tab ? `/${tab}` : ""}`, ctx);
}

/**
 * One app's console (`/o/<org>/apps/<name>`; `/forge/p/<name>` with org
 * links off): `name` is forge.yaml's `name`. The console's own builders
 * (components/Forge/Console/paths.ts consoleBase) spell the same root. A link
 * that knows which checkout backs the console adds `?project=<Reliant
 * project id>` (consoleCheckoutSearch): several of the person's projects can
 * declare one name, and without it the console asks which.
 */
export function appPath(name: string, ctx?: PathContext): string {
  return withOrg(`/forge/p/${seg(name)}`, ctx);
}

/** The search naming the checkout behind an app's console (`?project=`), or none when unknown. */
export function consoleCheckoutSearch(projectId: string | undefined): { project: string } | undefined {
  return projectId ? { project: projectId } : undefined;
}

/** Everything waiting on the person, across every chat and project. */
export function inboxPath(ctx?: PathContext): string {
  return withOrg("/inbox", ctx);
}

/** A GTM page (components/Gtm): Strategy, Ideas, Campaigns, Calendar, Channels, Positioning & facts. */
export type GtmPage = "strategy" | "ideas" | "campaigns" | "calendar" | "channels" | "positioning";

/** The GTM area (`/gtm` opens Ideas); `page` one of its pages. Gated per org (components/Gtm/shared/access.ts). */
export function gtmPath(page?: GtmPage, ctx?: PathContext): string {
  return withOrg(page ? `/gtm/${page}` : "/gtm", ctx);
}

/** One GTM idea's page: its rationale, evidence and draft. */
export function gtmIdeaPath(ideaId: string, ctx?: PathContext): string {
  return withOrg(`/gtm/ideas/${seg(ideaId)}`, ctx);
}

/** The strategy library (Strategy › Library), open on one entry when `entryKey` is given (`?entry=`). */
export function gtmLibraryPath(entryKey?: string, ctx?: PathContext): string {
  return withSearch(gtmPath("strategy", ctx), { view: "library", entry: entryKey || undefined });
}

// ── Apps (forge) ────────────────────────────────────────────────────────────

/** The org's Usage & billing page (components/Usage/UsageBillingPage.tsx); `usageBillingPath` names the same URL. */
export function usagePath(ctx?: PathContext): string {
  return withOrg("/usage", ctx);
}

/**
 * Usage & billing (components/Usage/UsageBillingPage.tsx): the org's spend
 * over time, per project, AI and sandbox time, plan, credits and invoices.
 * The nav footer's entry; Settings › Billing is its admin side.
 */
export function usageBillingPath(ctx?: PathContext): string {
  return usagePath(ctx);
}

/**
 * The bare console entry, `/forge`: it resolves `?project=` (an old link's
 * app) to that app's console, and otherwise redirects to Projects' apps.
 */
export function forgeLandingPath(ctx?: PathContext): string {
  return withOrg("/forge", ctx);
}

/** The forge demo console, on fixture data (components/Forge/Demo). Not org-scoped: it reads nothing of the person's. */
export function forgeDemoPath(): string {
  return "/forge/demo";
}

// ── Workflows ───────────────────────────────────────────────────────────────

/** The Workflows area's entry: redirects to its Workflows tab. */
export function workflowsPath(ctx?: PathContext): string {
  return withOrg("/workflows", ctx);
}

/** The Workflows tab: every workflow, with its triggers. */
export function workflowLibraryPath(ctx?: PathContext): string {
  return withOrg("/workflows/library", ctx);
}

/** One workflow's page in the Workflows area, by its ref (`builtin://get-it-right`, a user workflow's name). */
export function workflowDetailPath(workflowRef: Segment, ctx?: PathContext): string {
  return withOrg(`/workflows/library/${seg(workflowRef)}`, ctx);
}

/** The Runs tab: every execution. */
export function runsPath(ctx?: PathContext): string {
  return withOrg("/workflows/runs", ctx);
}

/** One run's page. A run is a chat: `runId` is its chat id. */
export function runPath(runId: Segment, ctx?: PathContext): string {
  return withOrg(`/workflows/runs/${seg(runId)}`, ctx);
}

/** The retired Automations tab; redirects to the Workflows tab filtered to what runs by itself. */
export function automationsPath(ctx?: PathContext): string {
  return withOrg("/workflows/automations", ctx);
}

/** The redirect an automation id resolves through, to its row on its workflow's page. */
export function automationPath(triggerId: Segment, ctx?: PathContext): string {
  return withOrg(`/workflows/automations/${seg(triggerId)}`, ctx);
}

/** The workflow builder's New workflow dialog. */
export function newWorkflowPath(ctx?: PathContext): string {
  return withOrg("/workflow/new", ctx);
}

/** The workflow builder, open on one workflow (`builtin://…` or a user workflow's name). */
export function workflowBuilderPath(workflowName: string, ctx?: PathContext): string {
  return withOrg(`/workflow/${seg(workflowName)}`, ctx);
}

// ── Settings ────────────────────────────────────────────────────────────────
//
// Not org-scoped yet: personal and org sections share /settings/<section>
// until the settings split (D10, U8) moves the org ones to /o/<org>/settings
// and machines to /o/<org>/machines.

/** Settings, at one section; bare `/settings` opens the last-visited one. */
export function settingsPath(section?: SettingsSection): string {
  return section ? `/settings/${section}` : "/settings";
}

/** The Connectors page's tabs: the integrations agents act through, and MCP servers. */
export type ConnectorsTab = "integrations" | "mcp";

/**
 * Connectors (components/Connectors/ConnectorsPage.tsx, U6d): the
 * integrations and the MCP servers agents use, one page with a tab each
 * (`?tab=mcp`; absent: Integrations). The Agents area's Connectors row.
 */
export function connectorsPath(tab?: ConnectorsTab, ctx?: PathContext): string {
  return withSearch(withOrg("/connectors", ctx), { tab: tab === "integrations" ? undefined : tab });
}

/**
 * Agent sandboxes: the machines page, Settings › Agent sandboxes (the UI's
 * name for machines; the section id, routes and APIs keep "machines").
 * `/machines` and `/o/<org>/machines` redirect here.
 */
export function sandboxesPath(): string {
  return settingsPath("machines");
}

/** One machine's page in Settings › Agent sandboxes. */
export function machineSettingsPath(machineId: string): string {
  return `/settings/machines/${seg(machineId)}`;
}

// ── Onboarding ──────────────────────────────────────────────────────────────

/** Setting up chats (a machine, a model). `/o/<org>/setup` in the design (R11); not moved by U3. */
export function onboardingPath(): string {
  return "/onboarding";
}

// ── Signing in, and links into the app from outside it ─────────────────────
//
// Never org-scoped: they come before an org is known, or choose one.

/** Sign in or sign up (`?redirect=` returns there afterwards). */
export function authPath(): string {
  return "/auth";
}

/** Verify the email address a session signed up with. */
export function verifyEmailPath(): string {
  return "/verify-email";
}

/** Set a new password (the reset email's link lands here signed in). */
export function resetPasswordPath(): string {
  return "/reset-password";
}

// Forge sign-in's own pages (AUTH_PROVIDER "forge"). reliant-api's login
// broker mails links to, and returns Google/GitHub sign-ins to, these exact
// paths (forge pkg/devidp; internal/app/login_broker.go), so they are fixed.

/** forge's sign-in path: where the broker sends a failed provider round trip (`?social=failed`). */
export function forgeSignInPath(): string {
  return "/auth/sign-in";
}

/** Ask for a password-reset link (forge sign-in). */
export function forgeForgotPasswordPath(): string {
  return "/auth/forgot";
}

/** A password-reset link's page (`?userID=…&code=…`, forge sign-in). */
export function forgeResetPasswordPath(): string {
  return "/auth/reset";
}

/** An address-confirmation link's page (`?userID=…&code=…`, forge sign-in). */
export function forgeVerifyEmailPath(): string {
  return "/auth/verify";
}

/** An emailed sign-in link's page (`?code=…`, forge sign-in). */
export function forgeMagicLinkPath(): string {
  return "/auth/magic";
}

/** Where Google or GitHub return (`?id=…&token=…`, forge sign-in). */
export function forgeSocialCallbackPath(): string {
  return "/auth/social/callback";
}

/** A changed address's confirmation link (forge account settings, `?code=…`). */
export function confirmEmailChangePath(): string {
  return "/auth/account/email/confirm";
}

/** The workspace proxy's sign-in hop (`?return=` is the proxied URL to go back to). */
export function proxyAuthPath(): string {
  return "/auth/proxy";
}

/** Give an anonymous session an email (`?returnTo=`). */
export function upgradePath(): string {
  return "/upgrade";
}

/** Claim a no-signup deploy (`/claim#rcl_<token>`). */
export function claimPath(): string {
  return "/claim";
}

/** Accept an org invitation (`/accept-invite#rinv_<token>`). */
export function acceptInvitePath(): string {
  return "/accept-invite";
}

/** CLI login consent (`forge cloud login`, `reliant auth login`). */
export function oauthCliPath(): string {
  return "/oauth/cli";
}

/** Self-hosted machine pairing (`reliant daemon start` prints it with its code). */
export function oauthDevicePath(): string {
  return "/oauth/device";
}

/** The OAuth 2.1 consent screen (Supabase's OAuth server redirects here). */
export function oauthConsentPath(): string {
  return "/oauth/consent";
}

/** Where an integration connection's OAuth flow returns (Go: connections.AppCallbackPath). */
export function connectionOAuthCallbackPath(): string {
  return "/connections/oauth/callback";
}

// ── Mobile (`/m/*`) ─────────────────────────────────────────────────────────
//
// Not org-scoped yet: the tree moves under /o/<org>/m in U15 (D11).

/** The mobile entry; redirects to the chat list. */
export function mobileHomePath(): string {
  return "/m";
}

export function mobileChatsPath(): string {
  return "/m/chats";
}

export function mobileChatPath(chatId: Segment): string {
  return `/m/chats/${seg(chatId)}`;
}

/** A workflow chat's run, as the mobile surface shows it. */
export function mobileChatWorkflowPath(chatId: Segment): string {
  return `/m/chats/${seg(chatId)}/workflow`;
}

export function mobileNewChatPath(): string {
  return "/m/new";
}

export function mobileMachinesPath(): string {
  return "/m/daemons";
}

export function mobileMachinePath(daemonId: string): string {
  return `/m/daemons/${seg(daemonId)}`;
}

export function mobileAccountPath(): string {
  return "/m/account";
}

export function mobileProjectsPath(): string {
  return "/m/projects";
}

export function mobileSearchPath(): string {
  return "/m/search";
}

export function mobileWorkflowsPath(): string {
  return "/m/workflows";
}

export function mobileWorkflowPath(workflowName: Segment): string {
  return `/m/workflows/${seg(workflowName)}`;
}

export function mobileInboxPath(): string {
  return "/m/inbox";
}

export function mobileRunsPath(): string {
  return "/m/runs";
}

/** Resolves an automation id to its workflow, on the mobile surface. */
export function mobileAutomationPath(triggerId: Segment): string {
  return `/m/automations/${seg(triggerId)}`;
}

export function mobileDiagnosticsPath(): string {
  return "/m/diagnostics";
}

/** Mobile settings; one section is `?section=`, not a path segment. */
export function mobileSettingsPath(): string {
  return "/m/settings";
}

export function mobileGitHubPath(): string {
  return "/m/github";
}
