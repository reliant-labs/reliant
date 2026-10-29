/**
 * Centralized Zod schemas for every route's search params.
 *
 * These live in a separate file (not in `routes.tsx`) so that tests and
 * non-React code can import them without dragging in the entire route-tree
 * dependency graph (App, Monaco, gRPC clients, etc.). `routes.tsx` imports
 * from here; so does `routes.test.tsx`.
 *
 * tanstack-router already JSON-parses values during parseSearch, so these
 * just validate structure; navigate() with object values round-trips cleanly
 * without any manual encodeURIComponent/JSON.stringify in callers.
 */

import { z } from "zod";
import { ONBOARDING_STEP_IDS } from "./components/Onboarding/constants";
import type { LaunchPlan } from "./components/OnboardingFlow/types";
import { COMPARE_MODES, REFRESH_INTERVALS } from "./components/Forge/Observability/Dashboards/dashboardModel";
import { RELEASE_TABS, STATUS_FILTERS } from "./components/Forge/Releases/releaseModel";
import { OVERVIEW_KIND_FILTERS, OVERVIEW_STATUS_FILTERS, WORKLOAD_TABS } from "./components/Forge/Workloads/workloadModel";
import { ALERTS_TABS, OBS_VIEWS, type ObsSearch } from "./components/Forge/Observability/obsSearch";
import { HEALTH_WINDOWS } from "./services/controlPlane/observability/deliveryTypes";
import { ISSUE_PLATFORMS, ISSUE_SORTS } from "./services/controlPlane/observability/issuesTypes";
import { RANGE_PRESETS } from "./services/controlPlane/observability/window";

// The guided tour's "active step" lives in the URL as `?tour=<step-id>` —
// absence means the wizard is not active. Adding this to every search schema
// the wizard may render on keeps tanstack-router's strict validation from
// stripping the param when the user navigates between hub / builder / chat
// while in the middle of a step.
const tourParam = z.enum(ONBOARDING_STEP_IDS).optional();

export const launchPlanSchema = z
  .object({
    // Keep in sync with OnboardingIntent in
    // components/OnboardingFlow/types.ts — only the values the rendered
    // wizard can set are accepted here.
    intent: z.enum(["build_app", "existing_codebase", "chat"]).optional(),
    compute: z
      .enum(["cloud_free_trial", "cloud_paid", "local_daemon", "undecided"])
      .optional(),
    repo: z
      .object({
        provider: z.enum(["github", "gitlab", "bitbucket"]),
        url: z.string(),
        branch: z.string().optional(),
      })
      .optional(),
    localPath: z.string().optional(),
    projectName: z.string().optional(),
    workflowId: z.string().optional(),
    modelProvider: z
      .enum([
        "reliant_credits",
        "openai",
        "anthropic",
        "openrouter",
        "copilot",
        "antigravity",
        "other",
        "not_configured",
      ])
      .optional(),
    workflowParams: z.record(z.string(), z.unknown()).optional(),
    // Idempotency key for the onboarding commit point. See LaunchPlan.commitKey
    // — it is in the URL so a reload mid-provision resumes the same commit.
    // (`daemonProvisioning` was removed with the speculative provisioning it
    // recorded.)
    commitKey: z.string().optional(),
    // The checkout step's selections, and the server-confirmed results. All
    // four are in the URL so a reload mid-payment resumes the same purchase
    // rather than restarting it.
    //
    // Settlement is recorded PER LEG (see LaunchPlan.computeSettled): a single
    // `paid` verdict recorded that the bills were settled without recording
    // what was bought, so credit bought on a local plan silently satisfied a
    // cloud subscription the user then chose via Back.
    computePlanId: z.string().optional(),
    aiCreditCents: z.number().optional(),
    computeSettled: z.boolean().optional(),
    creditSettled: z.boolean().optional(),
    // The compute step resolved itself (the user already had a daemon), so it
    // is hidden from the progress bar and Back is suppressed on the next step.
    // See LaunchPlan.computeAutoSkipped.
    //
    // This schema is .strict(): the plan round-trips through the URL, so ANY
    // field added to LaunchPlan must be declared here too or the router throws
    // `unrecognized_keys` and the onboarding route fails to match.
    computeAutoSkipped: z.boolean().optional(),
  })
  .strict();

/* ---------------------------------------------------------------------------
 * Drift guard: launchPlanSchema vs LaunchPlan
 *
 * The plan round-trips through the URL and the schema above is `.strict()`, so
 * a field present on the LaunchPlan interface but absent here does not fail to
 * compile and does not fail any test — it throws `unrecognized_keys` at
 * runtime the first time it is written to the URL, taking down /onboarding.
 * That is exactly how `computeAutoSkipped` shipped.
 *
 * Everything below is types only: it erases completely and costs nothing at
 * runtime, but `tsc -b` (CI runs `npm run typecheck`) fails on drift. It lives
 * in this source file rather than a test because tsconfig.app.json excludes
 * `*.test.*`, so a guard written in a test file would never be type-checked.
 * ------------------------------------------------------------------------ */

