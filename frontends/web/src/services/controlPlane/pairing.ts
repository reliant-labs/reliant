/**
 * Pairing a self-hosted machine: the web half of RFC 8628 device
 * authorization (control-plane internal/cliauth/device.go).
 *
 * `reliant daemon start` on the machine asks the control plane for a short
 * code ("BDFG-HJKL") and prints it. The person types it here, or opens the
 * link the CLI printed; this module looks the code up — so the UI can ask
 * "Approve machine <hostname>?" — and records the answer. The CLI, polling,
 * receives its credential and connects; the waiting UI sees a new machine in
 * the registry.
 *
 * These are plain HTTP routes on the control plane, authenticated with the
 * session JWT the same way the CLI-login consent page approves a login. They
 * are not Connect RPCs because the CLI's half is standard OAuth.
 */
import { supabase } from "@/lib/supabase";
import { getControlPlaneURL } from "@/lib/constants";

export interface PendingMachine {
  /** The code as the person should read it, "BDFG-HJKL". */
  userCode: string;
  /** What the CLI reported about its machine. Display text only. */
  hostname: string;
  platform: string;
  clientId: string;
  scopes: string[];
  expiresAt: string;
}

export class PairingError extends Error {
  /** 404: no machine is waiting with that code. */
  readonly notFound: boolean;

  constructor(message: string, notFound = false) {
    super(message);
    this.name = "PairingError";
    this.notFound = notFound;
  }
}

/** The 8 letters of a code, whatever the person typed around them. */
export function normalizePairingCode(typed: string): string {
  return typed.toUpperCase().replace(/[^A-Z]/g, "");
}

/** "bdfghjkl" → "BDFG-HJKL"; shorter input is returned normalized. */
export function formatPairingCode(typed: string): string {
  const code = normalizePairingCode(typed);
  return code.length === 8 ? `${code.slice(0, 4)}-${code.slice(4)}` : code;
}

async function post<T>(path: string, body: unknown): Promise<T> {
  const base = getControlPlaneURL();
  if (!base) throw new PairingError("Pairing needs the Reliant control plane, which this build is not connected to.");
  const {
    data: { session },
  } = await supabase.auth.getSession();
  if (!session) throw new PairingError("Your session ended. Sign in again to approve the machine.");
  const response = await fetch(`${base}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${session.access_token}` },
    body: JSON.stringify(body),
  });
  const data = (await response.json().catch(() => ({}))) as T & { error_description?: string };
  if (!response.ok) {
    throw new PairingError(
      data.error_description ?? `The control plane refused the request (HTTP ${response.status}).`,
      response.status === 404,
    );
  }
  return data;
}

/** The machine waiting with this code, for the approval prompt. */
export async function lookupPendingMachine(code: string): Promise<PendingMachine> {
  const raw = await post<{
    user_code: string;
    hostname: string;
    platform: string;
    client_id: string;
    scopes: string[] | null;
    expires_at: string;
  }>("/oauth/device/lookup", { user_code: normalizePairingCode(code) });
  return {
    userCode: raw.user_code,
    hostname: raw.hostname,
    platform: raw.platform,
    clientId: raw.client_id,
    scopes: raw.scopes ?? [],
    expiresAt: raw.expires_at,
  };
}

/** Approve (or decline) the machine waiting with this code. */
export async function decidePendingMachine(code: string, approved: boolean, orgId?: string | null): Promise<void> {
  // org_id: the org the machine's credential will act in, fixed when it is
  // minted. Omitted, the control plane uses the person's default org.
  await post("/oauth/device/approve", {
    user_code: normalizePairingCode(code),
    approved,
    ...(orgId ? { org_id: orgId } : {}),
  });
}
