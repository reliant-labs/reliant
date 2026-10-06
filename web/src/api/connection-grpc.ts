// Copyright (c) 2025 Reliant Labs

/**
 * Thin client over `reliant.v1.ConnectionService`: the caller's saved logins
 * to integrations (research/CONNECTIONS_VAULT.md). Metadata only — no RPC
 * here ever returns a secret.
 *
 * OAuth is two authenticated RPCs with the provider in between. StartOAuth
 * binds the flow to the signed-in user and returns the provider URL; the
 * provider redirects to the API, which relays the code back to this client
 * (the web app's `/connections/oauth/callback`, or the desktop app's loopback
 * receiver); CompleteOAuth finishes it. Nothing depends on a cookie: the app
 * and the API are different sites, so none would survive. The whole client
 * flow is lib/connection-oauth.ts.
 */

import { create } from "@bufbuild/protobuf";
import { ConnectError } from "@connectrpc/connect";

import { grpcClient } from "./grpc-client";
import {
  ApiKeyConnectionKind,
  CompleteOAuthRequestSchema,
  ConnectionAuthKind,
  ConnectionStatus,
  CreateApiKeyConnectionRequestSchema,
  ListConnectionsRequestSchema,
  ListIntegrationsRequestSchema,
  StartOAuthRequestSchema,
  type Connection as ProtoConnection,
  type Integration as ProtoIntegration,
  type IntegrationAuthMethod as ProtoAuthMethod,
  type IntegrationConnectionParam as ProtoConnectionParam,
} from "../gen/reliant/v1/connection_pb";

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
   * name them in `trigger.sender.id` (a Slack user id, a GitHub login, a
   * Gmail address); "" when the integration does not say. What "Only from:
   * Me" allowlists.
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

export const connectionGrpc = {
  async list(integrationId?: string): Promise<Connection[]> {
    const response = await grpcClient
      .connection()
      .listConnections(create(ListConnectionsRequestSchema, { integrationId: integrationId ?? "" }));
    return response.connections.map(connectionFromProto);
  },

  async listIntegrations(): Promise<Integration[]> {
    const response = await grpcClient.connection().listIntegrations(create(ListIntegrationsRequestSchema, {}));
    return response.integrations.map(integrationFromProto);
  },

  async createApiKey(input: {
    integrationId: string;
    name: string;
    kind: "api_key" | "basic";
    fields: Record<string, string>;
    params: Record<string, string>;
  }): Promise<Connection> {
    const response = await grpcClient.connection().createApiKeyConnection(
      create(CreateApiKeyConnectionRequestSchema, {
        integrationId: input.integrationId,
        name: input.name,
        kind: input.kind === "basic" ? ApiKeyConnectionKind.BASIC : ApiKeyConnectionKind.API_KEY,
        fields: input.fields,
        params: input.params,
      }),
    );
    if (!response.connection) throw new Error("No connection in response");
    return connectionFromProto(response.connection);
  },

  /**
   * Begin an OAuth flow for the signed-in user and return the provider URL to
   * send them to. Without `loopbackRedirect` the code is relayed back to this
   * web app's origin (the browser's Origin header, which the server checks
   * against the origins it serves); a desktop app passes its loopback
   * receiver. `redirectAfter` is the relative path to land on afterwards.
   */
  async startOAuth(input: {
    integrationId: string;
    name?: string;
    redirectAfter: string;
    params?: Record<string, string>;
    loopbackRedirect?: string;
  }): Promise<string> {
    const response = await grpcClient.connection().startOAuth(
      create(StartOAuthRequestSchema, {
        integrationId: input.integrationId,
        name: input.name ?? "",
        redirectAfter: input.redirectAfter,
        params: input.params ?? {},
        loopbackRedirect: input.loopbackRedirect ?? "",
      }),
    );
    if (!response.authorizeUrl) throw new Error("The server did not return an authorization URL");
    return response.authorizeUrl;
  },

  /** Finish a flow with the code and state the API relayed back. */
  async completeOAuth(input: { state: string; code: string }): Promise<{ connection: Connection; redirectAfter: string }> {
    const response = await grpcClient.connection().completeOAuth(create(CompleteOAuthRequestSchema, input));
    if (!response.connection) throw new Error("No connection in response");
    return { connection: connectionFromProto(response.connection), redirectAfter: response.redirectAfter };
  },
};