type LaunchPlanSearch = z.infer<typeof launchPlanSchema>;

/**
 * Fails to compile unless `Offenders` is `never`, and names the offending keys
 * in the error text ("Type '\"foo\"' does not satisfy the constraint 'never'").
 */
type AssertNoDrift<_Offenders extends never> = void;

// Key-set equality, checked in both directions.
//
// This is the check that catches the real bug, and plain assignability is NOT
// a substitute for it: TypeScript is structural, so a `Partial<LaunchPlan>`
// carrying an extra field is still assignable to the schema's inferred type.
// An assignability-only guard compiles clean against precisely the drift that
// broke the route. Verified by experiment before writing this.
export type _NoKeyMissingFromSchema = AssertNoDrift<
  Exclude<keyof LaunchPlan, keyof LaunchPlanSearch>
>;
export type _NoKeyMissingFromLaunchPlan = AssertNoDrift<
  Exclude<keyof LaunchPlanSearch, keyof LaunchPlan>
>;

// Value-type agreement on the shared keys. Key-set equality alone would let
// `foo: string` on one side and `foo: boolean` on the other pass, so each
// direction is also checked for assignability. The schema is all-optional by
// design (the plan is filled in step by step), so the comparison is against
// `Partial<LaunchPlan>`.
//
// Asserted against `true` rather than a `void`/`never` pair on purpose:
// `never` is assignable to everything, so a constraint like `T extends void`
// is satisfied by `never` and the check silently passes no matter what.
type AssertTrue<_T extends true> = void;
export type _SchemaMatchesLaunchPlan = AssertTrue<
  LaunchPlanSearch extends Partial<LaunchPlan> ? true : false
>;
export type _LaunchPlanMatchesSchema = AssertTrue<
  Partial<LaunchPlan> extends LaunchPlanSearch ? true : false
>;

export type IndexSearch = z.infer<typeof indexSearchSchema>;
export const indexSearchSchema = z.object({
  plan: launchPlanSchema.optional(),
  "reset-onboarding": z.boolean().optional(),
  // The backend sets `?github_connected=true` (literal "true") on success.
  // tanstack-router's default parseSearch runs JSON.parse on each value, so
  // it arrives here as a boolean — not a string. Same for any URL where the
  // value happens to be JSON-parseable.
  github_connected: z.boolean().optional(),
  // Set after a GitHub App install/setup return. Distinct from
  // github_connected because no credential was created — the user changed
  // which repositories an existing installation can reach.
  github_installed: z.boolean().optional(),
  github_error: z.string().optional(),
  github_error_msg: z.string().optional(),
  // Dev-only toggles. devForceShow is "true" → boolean true after parseSearch;
  // onboarding-credits is "eligible"/"ineligible" which JSON.parse can't parse,
  // so it stays a string.
  devForceShow: z.boolean().optional(),
  "onboarding-credits": z.string().optional(),
  tour: tourParam,
  // `?start=app` opens the new chat with the app-building composer
  // (components/Apps/appStart.ts): onboarding's "Build an app" and every
  // other "build an app" entry land here. NewChatView applies it once and
  // drops it.
  start: z.enum(["app"]).optional(),
});

export const authSearchSchema = z.object({
  redirect: z.string().optional(),
});

// Search params for `/apps`. `claimed` names the app (the forge project) a
// finished claim just made the user's own, so the page can point at it. It is
// a highlight, not a filter: every app is still listed, and the param is
// dropped once the page has shown it.
export const appsSearchSchema = z.object({
  claimed: z.string().optional(),
  // The claim put the person over the one free static site: the page says what
  // happens to the site and what to do about it.
  overAllowance: z.boolean().optional(),
});

// Search params for `/upgrade`. `returnTo` is the URL the user should be sent
// back to after they link a real identity (with an email) onto their existing
// anonymous account — e.g. the admin billing page. Validated at the call site
// (same-origin relative path only) before any redirect; see UpgradeAccount.tsx.
export const upgradeSearchSchema = z.object({
  returnTo: z.string().optional(),
});

// Search params for `/auth/callback`. Two different arrivals land here:
//
//   - OAuth (and any PKCE email link): `code`, exchanged for a session.
//   - A non-PKCE email confirmation link: `token_hash` + `type`, verified
//     directly. Which of the two Supabase sends is decided by the email
//     template in the hosted dashboard, which this repo cannot see or version,
//     so both shapes have to be accepted.
//
// `type` mirrors supabase-js's EmailOtpType. An unknown value is dropped by the
// enum rather than forwarded to verifyOtp.
export const oauthCallbackSearchSchema = z.object({
  code: z.string().optional(),
  token_hash: z.string().optional(),
  type: z
    .enum(["signup", "invite", "magiclink", "recovery", "email_change", "email"])
    .optional(),
  state: z.string().optional(),
  error: z.string().optional(),
  error_description: z.string().optional(),
  source: z.enum(["signin", "link"]).optional(),
  returnTo: z.string().optional(),
});

