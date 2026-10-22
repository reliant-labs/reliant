// Copyright (c) 2025 Reliant Labs

/**
 * "Me" for a trigger's "Only from": the id the signed-in person's own events
 * carry as `trigger.sender.id` on one integration. It comes from their
 * connection to it (Connection.senderId, which the server records from the
 * provider: Slack's installing user, the GitHub user id, the Gmail address).
 *
 * On GitHub "Me" is ALWAYS the numeric user id the provider attested for the
 * person's own credential, never a login: a login can be renamed and then
 * registered by someone else. A hosted GitHub account has no connection row —
 * its token is delegated by the control plane — so its id comes from the git
 * credential (GitCredentialStatus.accountId). When control-plane could not
 * resolve that id, "Me" is not offered there and the hint says what to do.
 */

import type { Connection } from "../api/connection-grpc";
import { isGitHubUserId, normalizeSenderId } from "../lib/onlyFromFilter";
import { useConnections } from "./connection-queries";
import { useGitHubCredential } from "./useGitHubCredential";

export interface MySender {
  /** The allowlist entry for "Me", normalized; unset when it cannot be known. */
  id?: string;
  /** How to show that entry (a GitHub login); unset when the id is already readable. */
  displayName?: string;
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
  const id = normalizeSenderId(integration, connection.senderId);
  // Made before connections recorded their person (Slack says who only when
  // the app is installed), or — on GitHub — recorded as a login before
  // senders were user ids: either way, not an id an allowlist can hold.
  if (!id || (integration === "github" && !isGitHubUserId(id))) {
    return { loading: false, missing: `Reconnect ${name} to add yourself.` };
  }
  if (integration === "github") {
    return { loading: false, id, displayName: connection.accountLabel || undefined };
  }
  return { loading: false, id };
}

/** "Me" on GitHub: a saved connection's user id, else the hosted account's, when control-plane reports it. */
export function useGitHubSender(connectionId?: string): MySender {
  const fromConnection = useConnectionSender("github", connectionId);
  const credential = useGitHubCredential();
  if (fromConnection.id || fromConnection.loading) return fromConnection;
  if (credential.isLoading) return { loading: true };
  if (credential.accountId && isGitHubUserId(credential.accountId)) {
    return { loading: false, id: credential.accountId, displayName: credential.accountLogin };
  }
  if (credential.accountLogin) {
    // Never fall back to the login: it is not what trigger.sender.id carries,
    // and an allowlist of it would match nobody (or, after a rename, someone
    // else). Adding the login by hand resolves it to the id.
    return {
      loading: false,
      missing: `Your GitHub sign-in doesn't report your GitHub user id yet, so "Me" isn't available. Add your login (${credential.accountLogin}) instead.`,
    };
  }
  return fromConnection;
}
