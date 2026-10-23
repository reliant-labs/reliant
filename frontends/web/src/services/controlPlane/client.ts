/**
 * Typed clients for control-plane Connect RPCs.
 *
 * Every cloud service module (git, daemon, onboarding, billing, …) calls
 * `getControlPlaneClient(SomeService)`. It is `connectClient` from
 * src/lib/connect.ts: every `controlplane.v1.*` service routes to
 * control-plane's admin-server there, on the same interceptor chain every
 * other transport in the app uses (timeout, auth, tracing, error logging,
 * upgrade modal, 401 recovery — minus x-daemon-last-seen, which admin-server's
 * CORS preflight does not allow). A bespoke chain here is how a previous bug
 * shipped: this transport once had only auth + upgrade, and `Resume daemon`
 * failures surfaced as raw toasts instead of the UpgradeRequiredModal.
 *
 * Wire format is Connect-JSON (protojson). Connect-go's protojson accepts and
 * emits camelCase field names natively, so request bodies should use the
 * generated TS types as-is — DO NOT send snake_case duplicates or
 * hand-rolled aliases.
 */
import type { Client } from "@connectrpc/connect";
import type { DescService } from "@bufbuild/protobuf";
import { connectClient } from "@/lib/connect";
import { CONTROL_PLANE_API_URL } from "./config";

/**
 * A typed Connect client for a control-plane service. The auth interceptor
 * (inside the shared chain) reads the session token on every request, so
 * token refreshes are picked up without rebuilding anything.
 *
 * Throws synchronously when `VITE_CONTROL_PLANE_API_URL` isn't configured —
 * callers that already gate on `hasControlPlane` will never see that case.
 */
export function getControlPlaneClient<T extends DescService>(service: T): Client<T> {
  if (!CONTROL_PLANE_API_URL) {
    throw new Error("Control plane API URL not configured (VITE_CONTROL_PLANE_API_URL)");
  }
  return connectClient(service);
}