// Search params for `/auth/github/callback` — the app-owned GitHub OAuth
// connect callback. GitHub redirects here with `code` + `state` on success, or
// `error` + `error_description` on denial. The route exchanges the code via the
// ExchangeGithubOAuthCode RPC (state carries identity), then navigates to the
// decoded returnTo. Owning this route in the SPA (rather than proxying to the
// control-plane GET handler) is what makes the flow work on Firebase, whose
// SPA-rewrites can't proxy to the GKE backend.
// GitHub returns to this one URL from two different flows, and they do not
// carry the same parameters:
//
//   OAuth (web application flow):  ?code=…&state=…
//   App INSTALL (setup redirect):  ?installation_id=…&setup_action=install
//                                  [&code=… when "Request user authorization
//                                  during installation" is enabled]
//
// The install return has NO state, because we never issued one — GitHub
// initiated that redirect. Requiring state unconditionally is what made a
// successful install land on "Invalid GitHub callback".
export const githubOAuthCallbackSearchSchema = z.object({
  code: z.string().optional(),
  state: z.string().optional(),
  error: z.string().optional(),
  error_description: z.string().optional(),
  /** Present only on an App install/setup return. GitHub documents this as
   *  spoofable, so it is treated as a hint to refresh, never as authority. */
  installation_id: z.coerce.string().optional(),
  /** "install" or "update" on a setup redirect; absent for plain OAuth. */
  setup_action: z.string().optional(),
});

/** What the API relays a connection OAuth flow back to the web app with. */
export const connectionOAuthCallbackSearchSchema = z.object({
  code: z.string().optional(),
  state: z.string().optional(),
  /** An error class ("denied", "invalid"), never the provider's own text. */
  error: z.string().optional(),
  redirect_after: z.string().optional(),
});

export const proxyAuthSearchSchema = z.object({
  return: z.string().optional(),
});

// Workflow builder search params — `drill` is a one-shot signal used by the
// onboarding tour to land the user inside a named loop/workflow node after the
// builder loads.
export const workflowSearchSchema = z.object({
  // The project the workflow belongs to, so a refresh or a pasted link
  // resolves it (useRouteProjectResolution) — the builder's routes never
  // mount ModernApp, which is what restores a project anywhere else.
  project: z.string().optional(),
  drill: z.string().optional(),
  tour: tourParam,
  // The chat shown in the editor's chat panel. UI state only: nothing on the
  // server binds a chat to a workflow.
  chat: z.string().optional(),
});

// Settings section identifiers — the source of truth for what `/settings/$section`
// accepts. Kept here (not in SettingsNavigation.tsx) because routes.tsx validates
// the route param against this list and would otherwise have to pull in the
// SettingsNavigation icon graph.
export const SETTINGS_SECTION_IDS = [
  "account",
  // AI providers, models, and local & custom models. Was `general`, a name
  // that said nothing; /settings/general redirects here.
  "ai",
  "shortcuts",
  "prompts",
  // Git worktrees for branched chats. Was `workspaces`, which collided with
  // the product's other "workspace" (a cloud workspace); /settings/workspaces
  // redirects here.
  "branches",
  "projects",
  "browser",
  "appearance",
  "notifications",
  "privacy",
  "mcp",
  "about",
  "tokens",
  // (No "connectors": grants for outside AI apps live on each machine, in
  // Settings → Machines. /settings/connectors redirects there — see
  // settingsConnectorRoutes.tsx.)
  // Every preset across workflows (WORKFLOW_UI.md §14.1 decision 7). A
  // workflow's own presets live on its detail page in the Workflows area.
  "presets",
  "git-connections",
  "developer",
  // Cloud settings sections — in-app control-plane (controlplane.v1) surfaces.
  // Billing also holds Reliant AI's managed keys and spend (`?tab=ai`).
  "billing",
  // Per-member org permissions. Cloud-only: a self-hosted deployment has no
  // organization to grant within, so SettingsNavigation gates the entry on
  // hasControlPlane. Route: /settings/member-permissions.
  "member-permissions",
  // Machines. Shown in every build: registered machines, and the outside AI
  // apps granted access to each, exist without a control plane too. Was
  // `environments`; /settings/environments redirects here.
  "machines",
] as const;
export type SettingsSection = (typeof SETTINGS_SECTION_IDS)[number];
export const DEFAULT_SETTINGS_SECTION: SettingsSection = "account";

export function isSettingsSection(value: string | undefined): value is SettingsSection {
  return !!value && (SETTINGS_SECTION_IDS as readonly string[]).includes(value);
}

/**
 * Slugs that are not section ids but that a link may still carry, or a person
 * would reasonably type, mapped to the section they mean. /settings/$section
 * redirects them (settingsSectionRoutes.tsx). Keys are lower case.
 */
