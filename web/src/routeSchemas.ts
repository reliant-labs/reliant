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
    daemonProvisioning: z.boolean().optional(),
  })
  .strict();

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