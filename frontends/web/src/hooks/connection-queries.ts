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
  ConnectionOwnerKind,
  ConnectionStatus,
  type Connection as ProtoConnection,
  type CreateApiKeyConnectionResponse,
  type Integration as ProtoIntegration,
  type IntegrationAuthMethod as ProtoAuthMethod,
  type IntegrationConnectionParam as ProtoConnectionParam,
  type ListConnectionsResponse,
  type ListIntegrationsResponse,
} from "../gen/reliant/v1/connection_pb";
import { unavailableIntegrationIds } from "../lib/integrationAvailability";
import { useActiveOrgStore } from "../store/activeOrgStore";
import { catalogServiceKeys } from "./catalog-service-hooks_gen";
import {
  connectionServiceKeys,
  useCreateApiKeyConnection as useCreateApiKeyConnectionRpc,
  useDeleteConnection as useDeleteConnectionRpc,
  useListConnections,
  useListIntegrations,
  useRenameConnection as useRenameConnectionRpc,
  useSetDefaultConnection as useSetDefaultConnectionRpc,
  useTestConnection as useTestConnectionRpc,
} from "./connection-service-hooks_gen";
import { triggerServiceKeys } from "./trigger-service-hooks_gen";

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
  /** Why it needs re-authorising or was revoked, in the server's words; "" otherwise. */
  statusReason: string;
  isDefault: boolean;
  /** RFC 3339, "" when never used or made. */
  lastUsedAt: string;
  createdAt: string;
  /**
   * Who it belongs to: the caller ("user"), or an organization ("org", whose
   * live members may use it in that org's runs and whose admins manage it).
   * An org connection's account fields describe the org's account.
   */
  ownerKind: "user" | "org";
  /** The organization of an org connection; "" for a personal one. */
  orgId: string;
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
  /**
   * How the active org's policy treats it in the org's runs; absent when the
   * policy does not govern integrations. `enforced` false is audit: a use
   * still works and is only counted.
   */
  orgPolicy?: { allowed: boolean; enforced: boolean };
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
    statusReason: proto.statusReason,
    isDefault: proto.isDefault,
    lastUsedAt: proto.lastUsedAt,
    createdAt: proto.createdAt,
    ownerKind: proto.ownerKind === ConnectionOwnerKind.ORG ? "org" : "user",
    orgId: proto.orgId,
  };
}

function integrationFromProto(proto: ProtoIntegration): Integration {
  return {
    id: proto.id,
    displayName: proto.displayName || proto.id,
    methods: proto.methods.map(authMethodFromProto),
    params: proto.connectionParams.map(connectionParamFromProto),
    ...(proto.orgPolicy ? { orgPolicy: { allowed: proto.orgPolicy.allowed, enforced: proto.orgPolicy.enforced } } : {}),
  };
}

/**
 * The reason a create or reconnect of an org's connection is refused while
 * the deployment has not enabled org connections (orgs.connections_enabled).
 */
export const ORG_CONNECTIONS_DISABLED = "org_connections_disabled";

/** Whether err is that refusal: branch on the reason, never the message. */
export function isOrgConnectionsDisabled(err: unknown): boolean {
  return err instanceof ConnectError && err.metadata.get("x-forge-error-reason") === ORG_CONNECTIONS_DISABLED;
}