export const SETTINGS_SECTION_ALIASES: Readonly<Record<string, SettingsSection>> = {
  // The ids sections had before they were named after their labels.
  general: "ai",
  environments: "machines",
  workspaces: "branches",
  // Singulars and near misses.
  machine: "machines",
  worktrees: "branches",
  providers: "ai",
};

// Billing sub-navigation. This was `useState` inside BillingSection, with a
// comment arguing the tab "never touches the router" so it composes under the
// settings shell. That was right while nothing needed to link INTO a tab; it is
// now the thing that drops the user's intent. A user who clicked "Set up
// billing" wanted plans, and after a Stripe or OAuth round-trip unaddressable
// state cannot carry that across.
/**
 * Every tab id the URL will ACCEPT. Wider than what the strip renders:
 * `invoices` is a legacy inbound value kept because external links carry
 * `?tab=invoices`, and dropping it from the enum would make the router strip
 * the param and land those users on Overview with no sign anything was lost.
 */
export const BILLING_TAB_IDS = [
  "overview",
  "plans",
  "invoices",
  "usage",
  "ai",
  "limits",
] as const;
export type BillingTab = (typeof BILLING_TAB_IDS)[number];

/**
 * The tabs the strip actually renders. An invoice is the settled record of a
 * period's usage — the same question, "what did I spend and when" — so the two
 * were one tab pretending to be two, and reconciling a number meant checking
 * both.
 */
export const VISIBLE_BILLING_TABS = ["overview", "plans", "usage", "ai"] as const;
export type VisibleBillingTab = (typeof VISIBLE_BILLING_TABS)[number];

/** Where an inbound `?tab=` lands now that invoices has been folded in. */
export function resolveBillingTab(tab?: BillingTab): VisibleBillingTab {
  return tab === "invoices" || tab === "limits" ? "usage" : (tab ?? "overview");
}

/**
 * Search params for `/settings` and `/settings/$section`.
 *
 * `checkout` is what Stripe hands back. Both `successUrl` and `cancelUrl` used
 * to be `window.location.href`, so a completed purchase and an abandoned one
 * returned to the identical URL and the app could not tell them apart. It is a
 * PRESENTATION signal only — a user can type it, and entitlement stays
 * webhook-driven — which is why the success state it drives confirms the
 * subscription against the server rather than asserting it.
 */
export const settingsSearchSchema = z.object({
  tab: z.enum(BILLING_TAB_IDS).optional(),
  checkout: z.enum(["success", "cancelled"]).optional(),
  // Which plan the user was buying, so the return state can check the right one
  // against the server. Named `planId` and NOT `plan` on purpose: `/` and
  // `/onboarding` already carry a `plan` that is a LaunchPlan OBJECT, and
  // tanstack-router types a `to: "."` search updater against the union of every
  // route's params — a string `plan` here makes those updaters fail to compile
  // across the app.
  planId: z.string().optional(),
  // Where the user came from. `onboarding`: billing offers a route back —
  // previously `returnTo` hard-coded /settings/billing and a user who detoured
  // mid-wizard had no way home. `forge`: an environment whose deploy is queued
  // on billing; the way back is its page. `connectors`: the retired
  // /settings/connectors redirect, so Machines can say where Connectors went.
  // Every other value is a surface that sent the user to billing
  // (hooks/useGoToBilling's BillingOrigin); billing offers the way back.
  from: z
    .enum(["onboarding", "forge", "connectors", "machines", "chat", "runs", "inbox", "reliant-ai", "credit", "upgrade"])
    .optional(),
  // The exact URL to return to, captured at the moment the user left. Carries
  // onboarding's `plan` search param — the wizard's ENTIRE state — so the trip
  // through billing (and Stripe, which is a full cold boot) is resumable.
  // Without it the return navigates to a bare /onboarding, deriveStep sees an
  // empty plan, and the user restarts from step one having already answered.
  // Validated at the point of use, not here: it is a URL from the address bar,
  // so it must be same-origin-checked before being navigated to.
  returnTo: z.string().optional(),
  // The pre-path deep link into one machine. /settings/machines redirects
  // it to /settings/machines/$machineId (settingsSectionRoutes.tsx);
  // declared so validation does not strip it before that redirect reads it.
  daemon: z.string().optional(),
  // The slug of a /settings/<slug> URL that matched no section. Set only by
  // that redirect; the page explains it and offers to dismiss.
  notFound: z.string().optional(),
});

// Search params for `/onboarding`. A strict subset of `indexSearchSchema` —
// these are the fields actually load-bearing for the onboarding flow. Keeping
// them on a dedicated schema means /onboarding can't accidentally inherit
// stray /-route params, and the schema documents what onboarding cares about.
// `/m/new` accepts an optional `worktreeId` so the chat-list group header's
// "new chat in this workspace" action can target a non-main workspace —
// without it, every mobile chat could only ever be created against main.
export const mobileNewChatSearchSchema = z.object({
  worktreeId: z.string().optional(),
});

