// Copyright (c) 2026 Reliant Labs

/**
 * ConnectionService: the integrations catalog and Alex's saved connections,
 * for the Connectors page's Integrations tab. Metadata only, as the real
 * service answers (a Connection has no field that could hold a credential).
 *
 * The GitHub connection is the one Nightly CI triage reads CI with, and it
 * needs reconnecting: GitHub refused its token, which is why that automation
 * is failing (workflows.ts NIGHTLY_FAILURE, the Inbox's automation item).
 * The Slack connection is healthy.
 *
 * App layer only (../../app.ts).
 */
import { Code, ConnectError } from "@connectrpc/connect";

import { ConnectionAuthKind, ConnectionOwnerKind, ConnectionService, ConnectionStatus } from "@/gen/reliant/v1/connection_pb";

import { DAY, HOUR } from "../../shopStory";
import { isoAgo } from "../../workspace";
import { serve } from "../serve";

const method = (kind: ConnectionAuthKind, fieldLabels: Record<string, string> = {}) => ({ kind, available: true, unavailableReason: "", fieldLabels });

/** The catalog, as the Connectors page lists it: GitHub, Slack, Gmail, Twilio and HTTP. */
const INTEGRATIONS = [
  { id: "github", displayName: "GitHub", methods: [method(ConnectionAuthKind.OAUTH2)], connectionParams: [] },
  { id: "slack", displayName: "Slack", methods: [method(ConnectionAuthKind.OAUTH2)], connectionParams: [] },
  { id: "gmail", displayName: "Gmail", methods: [method(ConnectionAuthKind.OAUTH2)], connectionParams: [] },
  {
    id: "twilio",
    displayName: "Twilio",
    methods: [method(ConnectionAuthKind.BASIC, { password: "Auth token" })],
    connectionParams: [{ name: "account_sid", displayName: "Account SID", description: "Starts with AC", pattern: "AC[0-9a-f]{32}", defaultValue: "", required: true }],
  },
  { id: "http", displayName: "HTTP", methods: [method(ConnectionAuthKind.API_KEY, { api_key: "API key", header: "Header" })], connectionParams: [] },
];

const connections = () => [
  {
    id: "shop-conn-github", integrationId: "github", authKind: ConnectionAuthKind.OAUTH2, name: "GitHub (acme)", accountLabel: "acme-ci",
    externalAccountId: "4410287", scopes: ["repo", "workflow"], status: ConnectionStatus.NEEDS_REAUTH, statusReason: "invalid_grant", isDefault: true,
    accessExpiresAt: isoAgo(2 * DAY), lastUsedAt: isoAgo(18 * HOUR), createdAt: isoAgo(20 * DAY), updatedAt: isoAgo(18 * HOUR), params: {},
    senderId: "4410287", ownerKind: ConnectionOwnerKind.USER, orgId: "",
  },
  {
    id: "shop-conn-slack", integrationId: "slack", authKind: ConnectionAuthKind.OAUTH2, name: "Slack (Acme)", accountLabel: "acme.slack.com",
    externalAccountId: "T0ACME", scopes: ["chat:write", "channels:read"], status: ConnectionStatus.ACTIVE, statusReason: "", isDefault: true,
    accessExpiresAt: "", lastUsedAt: isoAgo(3 * HOUR), createdAt: isoAgo(19 * DAY), updatedAt: isoAgo(3 * HOUR), params: {},
    senderId: "U0ALEX", ownerKind: ConnectionOwnerKind.USER, orgId: "",
  },
];

export function connectionHandlers() {
  return serve(ConnectionService, {
    listIntegrations: () => ({ integrations: INTEGRATIONS }),
    listConnections: (req) => ({ connections: connections().filter((c) => !req.integrationId || c.integrationId === req.integrationId) }),
    getConnection: (req) => {
      const connection = connections().find((c) => c.id === req.id);
      if (!connection) throw new ConnectError("connection not found", Code.NotFound);
      return { connection };
    },
  });
}
