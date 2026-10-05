// Copyright (c) 2025 Reliant Labs

/**
 * Thin client over `reliant.v1.ConnectionService`: the caller's saved logins
 * to integrations (research/CONNECTIONS_VAULT.md). Metadata only — no RPC
 * here ever returns a secret.
 *
 * OAuth in a browser goes through `/integrations/oauth/{provider}/start`, not
 * the StartOAuth RPC: the HTTP route also sets the cookie that binds the
 * callback to this browser. A navigation cannot carry the Authorization
 * header, so the app fetches the route with `mode=json` (which sets the
 * cookie) and then navigates to the returned provider URL.
 */

import { create } from "@bufbuild/protobuf";
import { ConnectError } from "@connectrpc/connect";

import { grpcClient, getGRPCBaseURLPublic } from "./grpc-client";
import { getAuthTokenProvider } from "./authProvider";
import {
  ApiKeyConnectionKind,
  ConnectionAuthKind,
  ConnectionStatus,
  CreateApiKeyConnectionRequestSchema,
  ListConnectionsRequestSchema,
  ListIntegrationsRequestSchema,
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

/** Where the browser OAuth routes live: the api-server origin the RPCs use. */
function apiOrigin(): string {
  return getGRPCBaseURLPublic() ?? window.location.origin;
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
   * Begin a browser OAuth flow and return the provider URL to navigate to.
   * `redirectAfter` is the relative path the callback lands on; it receives
   * `?connection=<id>` or `?connection_error=<class>`.
   */
  async startBrowserOAuth(input: {
    integrationId: string;
    name?: string;
    redirectAfter: string;
  }): Promise<string> {
    const url = new URL(`/integrations/oauth/${encodeURIComponent(input.integrationId)}/start`, apiOrigin());
    url.searchParams.set("mode", "json");
    url.searchParams.set("redirect_after", input.redirectAfter);
    if (input.name) url.searchParams.set("name", input.name);
    const token = await getAuthTokenProvider().getToken();
    const response = await fetch(url, {
      credentials: "include",
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!response.ok) {
      const text = (await response.text()).trim();
      throw new Error(text || `Could not start the connection (${response.status})`);
    }
    const body = (await response.json()) as { authorize_url?: string };
    if (!body.authorize_url) throw new Error("The server did not return an authorization URL");
    return body.authorize_url;
  },
};
