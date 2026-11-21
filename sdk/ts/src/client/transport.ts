// The Connect transport and the interceptors every Reliant call runs through.
//
// The auth, error-normalisation and trace stages are @reliantlabs/forge-web-
// runtime's (the same chain every forge frontend uses); this module composes
// them and leaves the application-specific stages (upgrade modal, machine
// wake, error reporting, 401 recovery) to the host through `interceptors`.
//
// Every call on a transport the SDK builds, unary and streaming, carries
// `Reliant-Client: sdk-ts/<version>`: the server refuses a reliant.v1 call that
// names no client once it enforces its minimum. A transport the host supplies
// is the host's, identity included (the web app names itself `web/<version>`);
// a host that wants the SDK's identity puts `sdkInterceptors()` in its chain.

import { createClient, type Client, type Interceptor, type Transport } from "@connectrpc/connect";
import type { DescService } from "@bufbuild/protobuf";
import {
  authInterceptor,
  errorNormalizeInterceptor,
  traceInterceptor,
} from "@reliantlabs/forge-web-runtime/interceptors";
import type { AuthTokenProvider } from "./auth.js";
import { RELIANT_CLIENT_HEADER, SDK_CLIENT_ID } from "./version.js";

export interface ReliantTransportOptions {
  /** Where requests go, e.g. `https://api.reliantapi.com`. Unused when `transport` is given. */
  baseUrl?: string;
  /** Bearer tokens. Without it requests go unauthenticated. */
  auth?: AuthTokenProvider;
  /**
   * Host interceptors, run after the SDK's (normalise -> client -> auth -> trace) in the
   * order given.
   */
  interceptors?: Interceptor[];
  /**
   * A transport to use as is (a mock, or the host's own routing); the
   * interceptor options then do not apply, and the host's chain must send
   * `Reliant-Client` itself: its own client id, or `sdkInterceptors()`. The SDK
   * adds no header to it, because a header the host's backends do not allow
   * would fail every cross-origin call at the CORS preflight.
   */
  transport?: Transport;
  /** Called on every outbound request so a transport can be built lazily by the host. */
  createTransport?: (options: { baseUrl: string; interceptors: Interceptor[] }) => Transport;
}

/** The SDK's stages of the interceptor chain, outermost first. */
export function sdkInterceptors(auth?: AuthTokenProvider): Interceptor[] {
  return [
    errorNormalizeInterceptor({}),
    clientInterceptor,
    authInterceptor({ getToken: () => auth?.getToken() ?? null }),
    traceInterceptor(),
  ];
}

// Names this SDK in Reliant-Client unless the call already names a client.
const clientInterceptor: Interceptor = (next) => async (req) => {
  if (!req.header.has(RELIANT_CLIENT_HEADER)) req.header.set(RELIANT_CLIENT_HEADER, SDK_CLIENT_ID);
  return next(req);
};

function withClientHeader(init: HeadersInit | undefined): Headers {
  const header = new Headers(init);
  if (!header.has(RELIANT_CLIENT_HEADER)) header.set(RELIANT_CLIENT_HEADER, SDK_CLIENT_ID);
  return header;
}

// identified wraps a transport the SDK built so every call, unary or
// streaming, names this SDK even when `createTransport` dropped the chain.
function identified(transport: Transport): Transport {
  return {
    unary: (method, signal, timeoutMs, header, input, contextValues) =>
      transport.unary(method, signal, timeoutMs, withClientHeader(header), input, contextValues),
    stream: (method, signal, timeoutMs, header, input, contextValues) =>
      transport.stream(method, signal, timeoutMs, withClientHeader(header), input, contextValues),
  };
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
  return identified(
    options.createTransport({
      baseUrl: options.baseUrl,
      interceptors: [...sdkInterceptors(options.auth), ...(options.interceptors ?? [])],
    }),
  );
}

/** A typed Connect client for one service on the transport. */
export function serviceClient<S extends DescService>(transport: Transport, service: S): Client<S> {
  return createClient(service, transport);
}
