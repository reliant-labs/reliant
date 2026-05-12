/**
 * Shared HTTP client for control-plane Connect RPC endpoints.
 *
 * Every cloud service module (git, secrets, deployments, …) delegates its
 * network calls here so auth handling, error formatting, and base-URL
 * resolution live in exactly one place.
 */

import { supabase } from "@/lib/supabase";
import { CONTROL_PLANE_API_URL } from "./config";

/**
 * Authenticated POST to a Connect RPC method on the control-plane API.
 *
 * @param service  Fully-qualified Connect service name, e.g. `"controlplane.v1.GitCredentialService"`
 * @param method   RPC method name, e.g. `"GetGitCredential"`
 * @param body     JSON-serialisable request payload (defaults to `{}`)
 */
export async function controlPlaneFetch(
  service: string,
  method: string,
  body: Record<string, unknown> = {},
) {
  if (!CONTROL_PLANE_API_URL) {
    throw new Error(
      "Control plane API URL not configured (VITE_CONTROL_PLANE_API_URL)",
    );
  }

  const {
    data: { session },
  } = await supabase.auth.getSession();
  if (!session) throw new Error("No active session");

  const resp = await fetch(`${CONTROL_PLANE_API_URL}/${service}/${method}`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${session.access_token}`,
    },
    body: JSON.stringify(body),
  });

  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(`${method} failed (${resp.status}): ${text}`);
  }

  return resp.json();
}
