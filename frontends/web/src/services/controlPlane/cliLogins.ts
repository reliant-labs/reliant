// Copyright (c) 2025 Reliant Labs

/**
 * The caller's own CLI logins (`controlplane.v1.AccessTokenService`
 * ListMyCLILogins / RevokeMyCLILogin): each `forge login` is a credential of
 * its own (an `rlci_` identity) that acts as the person on that device until
 * it expires, is signed out, or is revoked here.
 *
 * Both rpcs answer a person signed in to the web app only; the server refuses
 * a CLI login and every machine credential (reliant#1831, control-plane#2448).
 * They are the person's, not an organization's, so they carry no org.
 */

import { timestampDate } from "@bufbuild/protobuf/wkt";

import { AccessTokenService, type CLILogin as CLILoginPb } from "@/gen/controlplane/services/access_token/v1/access_token_pb";
import { getControlPlaneClient } from "./client";

export interface CliLogin {
  id: string;
  /** The CLI that logged in, e.g. `forge-cli`. */
  clientId: string;
  /** The device label the CLI sent at login. */
  deviceName: string;
  createdAt?: Date;
  expiresAt?: Date;
  /** When it last authenticated, to the minute; unset if never. */
  lastUsedAt?: Date;
}

/** What a client id is called on the page. */
export function cliClientLabel(clientId: string): string {
  switch (clientId) {
    case "forge-cli":
      return "forge CLI";
    case "reliant-cli":
      return "Reliant CLI";
    default:
      return clientId || "CLI";
  }
}

function toCliLogin(l: CLILoginPb): CliLogin {
  return {
    id: l.id,
    clientId: l.clientId,
    deviceName: l.deviceName,
    createdAt: l.createdAt ? timestampDate(l.createdAt) : undefined,
    expiresAt: l.expiresAt ? timestampDate(l.expiresAt) : undefined,
    lastUsedAt: l.lastUsedAt ? timestampDate(l.lastUsedAt) : undefined,
  };
}

/** The caller's live CLI logins, most recently used first. */
export async function listCliLogins(nowMs = Date.now()): Promise<CliLogin[]> {
  const res = await getControlPlaneClient(AccessTokenService).listMyCLILogins({});
  return res.logins
    .filter((l) => !l.revokedAt && !(l.expiresAt && timestampDate(l.expiresAt).getTime() <= nowMs))
    .map(toCliLogin)
    .sort((a, b) => (b.lastUsedAt ?? b.createdAt ?? new Date(0)).getTime() - (a.lastUsedAt ?? a.createdAt ?? new Date(0)).getTime());
}

/** Ends one of the caller's CLI logins: its next request is refused. Idempotent. */
export async function revokeCliLogin(id: string): Promise<void> {
  await getControlPlaneClient(AccessTokenService).revokeMyCLILogin({ id });
}
