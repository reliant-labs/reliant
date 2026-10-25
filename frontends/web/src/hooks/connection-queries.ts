// Copyright (c) 2025 Reliant Labs

/**
 * The caller's saved logins to integrations — `reliant.v1.ConnectionService`
 * (research/CONNECTIONS_VAULT.md) — on the generated hooks and keys. Metadata
 * only: no RPC here ever returns a secret.
 *
 * The cache holds what the generated hooks fetch (the proto responses) under
 * `connectionServiceKeys`; components read the plain frontend types mapped
 * from it, so they never touch `$typeName` or proto enums. A connection made,
 * changed or removed also flips `connected` across the integration catalog,
 * so every write here invalidates `catalogServiceKeys.all` as well — another
 * service's keys, which the generated mutation cannot know about.
 *
 * OAuth is two authenticated RPCs with the provider in between; the whole
 * client flow is lib/connection-oauth.ts.
 */

import { useMemo } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { ConnectError } from "@connectrpc/connect";

import {
  ApiKeyConnectionKind,
  ConnectionAuthKind,
  ConnectionStatus,
  type Connection as ProtoConnection,
  type CreateApiKeyConnectionResponse,
  type Integration as ProtoIntegration,
  type IntegrationAuthMethod as ProtoAuthMethod,
  type IntegrationConnectionParam as ProtoConnectionParam,
  type ListConnectionsResponse,
} from "../gen/reliant/v1/connection_pb";
import { unavailableIntegrationIds } from "../lib/integrationAvailability";
import { catalogServiceKeys } from "./catalog-service-hooks_gen";
import {
  connectionServiceKeys,
  useCreateApiKeyConnection as useCreateApiKeyConnectionRpc,
  useListConnections,
  useListIntegrations,
} from "./connection-service-hooks_gen";

// ── Frontend types ──────────────────────────────────────────────────────────

export type AuthKind = "oauth2" | "api_key" | "basic" | "none" | "delegated" | "unknown";

export type ConnectionHealth = "active" | "needs_reauth" | "revoked" | "unknown";

export interface Connection {
  id: string;
  integrationId: string;
  authKind: AuthKind;
  name: string;
  /** The provider identity it acts as ("octocat"). */
  accountLabel: string;
  /**
   * The provider's id for the person who made it, as that provider's events
   * name them in `trigger.sender.id` (a Slack user id, a GitHub user id — the
   * number, never the renameable login — a Gmail address); "" when the
   * integration does not say. What "Only from: Me" allowlists.
   */
  senderId: string;
  status: ConnectionHealth;
  isDefault: boolean;
}

export interface AuthMethod {
  kind: AuthKind;
  /** False when this deployment has not configured it. */
  available: boolean;
  unavailableReason: string;
  /** Form fields a pasted credential needs, keyed by the CreateApiKeyConnection field key. */
  fieldLabels: Record<string, string>;
}

export interface ConnectionParam {
  name: string;
  displayName: string;
  description: string;
  pattern: string;
  defaultValue: string;
  required: boolean;
}

export interface Integration {
  id: string;
  displayName: string;
  methods: AuthMethod[];
  params: ConnectionParam[];
}

// ── Proto mapping ───────────────────────────────────────────────────────────

export function authKindFromProto(kind: ConnectionAuthKind): AuthKind {
  switch (kind) {
    case ConnectionAuthKind.OAUTH2:
      return "oauth2";
    case ConnectionAuthKind.API_KEY:
      return "api_key";
    case ConnectionAuthKind.BASIC:
      return "basic";
    case ConnectionAuthKind.NONE:
      return "none";
    case ConnectionAuthKind.DELEGATED:
      return "delegated";
    default:
      return "unknown";
  }
}

function statusFromProto(status: ConnectionStatus): ConnectionHealth {
  switch (status) {
    case ConnectionStatus.ACTIVE:
      return "active";
    case ConnectionStatus.NEEDS_REAUTH:
      return "needs_reauth";
    case ConnectionStatus.REVOKED:
      return "revoked";
    default:
      return "unknown";
  }
}