/**
 * Search params for the ENTRIES into the forge console — the bare `/forge`
 * and the retired URLs (Console/ForgeResolve.tsx). The console itself is
 * addressed by the forge project in its path (Console/paths.ts); these name
 * a project the old ways, and only redirect:
 *
 * `project` is a Reliant project id — what every forge URL carried before
 * the console had a path of its own, so bookmarks and old links still carry
 * it.
 *
 * `forgeProject` is a FORGE project name (forge.yaml `name`) — the only name
 * the control plane knows, so it is what a link the control plane writes
 * carries: a queued deploy's action URL is
 * `/forge/env/<env>?forgeProject=<name>`. It is the console's own key, so it
 * redirects without a lookup, and opens even when no Reliant project here
 * declares it (an org admin a teammate sent the link to).
 */
const forgeProjectParam = z.string().optional();

export const forgeResolveSearchSchema = z.object({
  project: z.string().optional(),
  forgeProject: forgeProjectParam,
});

/**
 * The console's environment scope (Console/envScope.ts): an
 * environment name or `all`, on every console page. Absent, the console
 * resolves the remembered scope (default prod) and writes it back.
 */
const envScopeParam = z.string().optional().catch(undefined);

/** Every console page without page state of its own. */
export const forgeConsoleSearchSchema = z.object({
  env: envScopeParam,
});

/**
 * Free text the router may have parsed as JSON: a search for "981" or a
 * version "1.2" arrives as a number, and must stay the text it was.
 */
const textParam = z.union([z.string(), z.number().transform(String)]).optional().catch(undefined);

/**
 * Releases: the scope, and the list's filters — text, author, status, and
 * whether to show the releases that never ran in the scoped environment.
 */
export const forgeConsoleReleasesSearchSchema = z.object({
  env: envScopeParam,
  q: textParam,
  author: textParam,
  status: z.enum(STATUS_FILTERS).optional().catch(undefined),
  every: z.boolean().optional().catch(undefined),
  /** The deploy whose output is open in the Deploys section (the Overview's banner links here). */
  deploy: textParam,
});

/**
 * Activity: the scope, and the target an object's History link pre-filters to
 * (`release:<version>`) — kept in the URL so the link means
 * the same thing once the Activity page reads it.
 */
export const forgeConsoleActivitySearchSchema = z.object({
  env: envScopeParam,
  target: textParam,
});

/**
 * One release: the scope, the tab, the release Changes compares with, and the
 * environment the environment-bound tabs read (`in`, a name).
 */
export const forgeConsoleReleaseSearchSchema = z.object({
  env: envScopeParam,
  tab: z.enum(RELEASE_TABS).optional().catch(undefined),
  base: textParam,
  in: textParam,
});

/**
 * Secrets: the scope, and the secret open in detail. A NAME, never a value —
 * the surface cannot hold a value, so nothing here could leak into a URL, a
 * history entry or a referrer. Linked because a version history is a thing
 * people send each other ("look at what happened to DATABASE_URL").
 */
export const forgeConsoleSecretsSearchSchema = z.object({
  env: envScopeParam,
  secret: z.string().optional().catch(undefined),
});

// ── Observability URL state ────────────────────────────────────────────────

/**
 * The Observability area's URL state (components/Forge/Observability/obsSearch.ts),
 * shared by the env page's tab and the project-level area so one link shape
 * opens either. In the URL because "look at these errors in prod" is a link:
 * which view, which window, the query, the trace someone opened, and every
 * ad-hoc choice — filters and group-by — so a narrowed view can be sent to a
 * colleague as it is.
 *
 * `range` is a PRESET, never timestamps, so a link to the last hour means the
 * last hour when it is opened. `severity` is a comma list (`warn,error`). `q`
 * is the Logs query string, or in Traces the root operation to match.
 * `trace`/`span` name the trace in focus. `f` is attribute filter tokens
 * (`service.version:v1.8`), `groupBy` up to three keys.
 *
 * Every key drops itself (`.catch`) rather than failing the route: a link
 * from a newer build, or a hand-edited one, must open a slightly wider view,
 * not an error page.
 */