/** Personal connections, and one org's, out of a list that may hold both. */
export function splitByOwner(connections: readonly Connection[], orgId: string | undefined): { mine: Connection[]; org: Connection[] } {
  return {
    mine: connections.filter((c) => c.ownerKind === "user"),
    org: orgId ? connections.filter((c) => c.ownerKind === "org" && c.orgId === orgId) : [],
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

/** Every connection the caller has, to any integration (Settings → Integrations). */
export function useAllConnections() {
  return useListConnections({}, { staleTime: 30_000, select: connectionsOf });
}

const integrationsOf = (response: ListIntegrationsResponse) => response.integrations.map(integrationFromProto);

/**
 * ListIntegrations for the active org, named in the request itself: each
 * integration's `orgPolicy` is that org's, so the org belongs in the query key
 * (the request) — filled in on the wire alone, a switch would show the
 * previous org's markers from the cache under the new org's name. Until the
 * active org is known the request names none, and nothing is marked.
 */
function useActiveOrgListIntegrations<T = ListIntegrationsResponse>(select?: (r: ListIntegrationsResponse) => T) {
  const orgId = useActiveOrgStore((s) => s.effectiveId);
  return useListIntegrations(orgId ? { orgId } : {}, { staleTime: 5 * 60_000, ...(select ? { select } : {}) });
}

/** Every integration this deployment lists, with the ways to connect each (ListIntegrations). */
export function useIntegrations() {
  return useActiveOrgListIntegrations(integrationsOf);
}

/** Integration id → its display name ("github" → "GitHub"), from ListIntegrations. */
export function useIntegrationNames(): ReadonlyMap<string, string> {
  const query = useActiveOrgListIntegrations();
  const integrations = query.data?.integrations;
  return useMemo(() => new Map((integrations ?? []).map((i) => [i.id, i.displayName || i.id])), [integrations]);
}

/**
 * The integrations nobody can connect on this deployment (every connection
 * method switched off here), from ListIntegrations: one request for the whole
 * catalog, so the palette can say so on every row before the user clicks.
 * Empty until it loads, and if it fails — rows then read "Not connected",
 * and the connect dialog still explains.
 */
export function useUnavailableIntegrations(): ReadonlySet<string> {
  const query = useActiveOrgListIntegrations();
  const integrations = query.data?.integrations;
  return useMemo(() => unavailableIntegrationIds((integrations ?? []).map(integrationFromProto)), [integrations]);
}

/**
 * The integrations the active org's policy refuses in its runs (control-plane
 * docs/design/org-policies.md §4.3): ListIntegrations answers for the org
 * the request names (the active org). Only an ENFORCED refusal
 * is here: under audit the use works. Connecting is never refused — a
 * person's own connection stays usable in their other orgs — so a picker
 * marks these rather than hiding them.
 */
export function useOrgDisallowedIntegrations(): ReadonlySet<string> {
  const query = useActiveOrgListIntegrations();
  const integrations = query.data?.integrations;
  return useMemo(
    () => new Set((integrations ?? []).filter((i) => i.orgPolicy && !i.orgPolicy.allowed && i.orgPolicy.enforced).map((i) => i.id)),
    [integrations],
  );
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
  /** Make it this organization's instead of the caller's (its admins only). */
  ownerOrgId?: string;
}

/** The CreateApiKeyConnection request for a pasted credential. */
export function apiKeyConnectionRequest(input: ApiKeyConnectionInput) {
  return {
    integrationId: input.integrationId,
    name: input.name,
    kind: input.kind === "basic" ? ApiKeyConnectionKind.BASIC : ApiKeyConnectionKind.API_KEY,
    fields: input.fields,
    params: input.params,
    ownerOrgId: input.ownerOrgId || undefined,
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

/**
 * Rename, make default, test and remove: the generated writes, each also
 * refreshing the catalog (a removed or re-defaulted connection changes what
 * it says is connected) and the triggers that name a connection.
 */
function useConnectionWriteOptions() {
  const queryClient = useQueryClient();
  return {
    onSuccess: () => {
      invalidateConnectionQueries(queryClient);
      void queryClient.invalidateQueries({ queryKey: triggerServiceKeys.all });
    },
  };
}

export function useRenameConnection() {
  return useRenameConnectionRpc(useConnectionWriteOptions());
}

export function useSetDefaultConnection() {
  return useSetDefaultConnectionRpc(useConnectionWriteOptions());
}

export function useDeleteConnection() {
  return useDeleteConnectionRpc(useConnectionWriteOptions());
}

/** Probes the live credential; it records the account label it finds, so the list refreshes. */
export function useTestConnection() {
  return useTestConnectionRpc(useConnectionWriteOptions());
}
