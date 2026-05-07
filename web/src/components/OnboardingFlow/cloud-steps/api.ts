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

function getControlPlaneApiUrl(): string {
  const configuredUrl = import.meta.env.VITE_CONTROL_PLANE_API_URL;
  if (configuredUrl) {
    if (import.meta.env.DEV && configuredUrl.includes("reliant-provider.localhost")) {
      throw new Error(
        "VITE_CONTROL_PLANE_API_URL points at the provider app host. Set it to the control-plane/admin Connect API URL."
      );
    }

    return configuredUrl;
  }

  if (import.meta.env.DEV && window.location.hostname !== "localhost") {
    throw new Error(
      "VITE_CONTROL_PLANE_API_URL is required for cloud onboarding when the provider app is served from a dev host alias."
    );
  }

  return "/rpc";
}

const CONTROL_PLANE_API_URL = getControlPlaneApiUrl();

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