export const obsSearchShape = {
  view: z.enum(OBS_VIEWS).optional().catch(undefined),
  range: z.enum(RANGE_PRESETS).optional().catch(undefined),
  service: z.string().optional().catch(undefined),
  rpcService: z.string().max(512).optional().catch(undefined),
  rpcId: z.string().max(1024).optional().catch(undefined),
  declared: z.boolean().optional().catch(undefined),
  internal: z.boolean().optional().catch(undefined),
  severity: z.string().optional().catch(undefined),
  q: z.string().optional().catch(undefined),
  trace: z.string().optional().catch(undefined),
  span: z.string().optional().catch(undefined),
  profileType: z.string().optional().catch(undefined),
  live: z.boolean().optional().catch(undefined),
  errors: z.boolean().optional().catch(undefined),
  minDuration: z.string().optional().catch(undefined),
  envs: z.array(z.string()).optional().catch(undefined),
  apps: z.array(z.string()).optional().catch(undefined),
  f: z.array(z.string()).optional().catch(undefined),
  groupBy: z.array(z.string()).optional().catch(undefined),
  alerts: z.enum(ALERTS_TABS).optional().catch(undefined),
  rule: z.string().optional().catch(undefined),
  compare: z.string().optional().catch(undefined),
  cmpWindow: z.enum(HEALTH_WINDOWS).optional().catch(undefined),
  issue: z.string().optional().catch(undefined),
  istatus: z.string().optional().catch(undefined),
  platform: z.enum(ISSUE_PLATFORMS).optional().catch(undefined),
  isort: z.enum(ISSUE_SORTS).optional().catch(undefined),
  assignee: z.string().optional().catch(undefined),
  session: z.string().optional().catch(undefined),
  t: z.number().int().nonnegative().optional().catch(undefined),
  replay: z.boolean().optional().catch(undefined),
  user: z.string().optional().catch(undefined),
  cursor: z.array(z.string()).optional().catch(undefined),
};

/** Every ObsSearch key has a schema entry and vice versa (the launchPlan drift guard, for the same reason). */
export type _ObsShapeCoversSearch = AssertNoDrift<Exclude<keyof ObsSearch, keyof typeof obsSearchShape>>;
export type _ObsSearchCoversShape = AssertNoDrift<Exclude<keyof typeof obsSearchShape, keyof ObsSearch>>;
export type _ObsShapeMatchesSearch = AssertTrue<
  z.output<z.ZodObject<typeof obsSearchShape>> extends ObsSearch ? true : false
>;

/**
 * A console page hosting an Observability view (Console/ObservabilityHost):
 * the panel's keys minus the two the console moved out of the query — the
 * view is the page's path, and the environment filter is the scope (`env`) —
 * and the org's app filter, which a one-app console has no use for.
 */
const { view: _view, envs: _envs, apps: _apps, ...obsConsoleShape } = obsSearchShape;
export const forgeConsoleObsSearchSchema = z.object({
  env: envScopeParam,
  ...obsConsoleShape,
});

/**
 * The Overview (project home with the workloads): the scope, the window, and
 * the services table's filters — text, status (needs attention) and kind.
 */
export const forgeConsoleOverviewSearchSchema = z.object({
  env: envScopeParam,
  range: z.enum(RANGE_PRESETS).optional().catch(undefined),
  q: textParam,
  status: z.enum(OVERVIEW_STATUS_FILTERS).optional().catch(undefined),
  kind: z.enum(OVERVIEW_KIND_FILTERS).optional().catch(undefined),
});

/**
 * One workload: its tab, and the Logs / Traces / Profiles tabs' own keys (the
 * explorers' vocabulary, as on the Observability pages). The environment is
 * in the path; `env` is still carried so the way back keeps the scope.
 */
export const forgeConsoleWorkloadSearchSchema = z.object({
  env: envScopeParam,
  tab: z.enum(WORKLOAD_TABS).optional().catch(undefined),
  ...obsConsoleShape,
});

/**
 * Observability › Dashboards: the index's search box and its two filters
 * (source: release or workspace; owner: forge, repo or a person). The scope
 * (`env`) narrows the index server-side; these narrow it in place.
 */
export const forgeConsoleDashboardsSearchSchema = z.object({
  env: envScopeParam,
  q: z.string().optional().catch(undefined),
  source: z.enum(["release", "workspace"]).optional().catch(undefined),
  owner: z.string().optional().catch(undefined),
});

/**
 * One dashboard: everything Share puts in the link — the time window, the
 * service variable, the comparison, the auto-refresh, full screen — plus an
 * older version (workspace) or the release it shipped in (release). The
 * environment is the console scope (`env`).
 */
export const forgeConsoleDashboardSearchSchema = z.object({
  env: envScopeParam,
  range: z.enum(RANGE_PRESETS).optional().catch(undefined),
  service: z.string().optional().catch(undefined),
  compare: z.enum(COMPARE_MODES).optional().catch(undefined),
  refresh: z.enum(REFRESH_INTERVALS).optional().catch(undefined),
  rpcService: z.string().optional().catch(undefined),
  rpcMethod: z.string().optional().catch(undefined),
  rpcProcedure: z.string().optional().catch(undefined),
  variables: z.record(z.string(), z.string()).optional().catch(undefined),
  breakdown: z.string().optional().catch(undefined),
  hiddenSections: z.array(z.string()).optional().catch(undefined),
  hiddenPanels: z.array(z.string()).optional().catch(undefined),
  releases: z.boolean().optional().catch(undefined),
  alerts: z.boolean().optional().catch(undefined),
  tv: z.boolean().optional().catch(undefined),
  version: z.number().int().positive().optional().catch(undefined),
  release: z.string().optional().catch(undefined),
});

