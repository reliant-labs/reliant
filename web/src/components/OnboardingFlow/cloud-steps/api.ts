/**
 * Control-plane API client for cloud onboarding steps.
 *
 * After injection into the reliant web app (via inject-cloud-onboarding.sh),
 * this file lives at OnboardingFlow/cloud-steps/api.ts and imports resolve
 * relative to that position:
 *   - Supabase client: ../../../lib/supabase
 *   - Generated proto types: ./gen/admin_connect  (copied by the injection script)
 */

import { createClient } from "@connectrpc/connect";
import type { Client } from "@connectrpc/connect";
import type { DescService } from "@bufbuild/protobuf";
import { createConnectTransport } from "@connectrpc/connect-web";
import { supabase } from "../../../lib/supabase";

// Control-plane API URL — injected at build time or defaults to /rpc
const CONTROL_PLANE_API_URL =
  import.meta.env.VITE_CONTROL_PLANE_API_URL || "/rpc";

/**
 * Get the current Supabase auth token.
 * Returns undefined if not authenticated.
 */
async function getAuthToken(): Promise<string | undefined> {
  try {
    const {
      data: { session },
    } = await supabase.auth.getSession();
    return session?.access_token ?? undefined;
  } catch {
    return undefined;
  }
}

/**
 * Create a typed Connect client for a control-plane service.
 * Lazily fetches the auth token on each RPC call via an interceptor.
 */
export function getControlPlaneClient<T extends DescService>(
  service: T
): Client<T> {
  const transport = createConnectTransport({
    baseUrl: CONTROL_PLANE_API_URL,
    interceptors: [
      (next) => async (req) => {
        const token = await getAuthToken();
        if (token) {
          req.header.set("Authorization", `Bearer ${token}`);
        }
        return next(req);
      },
    ],
  });

  return createClient(service, transport);
}
