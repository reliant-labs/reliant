// Copyright (c) 2025 Reliant Labs

/**
 * The caller's own access tokens for the forge CLI and CI
 * (`controlplane.v1.AccessTokenService` ListMyTokens / CreateMyToken /
 * RevokeMyToken), scoped to the active organization.
 *
 * Reads and revokes run on the generated hooks
 * (hooks/controlplane/access-token-service-hooks_gen.ts); this module holds
 * their `select` mappers and createForgeToken, which picks between two rpcs.
 *
 * A token acts as the person who made it, clipped to the scopes it names and
 * to what that person holds in the org: the server enforces both, so the
 * scopes offered here are a convenience, never the control.
 */

import { timestampDate, timestampFromMs } from "@bufbuild/protobuf/wkt";

import {
  type AccessToken,
  AccessTokenService,
  type ListMyTokensResponse,
  type ListTokensResponse,
} from "@/gen/controlplane/services/access_token/v1/access_token_pb";
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
 * unbound, so offering them would only offer an error. It also omits
 * mcp:oauth and mcp:forge (accesstoken.OAuthClientScopes), which an
 * authorization server issues to an MCP client and a person never mints.
 *
 * The org permissions (deploy, secret, domain, vuln:read, tests, changes,
 * data access, identity, billing, administration, obs:write) are granted per
 * person in Settings ›
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
    id: "security",
    title: "Security",
    // forge pkg/accesstoken/scope.go ScopeVulnRead: an attack map, so neither
    // deploy:read nor deploy:write implies it.
    description: "Not included in deploy:read: what is exploitable in a running release is granted on its own.",
    scopes: [{ scope: "vuln:read", label: "Read vulnerability findings of releases and environments" }],
  },
  {
    id: "sbom",
    title: "SBOMs",
    // forge pkg/accesstoken/scope.go ScopeSBOMWrite: write-once, so CI mints
    // it only for a protected ref (FederationProtectedRef).
    description: "Narrow upload scope for CI. It implies nothing and nothing implies it, not even vuln:read.",
    scopes: [{ scope: "sbom:write", label: "Record a release's SBOM, and nothing else (it reads nothing back; the first one recorded stands)" }],
  },
  {
    id: "tests",
    title: "Tests",
    description:
      "tests:write is an organization token for CI, not yours: it acts as no one, carries no other scope, and needs the admin role. It can only upload test runs.",
    scopes: [
      { scope: "tests:read", label: "Read test runs and their failures, coverage and flaky tests" },
      { scope: "tests:write", label: "Upload a test run, and nothing else (it cannot read runs back)" },
    ],
  },
  {
    id: "changes",
    title: "Changes",
    description: "Remote config, gradual rollouts and A/B tests. Launching one shifts production traffic.",
    scopes: [
      {
        scope: "changes:read",
        label: "Read an environment's changes snapshot, for an app not on Reliant hosting (changes:write and changes:launch include it)",
      },
      { scope: "changes:write", label: "Draft, edit and stop changes (stopping is the kill switch)" },
      {
        scope: "changes:launch",
        label: "Launch a change to real users: start an experiment, ramp a rollout, roll a variant out (includes changes:write)",
      },
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
      "An organization token, not yours: it acts as no one, carries no other scope, and needs the admin role. It can only upload source maps (forge ui sourcemaps push).",
    scopes: [{ scope: "sourcemaps:write", label: "Upload a frontend's source maps, and nothing else" }],
  },
];

/** Every scope offered, in display order. */
export const FORGE_TOKEN_SCOPES: readonly ForgeTokenScope[] = FORGE_TOKEN_SCOPE_GROUPS.flatMap((g) => g.scopes);

/**
 * Upload-only scopes minted as an ORG automation credential that acts as no
 * person and carries nothing else, so with CreateToken, never CreateMyToken.
 *
 * sourcemaps:write makes its whole token registry-only (forge
 * pkg/accesstoken RegistryOnly): the server refuses it beside any other scope.
 * tests:write is not registry-only, but it is what a pull request's CI job
 * uploads its test runs with (control-plane RecordTestRun), so it is minted
 * the same way: a credential that can upload and do nothing else.
 */
export const ORG_TOKEN_ONLY_SCOPES: readonly string[] = ["sourcemaps:write", "tests:write"];

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

/**
 * The caller's live tokens in a ListMyTokens answer: the `select` of
 * `useListMyTokens`. A revoked or ephemeral credential is never listed.
 */
export function liveForgeTokens(res: ListMyTokensResponse): ForgeToken[] {
  return res.tokens
    .filter((t) => !t.revokedAt && !t.ephemeral)
    .map(toForgeToken);
}

/**
 * Create a token. The secret is returned once and never again.
 *
 * A token naming an org-token-only scope (sourcemaps:write, tests:write) is
 * minted as an organization automation token (CreateToken: admin role, no
 * acting user); every other is the caller's own (CreateMyToken). Such a scope
 * stands alone: combining it with any other scope, another org-token-only one
 * included, is refused here (the server refuses it for sourcemaps:write).
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
    if (input.scopes.length > 1) {
      throw new Error(`${input.scopes.filter(isOrgTokenOnlyScope).join(", ")} cannot be combined with other scopes`);
    }
    const res = await client.createToken({ orgId: input.orgId, name: input.name, scopes: input.scopes, expiresAt });
    return { secret: res.secret, kind: "org" };
  }
  const res = await client.createMyToken({ orgId: input.orgId, name: input.name, scopes: input.scopes, expiresAt });
  return { secret: res.secret, kind: "personal" };
}

/**
 * The organization's live automation tokens (no acting user) in a ListTokens
 * answer, such as the source-map tokens this page mints: the `select` of
 * `useListTokens`. ListTokens needs the admin role, so it rejects with
 * PermissionDenied for anyone else.
 */
export function liveOrgForgeTokens(res: ListTokensResponse): ForgeToken[] {
  return res.tokens
    .filter((t) => !t.revokedAt && !t.actingUserId && !t.ephemeral)
    .map(toForgeToken);
}