/**
 * The retired URLs' view: a current view, the retired `workloads` (the
 * workloads are the Overview's now, with a page per workload) and the
 * workload it had open, or the retired `usage` (the app's Settings › Billing)
 * — read only so the redirect lands on the right page.
 */
const legacyObsShape = {
  ...obsSearchShape,
  view: z.enum([...OBS_VIEWS, "workloads", "usage"]).optional().catch(undefined),
  workload: z.string().optional().catch(undefined),
};

/**
 * The org-wide /forge/observability[/:view]: the panel's keys, where `envs`
 * (`app/env` names) and `apps` are the filters. `project` and `forgeProject`
 * (a link to ONE app, redirected to its console) and a bare `view` /
 * `workload` (the retired URLs) are read only so those redirects land right
 * (ForgeEntryRoutes).
 */
export const forgeOrgObsSearchSchema = z.object({
  project: z.string().optional(),
  forgeProject: forgeProjectParam,
  ...legacyObsShape,
});

/**
 * Which tab of one environment's page is open. In the URL because people link
 * each other to an answer ("look at prod's releases"), and a refresh should
 * not throw you back to the default.
 *
 * ABSENT means the environment's default tab — `overview` for a deployed env,
 * `running` for a local one — so a bare link to an environment opens whatever
 * that environment leads with.
 *
 * `live`, `preview`, `changes`, `checks` and `observability` are retired tab
 * names, still accepted so old links land on the default tab rather than
 * failing to parse.
 */
const envPageTab = z
  .enum([
    "overview",
    "identity",
    "running",
    "releases",
    "activity",
    "secrets",
    "domains",
    "changes",
    "checks",
    "live",
    "preview",
    "observability",
  ])
  .optional();

/**
 * /forge/p/$project/environments/$env — one environment, as a configured
 * object. The environment is a PATH segment: it is the page's subject, so the
 * top-bar scope (`env`, kept so links out carry it) does not apply to it.
 * `secret` selects one secret's detail in the Secrets tab (see
 * forgeConsoleSecretsSearchSchema).
 */
export const forgeConsoleEnvSearchSchema = z.object({
  env: envScopeParam,
  secret: z.string().optional(),
  tab: envPageTab,
});

/** The retired /forge/env/$env: read only to redirect (Console/paths.ts legacyEnvTarget). */
export const forgeLegacyEnvSearchSchema = z.object({
  project: z.string().optional(),
  forgeProject: forgeProjectParam,
  secret: z.string().optional(),
  tab: z.string().optional(),
  ...legacyObsShape,
});

/**
 * The retired per-command routes (/forge/topology, /forge/environments,
 * /forge/status, /forge/secrets). They only redirect now, and keep their old
 * params so a bookmark lands on the same project — and, when it named one,
 * the same environment and secret.
 */
export const forgeLegacySearchSchema = z.object({
  project: z.string().optional(),
  forgeProject: forgeProjectParam,
  env: z.string().optional(),
  secret: z.string().optional(),
});

// ── /workflows ──────────────────────────────────────────────────────────────

/**
 * Every page of the Workflows area carries `project`, for the same reason the
 * forge screens do: the area renders outside ModernApp, which is the only
 * thing that resolves `currentProject` on a page load, so a hard refresh must
 * be able to read the project from the URL (WorkflowsShell resolves it).
 * Absent means "resolve it the normal way", not "no project".
 *
 * `allProjects` is the area's ONE scope: the header's project menu sets it,
 * every tab honours it (the Workflows list, a workflow's Triggers and Recent
 * runs, Runs), and it rides along between tabs with `project`. Absent means
 * the current project — or every project, when there is none.
 *
 * `tour` is here because the onboarding tour's hub step spotlights the
 * Workflows list, and strict search validation would otherwise strip it.
 */
export const workflowsAreaSearchSchema = z.object({
  project: z.string().optional().catch(undefined),
  allProjects: z.boolean().optional().catch(undefined),
  tour: tourParam.catch(undefined),
});
export type WorkflowsAreaSearch = Partial<z.output<typeof workflowsAreaSearchSchema>>;

// ── /workflows/library (the Workflows tab) ──────────────────────────────────

/**
 * The Workflows list's filter chips. Absent means All. "on" is what runs by
 * itself (what the Automations tab used to list); "needs-you" is what the
 * Inbox would raise about it; the rest narrow by where the definition lives.
 */
export const WORKFLOWS_SHOW_KEYS = ["on", "needs-you", "yours", "builtin", "failed"] as const;
export type WorkflowsShowKey = (typeof WORKFLOWS_SHOW_KEYS)[number];

/**
 * The list's sort. Absent means by name: a list that reorders itself when
 * the last-run data arrives would move rows under the pointer.
 */
