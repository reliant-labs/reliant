// Copyright (c) 2025 Reliant Labs

/**
 * The caller's own access tokens for the forge CLI and CI
 * (`controlplane.v1.AccessTokenService` ListMyTokens / CreateMyToken /
 * RevokeMyToken), scoped to the active organization.
 *
 * A token acts as the person who made it, clipped to the scopes it names and
 * to what that person holds in the org: the server enforces both, so the
 * scopes offered here are a convenience, never the control.
 */

import { timestampDate, timestampFromMs } from "@bufbuild/protobuf/wkt";

import { type AccessToken, AccessTokenService } from "@/gen/controlplane/services/access_token/v1/access_token_pb";
import { getControlPlaneClient } from "./client";

/**
 * The environment variable the forge CLI reads a token from when an
 * environment's control-plane declaration names no `token_env` of its own
 * (forge internal/cloud/credential.go DefaultTokenEnv).
 */
export const FORGE_TOKEN_ENV = "FORGE_CONTROL_PLANE_TOKEN";

export interface ForgeTokenScope {
  scope: string;
  label: string;
}

export interface ForgeTokenScopeGroup {
  id: string;
  title: string;
  /** What the group is for, and who may grant it. */
  description?: string;
  scopes: readonly ForgeTokenScope[];
}

/**
 * Every scope a person can put on a token from this page, grouped by product
 * and in display order. It is forge's catalog (forge pkg/accesstoken/scope.go
 * AllScopes) minus the three that only mean something bound to one resource
 * this page does not bind: proxy:port (one port), mcp:connector (one
 * connector grant) and daemon:resume (one daemon). The server refuses those
 * unbound, so offering them would only offer an error.
 *
 * The org permissions (deploy, secret, domain, data access, identity,
 * billing, administration, obs:write) are granted per person in Settings ›
 * Members; the server refuses a scope the caller does not hold, and clips a
 * token to its person's grants at every use. obs:read and obs:triage are every
 * member's. The Reliant and machine scopes are the person's own authority.
 */
export const FORGE_TOKEN_SCOPE_GROUPS: readonly ForgeTokenScopeGroup[] = [
  {
    id: "deploy",
    title: "Deploy",
    scopes: [
      { scope: "deploy:read", label: "Read deployments, releases and status" },
      { scope: "deploy:write", label: "Deploy and promote releases" },
      {
        scope: "deploy:config:read",
        label: "Read the VALUES of a release's rendered configuration (env var values and manifests, redacted)",
      },
    ],
  },
  {
    id: "secrets",
    title: "Secrets",
    scopes: [
      // forge pkg/accesstoken/scope.go ScopeSecretRead: metadata everywhere, and
      // the VALUES of local environments' secrets (forge env up pulls them).
      {
        scope: "secret:read",
        label: "Read secret names and metadata, and pull the secret VALUES of local environments (forge env up)",
      },
      { scope: "secret:write", label: "Set and delete secrets" },
    ],
  },
  {
    id: "domains",
    title: "Domains",
    scopes: [
      { scope: "domain:read", label: "Read custom domains" },
      { scope: "domain:write", label: "Add, bind and remove custom domains" },
    ],
  },
  {
    id: "observability",
    title: "Observability",
    description: "Every member may grant obs:read and obs:triage; obs:write must be granted to you.",
    scopes: [
      {
        scope: "obs:read",
        label: "Read telemetry, issues and release health (forge obs check, forge obs status)",
      },
      { scope: "obs:triage", label: "Resolve, ignore and reopen issues" },
      { scope: "obs:write", label: "Save, restore and archive the organization's dashboards" },
    ],
  },
  {
    id: "data",
    title: "Data access",
    description: "Your app's users' data. An environment's own policy may still restrict it to people.",
    scopes: [
      { scope: "db:query", label: "Run read-only queries against an environment's database (forge db query)" },
      { scope: "storage:read", label: "List, preview and download an environment's stored objects" },
    ],
  },
  {
    id: "identity",
    title: "App identity",
    scopes: [
      { scope: "identity:read", label: "Read an environment's sign-in status and its end users" },
      {
        scope: "identity:manage",
        label: "Invite, lock and delete end users, change their roles and issue password-set codes",
      },
    ],
  },
  {
    id: "billing",
    title: "Billing",
    scopes: [{ scope: "billing:read", label: "Read the organization's costs and usage" }],
  },
  {
    id: "admin",
    title: "Organization administration",
    scopes: [
      { scope: "cluster:manage", label: "Register, rotate and remove your own clusters" },
      { scope: "token:read", label: "List the organization's tokens" },
      { scope: "token:write", label: "Create and revoke tokens, never with more than this token holds" },
    ],
  },
  {
    id: "reliant",
    title: "Reliant",
    description: "Act as you in Reliant: your runs, triggers and inbox.",
    scopes: [
      { scope: "reliant:api", label: "The whole Reliant API as you (includes every scope below in this group)" },
      { scope: "runs:start", label: "Start runs" },
      { scope: "runs:read", label: "Read runs, their state and events" },
      { scope: "runs:control", label: "Pause, resume, cancel and signal runs" },
      { scope: "triggers:fire", label: "Fire an existing trigger now" },
      { scope: "triggers:manage", label: "Create, edit, enable and disable triggers" },
      { scope: "inbox:read", label: "Read your inbox" },
      { scope: "inbox:manage", label: "Dismiss and restore inbox items" },
      { scope: "approvals:decide", label: "Approve or deny pending approvals" },
      { scope: "questions:answer", label: "Answer questions agents asked you" },
    ],
  },
  {
    id: "machines",
    title: "Machines and models",
    description: "These can spend your money.",
    scopes: [
      { scope: "daemon:manage", label: "List, start, stop and resize your machines" },
      { scope: "daemon:connect", label: "Connect a new daemon as you" },
      { scope: "llm:invoke", label: "Call the LLM gateway, billed to you" },
    ],
  },
  {
    id: "sourcemaps",
    title: "Source maps",
    description:
      "An organization token, not yours: it acts as no one, carries no other scope, and needs the admin role. It can only upload source maps (forge frontend sourcemaps push).",
    scopes: [{ scope: "sourcemaps:write", label: "Upload a frontend's source maps, and nothing else" }],
  },
];

