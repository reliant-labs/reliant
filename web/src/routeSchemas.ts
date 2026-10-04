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

// The guided tour's "active step" lives in the URL as `?tour=<step-id>` —
// absence means the wizard is not active. Adding this to every search schema
// the wizard may render on keeps tanstack-router's strict validation from
// stripping the param when the user navigates between hub / builder / chat
// while in the middle of a step.
const tourParam = z.enum(ONBOARDING_STEP_IDS).optional();

export const launchPlanSchema = z
  .object({
    // Keep in sync with OnboardingIntent in
    // components/OnboardingFlow/types.ts — only the two values the rendered
    // wizard can set are accepted here.
    intent: z.enum(["build_app", "existing_codebase"]).optional(),
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
});

export const authSearchSchema = z.object({
  redirect: z.string().optional(),
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

export const proxyAuthSearchSchema = z.object({
  return: z.string().optional(),
});

// Workflow builder search params — `drill` is a one-shot signal used by the
// onboarding tour to land the user inside a named loop/workflow node after the
// builder loads.
export const workflowSearchSchema = z.object({
  drill: z.string().optional(),
  tour: tourParam,
});

// Settings section identifiers — the source of truth for what `/settings/$section`
// accepts. Kept here (not in SettingsNavigation.tsx) because routes.tsx validates
// the route param against this list and would otherwise have to pull in the
// SettingsNavigation icon graph.
export const SETTINGS_SECTION_IDS = [
  "account",
  "general",
  "shortcuts",
  "prompts",
  "workspaces",
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
  // Cloud settings sections — in-app control-plane (controlplane.v1) surfaces
  // that replaced the external "Manage cloud account" portal link. Routes:
  // /settings/billing, /settings/environments. (Managed Reliant AI keys/spend
  // now live as a tab inside the /settings/general "AI" section.)
  "billing",
  // Per-member org permissions. Cloud-only: a self-hosted deployment has no
  // organization to grant within, so SettingsNavigation gates the entry on
  // hasControlPlane. Route: /settings/member-permissions.
  "member-permissions",
  // Machines. Shown in every build: registered machines, and the outside AI
  // apps granted access to each, exist without a control plane too.
  "environments",
] as const;
export type SettingsSection = (typeof SETTINGS_SECTION_IDS)[number];
export const DEFAULT_SETTINGS_SECTION: SettingsSection = "account";

export const settingsParamsSchema = z.object({
  section: z.enum(SETTINGS_SECTION_IDS),
});

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
] as const;
export type BillingTab = (typeof BILLING_TAB_IDS)[number];

/**
 * The tabs the strip actually renders. An invoice is the settled record of a
 * period's usage — the same question, "what did I spend and when" — so the two
 * were one tab pretending to be two, and reconciling a number meant checking
 * both.
 */
export const VISIBLE_BILLING_TABS = ["overview", "plans", "usage"] as const;
export type VisibleBillingTab = (typeof VISIBLE_BILLING_TABS)[number];

/** Where an inbound `?tab=` lands now that invoices has been folded in. */
export function resolveBillingTab(tab?: BillingTab): VisibleBillingTab {
  return tab === "invoices" ? "usage" : (tab ?? "overview");
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
  // Where the user came from, so billing can offer a route back. Onboarding
  // previously had none: `returnTo` hard-coded /settings/billing and a user who
  // detoured mid-wizard had no way home.
  from: z.enum(["onboarding"]).optional(),
  // The exact URL to return to, captured at the moment the user left. Carries
  // onboarding's `plan` search param — the wizard's ENTIRE state — so the trip
  // through billing (and Stripe, which is a full cold boot) is resumable.
  // Without it the return navigates to a bare /onboarding, deriveStep sees an
  // empty plan, and the user restarts from step one having already answered.
  // Validated at the point of use, not here: it is a URL from the address bar,
  // so it must be same-origin-checked before being navigated to.
  returnTo: z.string().optional(),
  // Environments deep-links to a specific daemon's detail view; the section
  // reads it via useSearch({ strict: false }). Declared here because the route
  // now validates its search and would otherwise strip it.
  daemon: z.string().optional(),
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
 * Search params for the forge screens.
 *
 * `project` is a project id, and it is what makes a forge URL survive a refresh.
 * The forge routes do not mount ModernApp, which is the only thing that resolves
 * `projectStore.currentProject` on a page load, so before this param existed a
 * reload of /forge/topology had no way to learn which project it was about and
 * dead-ended. ForgeLayout reflects the resolved project into this param and
 * reads it back on the next load. It is a param rather than a path segment to
 * match the existing three forge paths and to keep it optional — absent means
 * "resolve it the normal way", not "no project".
 *
 * `env` is an environment name, on the two routes that have a per-environment
 * view. It moved out of component state so that a topology row can link into a
 * specific environment's status, and so a refresh keeps the selection.
 */
export const forgeOverviewSearchSchema = z.object({
  project: z.string().optional(),
});

/**
 * /forge/domains. Carries `project` for the same reason every other forge
 * route does — a refresh must resolve the project without ModernApp.
 *
 * It carries NOTHING ELSE, and in particular no domain selection. The domain
 * list is ORG-scoped, so a link to one domain is not a link to a place in
 * this project; the expanded row is component state deliberately, because a
 * shared URL naming a domain id would open a blank row for a teammate in a
 * different organization.
 */
export const forgeDomainsSearchSchema = z.object({
  project: z.string().optional(),
});

/**
 * /forge/env/$env. The environment is a PATH segment — it is the page's
 * subject, not a filter on it.
 */
export const forgeEnvPageSearchSchema = z.object({
  project: z.string().optional(),
  /**
   * `secret` selects one secret's detail view in the Secrets section, for the
   * same reason the env is in the URL rather than state: a version history is
   * a thing people link each other to ("look at what happened to
   * DATABASE_URL"), and a refresh while reading one should not throw you back
   * to the list.
   *
   * It is a NAME, never a value — the whole surface is incapable of holding a
   * value, so there is nothing here that could leak into a URL, a browser
   * history entry, or a referrer header.
   */
  secret: z.string().optional(),
  /**
   * Which of the two tabs is open — `live` (the default) or `preview`.
   *
   * In the URL for the same reason `secret` is: the two answer different
   * questions and people link each other to the answer ("look at what Preview
   * says about staging"). It is also what lets Live's "Open Preview" remedy be
   * a real navigation rather than hidden component state, so a refresh while
   * reading Preview does not throw you back to Live.
   *
   * `live` is encoded as ABSENT rather than as the string, so the default view
   * has the plain URL and a bare link to an environment means "its Live
   * state" — which is the view that needs no daemon.
   */
  tab: z.enum(["live", "preview"]).optional(),
});

/**
 * The retired per-command routes (/forge/topology, /forge/environments,
 * /forge/status, /forge/secrets). They only redirect now, and keep their old
 * params so a bookmark lands on the same project — and, when it named one,
 * the same environment and secret.
 */
export const forgeLegacySearchSchema = z.object({
  project: z.string().optional(),
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
 * `tour` is here because the onboarding tour's hub step spotlights the
 * Library, and strict search validation would otherwise strip it.
 */
export const workflowsAreaSearchSchema = z.object({
  project: z.string().optional().catch(undefined),
  tour: tourParam.catch(undefined),
});
export type WorkflowsAreaSearch = Partial<z.output<typeof workflowsAreaSearchSchema>>;

// ── /workflows/library ──────────────────────────────────────────────────────

/** The Library's source filter. Absent means every source. */
export const LIBRARY_SOURCE_KEYS = ["yours", "builtin", "failed"] as const;
export type LibrarySourceKey = (typeof LIBRARY_SOURCE_KEYS)[number];

/**
 * The Library's sort. Absent means by name: a list that reorders itself when
 * the last-run data arrives would move rows under the pointer.
 */
export const LIBRARY_SORT_KEYS = ["name", "recent"] as const;
export type LibrarySortKey = (typeof LIBRARY_SORT_KEYS)[number];

/**
 * /workflows/library (§2.2). The search, source and sort live in the URL like
 * the Runs filters, so a narrowed library is a link and Back restores it. An
 * unknown value is dropped rather than failing the route.
 */
export const librarySearchSchema = workflowsAreaSearchSchema.extend({
  q: z.string().optional().catch(undefined),
  source: z.enum(LIBRARY_SOURCE_KEYS).optional().catch(undefined),
  sort: z.enum(LIBRARY_SORT_KEYS).optional().catch(undefined),
});
/** Every key optional: absent is the default, which is what links omit. */
export type LibrarySearch = Partial<z.output<typeof librarySearchSchema>>;

// ── /workflows/runs ─────────────────────────────────────────────────────────

/**
 * The Runs list's state filter, in the words a user picks (§5.2), not the
 * wire enum. One chip can cover several display states ("Live" is queued,
 * running, paused and waiting for a machine); run-grpc maps them.
 */
export const RUN_STATE_FILTER_KEYS = ["needs_you", "live", "failed", "completed", "cancelled"] as const;
export type RunStateFilterKey = (typeof RUN_STATE_FILTER_KEYS)[number];

/**
 * Launch kinds the kind chips offer. The server matches any string. With no
 * kind chip pressed it leaves builder.test out: a test run from the workflow
 * builder is scratch work, listed only under the "Tests" chip.
 */
export const RUN_KIND_FILTER_KEYS = ["chat.start", "schedule", "agent.start_run", "builder.test"] as const;

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
  /** Widen past the current project (decision 10: Runs defaults to it). */
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