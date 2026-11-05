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

import { AccessTokenService } from "@/gen/controlplane/services/access_token/v1/access_token_pb";
import { getControlPlaneClient } from "./client";

/**
 * The environment variable the forge CLI reads a token from when an
 * environment's control-plane declaration names no `token_env` of its own
 * (forge internal/cloud/credential.go DefaultTokenEnv).
 */
export const FORGE_TOKEN_ENV = "FORGE_CONTROL_PLANE_TOKEN";

/** The scopes a forge token is offered with, in display order. */
export const FORGE_TOKEN_SCOPES = [
  { scope: "deploy:read", label: "Read deployments, releases and status" },
  { scope: "deploy:write", label: "Deploy and promote releases" },
  // forge pkg/accesstoken/scope.go ScopeSecretRead: metadata everywhere, and
  // the VALUES of local environments' secrets (forge env up pulls them).
  {
    scope: "secret:read",
    label: "Read secret names and metadata, and pull the secret VALUES of local environments (forge env up)",
  },
  { scope: "secret:write", label: "Set and delete secrets" },
  { scope: "domain:read", label: "Read custom domains" },
  { scope: "domain:write", label: "Add, bind and remove custom domains" },
] as const;

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

export async function listForgeTokens(orgId: string): Promise<ForgeToken[]> {
  const res = await getControlPlaneClient(AccessTokenService).listMyTokens({ orgId });
  return res.tokens
    .filter((t) => !t.revokedAt && !t.ephemeral)
    .map((t) => ({
      id: t.id,
      name: t.name,
      prefix: t.displayPrefix,
      scopes: [...t.scopes],
      allScopes: t.allScopes,
      createdAt: t.createdAt ? timestampDate(t.createdAt) : undefined,
      expiresAt: t.expiresAt ? timestampDate(t.expiresAt) : undefined,
      lastUsedAt: t.lastUsedAt ? timestampDate(t.lastUsedAt) : undefined,
    }));
}

/** Create a token. The secret is returned once and never again. */
export async function createForgeToken(input: {
  orgId: string;
  name: string;
  scopes: string[];
  expiresInDays: number | null;
  nowMs?: number;
}): Promise<{ secret: string }> {
  const now = input.nowMs ?? Date.now();
  const res = await getControlPlaneClient(AccessTokenService).createMyToken({
    orgId: input.orgId,
    name: input.name,
    scopes: input.scopes,
    expiresAt: input.expiresInDays ? timestampFromMs(now + input.expiresInDays * 86_400_000) : undefined,
  });
  return { secret: res.secret };
}

export async function revokeForgeToken(orgId: string, id: string): Promise<void> {
  await getControlPlaneClient(AccessTokenService).revokeMyToken({ orgId, id });
}