/** Every scope offered, in display order. */
export const FORGE_TOKEN_SCOPES: readonly ForgeTokenScope[] = FORGE_TOKEN_SCOPE_GROUPS.flatMap((g) => g.scopes);

/**
 * Scopes that make the whole token a registry-only ORG automation credential
 * (forge pkg/accesstoken RegistryOnly): it carries nothing else and acts as no
 * person, so it is minted with CreateToken, never CreateMyToken.
 */
export const ORG_TOKEN_ONLY_SCOPES: readonly string[] = ["sourcemaps:write"];

export function isOrgTokenOnlyScope(scope: string): boolean {
  return ORG_TOKEN_ONLY_SCOPES.includes(scope);
}

/** A sensible default for CI: deploy, and read what it deployed. */
export const DEFAULT_FORGE_TOKEN_SCOPES = ["deploy:read", "deploy:write"];

export interface ForgeToken {
  id: string;
  name: string;
  /** The first characters of the secret, to recognise it by. */
  prefix: string;
  scopes: string[];
  allScopes: boolean;
  createdAt?: Date;
  expiresAt?: Date;
  lastUsedAt?: Date;
}

function toForgeToken(t: AccessToken): ForgeToken {
  return {
    id: t.id,
    name: t.name,
    prefix: t.displayPrefix,
    scopes: [...t.scopes],
    allScopes: t.allScopes,
    createdAt: t.createdAt ? timestampDate(t.createdAt) : undefined,
    expiresAt: t.expiresAt ? timestampDate(t.expiresAt) : undefined,
    lastUsedAt: t.lastUsedAt ? timestampDate(t.lastUsedAt) : undefined,
  };
}

export async function listForgeTokens(orgId: string): Promise<ForgeToken[]> {
  const res = await getControlPlaneClient(AccessTokenService).listMyTokens({ orgId });
  return res.tokens
    .filter((t) => !t.revokedAt && !t.ephemeral)
    .map(toForgeToken);
}

/**
 * Create a token. The secret is returned once and never again.
 *
 * A token naming an org-token-only scope (sourcemaps:write) is minted as an
 * organization automation token (CreateToken: admin role, no acting user);
 * every other is the caller's own (CreateMyToken). Mixing the two is refused
 * here because the server refuses it too, and the request would only fail.
 */
export async function createForgeToken(input: {
  orgId: string;
  name: string;
  scopes: string[];
  expiresInDays: number | null;
  nowMs?: number;
}): Promise<{ secret: string; kind: "personal" | "org" }> {
  const now = input.nowMs ?? Date.now();
  const expiresAt = input.expiresInDays ? timestampFromMs(now + input.expiresInDays * 86_400_000) : undefined;
  const client = getControlPlaneClient(AccessTokenService);
  if (input.scopes.some(isOrgTokenOnlyScope)) {
    const others = input.scopes.filter((s) => !isOrgTokenOnlyScope(s));
    if (others.length > 0) {
      throw new Error(`${input.scopes.filter(isOrgTokenOnlyScope).join(", ")} cannot be combined with other scopes`);
    }
    const res = await client.createToken({ orgId: input.orgId, name: input.name, scopes: input.scopes, expiresAt });
    return { secret: res.secret, kind: "org" };
  }
  const res = await client.createMyToken({ orgId: input.orgId, name: input.name, scopes: input.scopes, expiresAt });
  return { secret: res.secret, kind: "personal" };
}

export async function revokeForgeToken(orgId: string, id: string): Promise<void> {
  await getControlPlaneClient(AccessTokenService).revokeMyToken({ orgId, id });
}

/**
 * The organization's live automation tokens (no acting user), such as the
 * source-map tokens this page mints. ListTokens needs the admin role, so it
 * rejects with PermissionDenied for anyone else.
 */
export async function listOrgForgeTokens(orgId: string): Promise<ForgeToken[]> {
  const res = await getControlPlaneClient(AccessTokenService).listTokens({ orgId });
  return res.tokens
    .filter((t) => !t.revokedAt && !t.actingUserId && !t.ephemeral)
    .map(toForgeToken);
}

export async function revokeOrgForgeToken(orgId: string, id: string): Promise<void> {
  await getControlPlaneClient(AccessTokenService).revokeToken({ orgId, id });
}
