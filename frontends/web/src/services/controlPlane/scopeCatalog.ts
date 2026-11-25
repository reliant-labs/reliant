// Copyright (c) 2025 Reliant Labs

/**
 * forge's scope catalog (forge pkg/accesstoken/catalog.go), as the API-tokens
 * page needs it: every scope's class, and the bundles that grant it.
 *
 * WHY A COPY. The web app cannot import forge's Go catalog, and no rpc lists
 * it. The page's scope list is checked against FORGE_SCOPES (every scope the
 * server may accept), and a bundle is previewed BEFORE the token exists from
 * the bundle column. Once a token is minted the page shows the server's
 * resolved set (AccessToken.scopes), never this.
 *
 * THE COPY CANNOT DRIFT. internal/accesstokenclient/web_scope_catalog_test.go
 * reads the rows between the markers below and fails unless they are forge's
 * catalog at the forge this repo pins: AllScopes order, ClassOf, and BundlesOf
 * (which names no bundle for a sensitive scope). Keep one row per line; that
 * test prints the exact rows to paste when forge moves.
 */

export type ForgeScopeClass = "personal" | "member" | "standard" | "sensitive";

/** forge accesstoken.AllBundles, least authority first. */
export const FORGE_BUNDLES = ["ci", "developer", "admin"] as const;
export type ForgeBundle = (typeof FORGE_BUNDLES)[number];

export interface ForgeScopeCatalogEntry {
  scope: string;
  class: ForgeScopeClass;
  /** The bundles that grant this scope (forge BundlesOf). Always empty for a sensitive scope. */
  bundles: readonly ForgeBundle[];
}

// forge-scope-catalog:begin
// prettier-ignore
export const FORGE_SCOPE_CATALOG: readonly ForgeScopeCatalogEntry[] = [
  { scope: "deploy:read", class: "standard", bundles: ["ci", "developer", "admin"] },
  { scope: "deploy:write", class: "standard", bundles: ["ci", "developer", "admin"] },
  { scope: "deploy:config:read", class: "sensitive", bundles: [] },
  { scope: "billing:read", class: "standard", bundles: ["admin"] },
  { scope: "cluster:manage", class: "standard", bundles: ["admin"] },
  { scope: "token:read", class: "sensitive", bundles: [] },
  { scope: "token:write", class: "sensitive", bundles: [] },
  { scope: "reliant:api", class: "personal", bundles: ["developer"] },
  { scope: "daemon:connect", class: "personal", bundles: [] },
  { scope: "llm:invoke", class: "personal", bundles: ["developer"] },
  { scope: "proxy:port", class: "personal", bundles: [] },
  { scope: "mcp:connector", class: "personal", bundles: [] },
  { scope: "mcp:oauth", class: "personal", bundles: [] },
  { scope: "daemon:resume", class: "personal", bundles: [] },
  { scope: "daemon:manage", class: "personal", bundles: [] },
  { scope: "secret:read", class: "standard", bundles: ["admin"] },
  { scope: "secret:write", class: "sensitive", bundles: [] },
  { scope: "domain:read", class: "standard", bundles: ["developer", "admin"] },
  { scope: "domain:write", class: "standard", bundles: ["admin"] },
  { scope: "identity:read", class: "standard", bundles: ["admin"] },
  { scope: "identity:manage", class: "sensitive", bundles: [] },
  { scope: "obs:read", class: "member", bundles: ["developer", "admin"] },
  { scope: "obs:triage", class: "member", bundles: ["developer", "admin"] },
  { scope: "obs:write", class: "standard", bundles: ["developer", "admin"] },
  { scope: "db:query", class: "sensitive", bundles: [] },
  { scope: "storage:read", class: "sensitive", bundles: [] },
  { scope: "runs:start", class: "personal", bundles: ["developer"] },
  { scope: "runs:read", class: "personal", bundles: ["developer"] },
  { scope: "runs:control", class: "personal", bundles: ["developer"] },
  { scope: "triggers:fire", class: "personal", bundles: ["developer"] },
  { scope: "triggers:manage", class: "personal", bundles: ["developer"] },
  { scope: "inbox:read", class: "personal", bundles: ["developer"] },
  { scope: "inbox:manage", class: "personal", bundles: ["developer"] },
  { scope: "approvals:decide", class: "personal", bundles: [] },
  { scope: "questions:answer", class: "personal", bundles: ["developer"] },
  { scope: "sourcemaps:write", class: "standard", bundles: [] },
  { scope: "changes:write", class: "standard", bundles: ["developer", "admin"] },
  { scope: "changes:launch", class: "sensitive", bundles: [] },
  { scope: "vuln:read", class: "standard", bundles: ["ci", "developer", "admin"] },
  { scope: "tests:read", class: "standard", bundles: ["developer", "admin"] },
  { scope: "tests:write", class: "standard", bundles: ["ci", "developer", "admin"] },
  { scope: "mcp:forge", class: "personal", bundles: [] },
];
// forge-scope-catalog:end

/** Every scope forge knows, in its AllScopes order. */
export const FORGE_SCOPES: readonly string[] = FORGE_SCOPE_CATALOG.map((e) => e.scope);

/**
 * The scopes no bundle ever carries (forge ClassSensitive): their leak is not
 * recoverable by revoking the token. A token holds one only when it names it.
 */
export const SENSITIVE_SCOPES: readonly string[] = FORGE_SCOPE_CATALOG.filter((e) => e.class === "sensitive").map(
  (e) => e.scope,
);

export function isForgeBundle(name: string): name is ForgeBundle {
  return (FORGE_BUNDLES as readonly string[]).includes(name);
}

/**
 * What bundle b grants, in catalog order (forge ExpandBundle). A sensitive
 * scope is skipped even if a row ever named a bundle for it, as forge does, so
 * nothing shown as a bundle's expansion is ever sensitive.
 */
export function expandBundle(b: ForgeBundle): string[] {
  return FORGE_SCOPE_CATALOG.filter((e) => e.class !== "sensitive" && e.bundles.includes(b)).map((e) => e.scope);
}

/**
 * Whether a bundle holds a scope that acts as a person (forge ClassPersonal).
 * An organization token acts as no one, so the server refuses such a bundle
 * on one: today that is developer.
 */
export function bundleActsAsPerson(b: ForgeBundle): boolean {
  return FORGE_SCOPE_CATALOG.some((e) => e.class === "personal" && e.bundles.includes(b));
}
