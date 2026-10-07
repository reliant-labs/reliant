// Copyright (c) 2025 Reliant Labs

/**
 * "Me" for a trigger's "Only from": the id the signed-in person's own events
 * carry as `trigger.sender.id` on one integration. It comes from their
 * connection to it (Connection.senderId, which the server records from the
 * provider: Slack's installing user, the GitHub login, the Gmail address).
 * A hosted GitHub account has no connection row — its token is delegated by
 * the control plane — so its login comes from the git credential instead.
 */

import type { Connection } from "../api/connection-grpc";
import { normalizeSenderId } from "../lib/onlyFromFilter";
import { useConnections } from "./connection-queries";
import { useGitHubCredential } from "./useGitHubCredential";

export interface MySender {
  /** The allowlist entry for "Me", normalized; unset when it cannot be known. */
  id?: string;
  loading: boolean;
  /** Why there is no id, in words for a hint. */
  missing?: string;
}

const NAMES: Record<string, string> = { slack: "Slack", github: "GitHub", gmail: "Gmail" };

/** The connection "Me" is read from: the named one, else the default, else the first that works. */
export function senderConnection(connections: readonly Connection[], connectionId?: string): Connection | undefined {
  if (connectionId) return connections.find((c) => c.id === connectionId);
  const usable = connections.filter((c) => c.status === "active");
  return usable.find((c) => c.isDefault) ?? usable[0];
}

/** "Me" from the caller's connection to integration (connectionId when one is chosen). */
export function useConnectionSender(integration: string, connectionId?: string): MySender {
  const query = useConnections(integration);
  const name = NAMES[integration] ?? integration;
  if (query.isLoading) return { loading: true };
  const connection = senderConnection(query.data ?? [], connectionId);
  if (!connection) return { loading: false, missing: `Connect ${name} to add yourself.` };
  if (!connection.senderId) {
    // Made before connections recorded their person (Slack says who only
    // when the app is installed).
    return { loading: false, missing: `Reconnect ${name} to add yourself.` };
  }
  return { loading: false, id: normalizeSenderId(integration, connection.senderId) };
}

/** "Me" on GitHub: a saved connection's login, else the hosted account's. */
export function useGitHubSender(connectionId?: string): MySender {
  const fromConnection = useConnectionSender("github", connectionId);
  const credential = useGitHubCredential();
  if (fromConnection.id || fromConnection.loading) return fromConnection;
  if (credential.isLoading) return { loading: true };
  if (credential.accountLogin) return { loading: false, id: normalizeSenderId("github", credential.accountLogin) };
  return fromConnection;
}
