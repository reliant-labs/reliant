/**
 * Shared transport + typed-client factory for control-plane Connect RPCs.
 *
 * Every cloud service module (git, daemon, onboarding, billing, …) calls into
 * `getControlPlaneClient(SomeService)` to get a `Client<SomeService>` wired up
 * with the same interceptor chain every other transport in the app uses
 * (timeout, auth, tracing, error logging, upgrade modal, 401 sign-out). The
 * canonical chain lives in `@/api/transport`; passing a bespoke inline array
 * here is how the previous bug shipped — the third transport was missing
 * everything but auth + upgrade, and `Resume daemon` failures surfaced as raw
 * toasts instead of the UpgradeRequiredModal.
 *
 * Wire format is Connect-JSON (protojson). Connect-go's protojson accepts and
 * emits camelCase field names natively, so request bodies should use the
 * generated TS types as-is — DO NOT send snake_case duplicates or
 * hand-rolled aliases.
 */
import { createClient, type Client } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import type { DescService } from "@bufbuild/protobuf";
import { buildInterceptors } from "@/api/transport";
import { CONTROL_PLANE_API_URL } from "./config";

/**
 * Create a typed Connect client for a control-plane service. The auth
 * interceptor (inside the shared chain) reads the Supabase session token on
 * every request so token refreshes are picked up without rebuilding the
 * transport.
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

  // interceptors via buildInterceptors — see api/transport.ts
  const transport = createConnectTransport({
    baseUrl: CONTROL_PLANE_API_URL,
    interceptors: buildInterceptors({ withAuth: true }),
  });

  return createClient(service, transport);
}
