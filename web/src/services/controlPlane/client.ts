/**
 * Shared transport + typed-client factory for control-plane Connect RPCs.
 *
 * Every cloud service module (git, daemon, onboarding, billing, …) calls into
 * `getControlPlaneClient(SomeService)` to get a `Client<SomeService>` wired up
 * with the Supabase auth header. The transport is created per call so the
 * interceptor closure always reads the freshest session token.
 *
 * Wire format is Connect-JSON (protojson). Connect-go's protojson accepts and
 * emits camelCase field names natively, so request bodies should use the
 * generated TS types as-is — DO NOT send snake_case duplicates or
 * hand-rolled aliases.
 */
import { createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import type { DescService } from "@bufbuild/protobuf";
import { supabase } from "@/lib/supabase";
import { upgradeInterceptor } from "@/api/upgradeInterceptor";
import { CONTROL_PLANE_API_URL } from "./config";

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
 * Create a typed Connect client for a control-plane service. The auth
 * interceptor reads the Supabase session token on every request so token
 * refreshes are picked up without rebuilding the transport.
 *
 * Throws synchronously when `VITE_CONTROL_PLANE_API_URL` isn't configured —
 * callers that already gate on `hasControlPlane` will never see that case.
 */
export function getControlPlaneClient<T extends DescService>(
  service: T,
): Client<T> {
  if (!CONTROL_PLANE_API_URL) {
    throw new Error(
      "Control plane API URL not configured (VITE_CONTROL_PLANE_API_URL)",
    );
  }

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
      // Open the UpgradeRequiredModal on ResourceExhausted errors that carry
      // X-Reliant-Reason / X-Reliant-Upgrade-URL metadata. The project
      // picker's "Resume onboarding daemon" button calls
      // controlplane.v1.DaemonService.ResumeDaemon through this transport;
      // without the interceptor here, the per-org compute-cap error showed
      // up only as a raw toast.
      upgradeInterceptor,
    ],
  });

  return createClient(service, transport);
}
