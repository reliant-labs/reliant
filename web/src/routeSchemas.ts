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

export const oauthCallbackSearchSchema = z.object({
  code: z.string().optional(),
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
export const githubOAuthCallbackSearchSchema = z.object({
  code: z.string().optional(),
  state: z.string().optional(),
  error: z.string().optional(),
  error_description: z.string().optional(),
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
  // Grants for third-party MCP clients (ChatGPT, Claude, mobile) that drive a
  // cloud workspace. Route: /settings/connectors.
  "connectors",
  "git-connections",
  "developer",
  // Cloud settings sections — in-app control-plane (controlplane.v1) surfaces
  // that replaced the external "Manage cloud account" portal link. Routes:
  // /settings/billing, /settings/environments. (Managed Reliant AI keys/spend
  // now live as a tab inside the /settings/general "AI" section.)
  "billing",
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

export const onboardingSearchSchema = z.object({
  plan: launchPlanSchema.optional(),
  "reset-onboarding": z.boolean().optional(),
  // OAuth return-path params. The control-plane OAuth handler redirects back
  // to whatever returnTo it was given (see ProjectChoiceStep.tsx), so a
  // GitHub OAuth started from /onboarding lands back here with these set.
  github_connected: z.boolean().optional(),
  github_error: z.string().optional(),
  github_error_msg: z.string().optional(),
  devForceShow: z.boolean().optional(),
  "onboarding-credits": z.string().optional(),
});