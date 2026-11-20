// Copyright (c) 2026 Reliant Labs

/**
 * STUBS for the path builders the parallel U6d branches add (u6d-briefing.md,
 * "Path contract"). Each returns a real page that exists today, built by an
 * existing builder, so the nav links somewhere now; the orchestrator's merge
 * swaps each import for the real one and deletes this file. The rows match
 * with these same builders, so the swap fixes the links and the lit rows at once.
 *
 * TODO(U6d CONNECTORS): connectorsPath → lib/paths (`/connectors`, `?tab=`).
 * TODO(U6d SECURITY):   securityPath, authPath → Forge/Console/paths.ts.
 */

import { settingsPath } from "@/lib/paths";

import { consolePath } from "../../../Forge/Console/paths";

export type ConnectorsTab = "integrations" | "mcp";

/** Connectors: Integrations | MCP servers. Stub: today's Settings sections. */
export function connectorsPath(tab?: ConnectorsTab): string {
  return settingsPath(tab === "mcp" ? "mcp" : "integrations");
}

export type SecurityView = "vulnerabilities" | "sboms";

/** One Security page of an app. Stub: a placeholder console segment until the pages exist. */
export function securityPath(app: string, view: SecurityView): string {
  return consolePath(app, { kind: "extension", id: `security-${view}`, segment: `security-${view}` });
}

export type AuthTab = "users" | "sign-in";

/** The app's Auth page. Stub: today's managed-identity Users page. */
export function authPath(app: string, _tab?: AuthTab): string {
  return consolePath(app, { kind: "users" });
}