export const LIBRARY_SORT_KEYS = ["name", "recent"] as const;
export type LibrarySortKey = (typeof LIBRARY_SORT_KEYS)[number];

/**
 * /workflows/library (§2.2). The search, filter and sort live in the URL like
 * the Runs filters, so a narrowed list is a link and Back restores it. An
 * unknown value is dropped rather than failing the route.
 */
export const librarySearchSchema = workflowsAreaSearchSchema.extend({
  q: z.string().optional().catch(undefined),
  show: z.enum(WORKFLOWS_SHOW_KEYS).optional().catch(undefined),
  sort: z.enum(LIBRARY_SORT_KEYS).optional().catch(undefined),
});
/** Every key optional: absent is the default, which is what links omit. */
export type LibrarySearch = Partial<z.output<typeof librarySearchSchema>>;

// ── /workflows/runs ─────────────────────────────────────────────────────────

/**
 * The Runs list's state filter, in the words a user picks (§5.2), not the
 * wire enum. One chip can cover several display states ("Live" is queued,
 * running, paused and waiting for a machine); run-queries maps them.
 */
export const RUN_STATE_FILTER_KEYS = ["needs_you", "live", "failed", "completed", "cancelled"] as const;
export type RunStateFilterKey = (typeof RUN_STATE_FILTER_KEYS)[number];

/**
 * Launch kinds the "Started by" filter offers — every kind a run can carry,
 * attended and unattended (runStatus.ts LaunchKind). The server matches any
 * string. With no kind picked it leaves builder.test out: a test run from the
 * workflow builder is scratch work, listed only under "Tests".
 */
export const RUN_KIND_FILTER_KEYS = [
  "chat.start",
  "manual.run",
  "schedule",
  "webhook",
  "integration",
  "workflow_event",
  "agent.start_run",
  "builder.test",
] as const;

export const RUN_RANGE_KEYS = ["24h", "7d", "30d", "all"] as const;
export type RunRangeKey = (typeof RUN_RANGE_KEYS)[number];

/**
 * A list param that keeps only the values `keep` accepts. A shared link from a
 * newer build, or a hand-edited URL, must narrow the list rather than take the
 * page down, so unknown entries are dropped instead of failing validation.
 *
 * Built from catch + transform rather than preprocess on purpose: preprocess
 * types the INPUT as `unknown`, which makes tanstack-router demand a `search`
 * object on every link to the Runs tab.
 */
function listParam<T extends string>(keep: (value: string) => value is T) {
  return z
    .array(z.string())
    .optional()
    .catch(undefined)
    .transform((values): T[] | undefined => {
      const kept = values?.filter(keep) ?? [];
      return kept.length > 0 ? kept : undefined;
    });
}

const isStateFilter = (value: string): value is RunStateFilterKey =>
  (RUN_STATE_FILTER_KEYS as readonly string[]).includes(value);
const isNonEmpty = (value: string): value is string => value !== "";

/**
 * /workflows/runs. Every filter lives in the URL so a filtered list can be shared and
 * the back button restores it (§5.2). Absent means the default: the current
 * project, every kind and state, the last 24 hours, repeats grouped.
 */
export const runsSearchSchema = z.object({
  state: listParam(isStateFilter),
  kind: listParam(isNonEmpty),
  workflow: listParam(isNonEmpty),
  trigger: z.string().optional().catch(undefined),
  range: z.enum(RUN_RANGE_KEYS).optional().catch(undefined),
  q: z.string().optional().catch(undefined),
  /** The area's scope (workflowsAreaSearchSchema): every project, not the current one. */
  allProjects: z.boolean().optional().catch(undefined),
  /** False turns off grouping of repeated automation runs (on by default). */
  group: z.boolean().optional().catch(undefined),
  /**
   * Only the runs an agent started from this chat (decision 4: they are not in
   * the sidebar; the parent chat's header links here). Spans every project
   * and every time: a chat's children are few, and a child in another project
   * or older than the default window is still that chat's child.
   */
  parent: z.string().min(1).optional().catch(undefined),
  /** The area's project param (workflowsAreaSearchSchema); not a filter. */
  project: z.string().optional().catch(undefined),
});
/** Every key optional: absent is the default, which is what links omit. */
export type RunsSearch = Partial<z.output<typeof runsSearchSchema>>;

export const onboardingSearchSchema = z.object({
  plan: launchPlanSchema.optional(),
  "reset-onboarding": z.boolean().optional(),
  // OAuth return-path params. The control-plane OAuth handler redirects back
  // to whatever returnTo it was given (see ProjectChoiceStep.tsx), so a
  // GitHub OAuth started from /onboarding lands back here with these set.
  github_connected: z.boolean().optional(),
  // An App install/setup return can land here too, when the install was
  // started from onboarding.
  github_installed: z.boolean().optional(),
  github_error: z.string().optional(),
  github_error_msg: z.string().optional(),
  devForceShow: z.boolean().optional(),
  "onboarding-credits": z.string().optional(),
});