// The Connect transport and the interceptors every Reliant call runs through.
//
// The auth, error-normalisation and trace stages are @reliantlabs/forge-web-
// runtime's (the same chain every forge frontend uses); this module composes
// them and leaves the application-specific stages (upgrade modal, machine
// wake, error reporting, 401 recovery) to the host through `interceptors`.

import { createClient, type Client, type Interceptor, type Transport } from "@connectrpc/connect";
import type { DescService } from "@bufbuild/protobuf";
import {
  authInterceptor,
  errorNormalizeInterceptor,
  traceInterceptor,
} from "@reliantlabs/forge-web-runtime/interceptors";
import type { AuthTokenProvider } from "./auth.js";

export interface ReliantTransportOptions {
  /** Where requests go, e.g. `https://api.reliantapi.com`. Unused when `transport` is given. */
  baseUrl?: string;
  /** Bearer tokens. Without it requests go unauthenticated. */
  auth?: AuthTokenProvider;
  /**
   * Host interceptors, run after the SDK's (normalise -> auth -> trace) in the
   * order given.
   */
  interceptors?: Interceptor[];
  /** A transport to use as is (a mock, or the host's own routing); the interceptor options then do not apply. */
  transport?: Transport;
  /** Called on every outbound request so a transport can be built lazily by the host. */
  createTransport?: (options: { baseUrl: string; interceptors: Interceptor[] }) => Transport;
}

/** The SDK's stages of the interceptor chain, outermost first. */
export function sdkInterceptors(auth?: AuthTokenProvider): Interceptor[] {
  return [
    errorNormalizeInterceptor({}),
    authInterceptor({ getToken: () => auth?.getToken() ?? null }),
    traceInterceptor(),
  ];
}

/**
 * Resolve the transport for the options. A transport given is used as is;
 * otherwise `createTransport` builds one from the base URL and the full chain.
 */
export function resolveTransport(options: ReliantTransportOptions): Transport {
  if (options.transport) return options.transport;
  if (!options.createTransport || !options.baseUrl) {
    throw new Error(
      "@reliantlabs/sdk: pass `transport`, or `baseUrl` with `createTransport` (for example createConnectTransport from @connectrpc/connect-web)",
    );
  }
  return options.createTransport({
    baseUrl: options.baseUrl,
    interceptors: [...sdkInterceptors(options.auth), ...(options.interceptors ?? [])],
  });
}

/** A typed Connect client for one service on the transport. */
export function serviceClient<S extends DescService>(transport: Transport, service: S): Client<S> {
  return createClient(service, transport);
}