export function authMethodFromProto(proto: ProtoAuthMethod): AuthMethod {
  return {
    kind: authKindFromProto(proto.kind),
    available: proto.available,
    unavailableReason: proto.unavailableReason,
    fieldLabels: { ...proto.fieldLabels },
  };
}

export function connectionParamFromProto(proto: ProtoConnectionParam): ConnectionParam {
  return {
    name: proto.name,
    displayName: proto.displayName || proto.name,
    description: proto.description,
    pattern: proto.pattern,
    defaultValue: proto.defaultValue,
    required: proto.required,
  };
}

export function connectionFromProto(proto: ProtoConnection): Connection {
  return {
    id: proto.id,
    integrationId: proto.integrationId,
    authKind: authKindFromProto(proto.authKind),
    name: proto.name,
    accountLabel: proto.accountLabel,
    senderId: proto.senderId,
    status: statusFromProto(proto.status),
    isDefault: proto.isDefault,
  };
}

function integrationFromProto(proto: ProtoIntegration): Integration {
  return {
    id: proto.id,
    displayName: proto.displayName || proto.id,
    methods: proto.methods.map(authMethodFromProto),
    params: proto.connectionParams.map(connectionParamFromProto),
  };
}

/** The message to show for a failed connection RPC, without connect's code prefix. */
export function connectionErrorMessage(error: unknown): string {
  if (error instanceof ConnectError) return error.rawMessage || error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}

// ── Reads ───────────────────────────────────────────────────────────────────

/** The caller's connections to one integration. */
export function useConnections(integrationId: string | undefined) {
  return useListConnections({ integrationId }, { enabled: !!integrationId, staleTime: 30_000, select: connectionsOf });
}

// Module-level, so React Query re-maps only when the response changes.
const connectionsOf = (response: ListConnectionsResponse) => response.connections.map(connectionFromProto);

/**
 * The integrations nobody can connect on this deployment (every connection
 * method switched off here), from ListIntegrations: one request for the whole
 * catalog, so the palette can say so on every row before the user clicks.
 * Empty until it loads, and if it fails — rows then read "Not connected",
 * and the connect dialog still explains.
 */
export function useUnavailableIntegrations(): ReadonlySet<string> {
  const query = useListIntegrations({}, { staleTime: 5 * 60_000 });
  const integrations = query.data?.integrations;
  return useMemo(() => unavailableIntegrationIds((integrations ?? []).map(integrationFromProto)), [integrations]);
}

// ── Writes ──────────────────────────────────────────────────────────────────

/**
 * Refetch everything a new or changed connection shows up in: every
 * ConnectionService read, and every catalog read (search results, listings,
 * entries and the input-option lists all say whether the caller is connected).
 */
export function invalidateConnectionQueries(queryClient: QueryClient) {
  void queryClient.invalidateQueries({ queryKey: connectionServiceKeys.all });
  void queryClient.invalidateQueries({ queryKey: catalogServiceKeys.all });
}

export interface ApiKeyConnectionInput {
  integrationId: string;
  name: string;
  kind: "api_key" | "basic";
  fields: Record<string, string>;
  params: Record<string, string>;
}

/** The CreateApiKeyConnection request for a pasted credential. */
export function apiKeyConnectionRequest(input: ApiKeyConnectionInput) {
  return {
    integrationId: input.integrationId,
    name: input.name,
    kind: input.kind === "basic" ? ApiKeyConnectionKind.BASIC : ApiKeyConnectionKind.API_KEY,
    fields: input.fields,
    params: input.params,
  };
}

/** The connection a CreateApiKeyConnection answer carries. */
export function createdConnection(response: CreateApiKeyConnectionResponse): Connection {
  if (!response.connection) throw new Error("No connection in response");
  return connectionFromProto(response.connection);
}

/**
 * Save a pasted credential (`mutateAsync(apiKeyConnectionRequest(...))`). The
 * generated mutation invalidates `connectionServiceKeys.all`; the catalog's
 * `connected` flags are another service's keys, invalidated here.
 */
export function useCreateApiKeyConnection() {
  const queryClient = useQueryClient();
  return useCreateApiKeyConnectionRpc({
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: catalogServiceKeys.all }),
  });
}
