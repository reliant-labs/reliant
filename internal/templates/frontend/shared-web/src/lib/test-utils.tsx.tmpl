// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Test utilities for forge frontend tests. Written once on scaffold;
// owned by the user thereafter. Safe to edit, extend, and re-export from.
//
// Two primitives live here:
//
//   - mockTransport(handlers): a Connect-compatible Transport whose unary
//     responses are keyed on "<ServiceTypeName>/<MethodName>" or the
//     "<ServiceName>.<MethodName>" shorthand. Per-handler entries may be:
//       { response: <any> }            — return a typed message
//       { error: <Error | string> }    — reject with a ConnectError
//       (input) => <any | Promise<any>> — compute the response from the request
//     Unmatched RPCs reject with Code.Unimplemented so missing fixtures
//     surface as a clear error.
//
//   - renderWithTransport(ui, { transport }): wraps the UI in
//     QueryClientProvider with a fresh QueryClient (retries: 0, no stale
//     time) and swaps the active transport via setTransport() so any
//     generated hook in the tree dispatches against the mock. Use this
//     in page tests; the production providers do too much (telemetry,
//     auth bridge, event bus) for a hermetic unit test.
//
// See the `frontend-testing` skill for recipes and anti-patterns.

import { Code, ConnectError } from "@connectrpc/connect";
import {
  QueryClient,
  QueryClientProvider,
} from "@tanstack/react-query";
import { render, type RenderOptions, type RenderResult } from "@testing-library/react";

import { setTransport } from "@/lib/connect";

import type {
  Transport,
  UnaryResponse,
  StreamResponse,
} from "@connectrpc/connect";
import type { ReactElement } from "react";

// MockHandler is what a caller writes for a single RPC. A `response` entry
// returns the value; an `error` entry rejects; a function computes the
// response from the request input (handy for "if id===X return Y" cases).
export type MockHandler<TIn = unknown, TOut = unknown> =
  | { response: TOut }
  | { error: Error | string | ConnectError }
  | ((input: TIn) => TOut | Promise<TOut>);

// Keys accept either "ServiceTypeName/Method" (the Connect dispatch key
// format, e.g. "user.v1.UserService/GetUser") or the friendlier
// "ServiceName.Method" (e.g. "UserService.GetUser"). Both shapes are
// looked up so callers don't have to know the proto package.
export type MockHandlers = Record<string, MockHandler>;

function normalizeKey(serviceType: string, methodName: string): string[] {
  // Always try the fully-qualified key first, then strip any leading
  // package and try the short "<ServiceName>.<Method>" form so callers
  // can write either.
  const full = `${serviceType}/${methodName}`;
  const shortService = serviceType.includes(".")
    ? serviceType.slice(serviceType.lastIndexOf(".") + 1)
    : serviceType;
  return [full, `${shortService}.${methodName}`, `${shortService}/${methodName}`];
}

function toConnectError(err: Error | string | ConnectError): ConnectError {
  if (err instanceof ConnectError) return err;
  if (err instanceof Error) return new ConnectError(err.message, Code.Internal);
  return new ConnectError(String(err), Code.Internal);
}

function makeUnaryResponse(
  method: { name: string; parent: { typeName: string } },
  message: unknown,
): UnaryResponse<never, never> {
  return {
    service: method.parent as never,
    method: method as never,
    stream: false,
    header: new Headers(),
    message: message as never,
    trailer: new Headers(),
  };
}

/**
 * mockTransport returns a Connect-compatible Transport backed by an
 * in-memory handler table. Use it in tests in place of the real
 * transport.
 *
 *   const transport = mockTransport({
 *     "UserService.GetUser": { response: { user: { id: "u_1", email: "a@b" } } },
 *     "UserService.CreateUser": () => { throw new ConnectError("exists", Code.AlreadyExists) },
 *   });
 *
 * Pair with renderWithTransport(ui, { transport }) to drive a page test.
 *
 * Unmatched RPCs reject with Code.Unimplemented and a key-listing error
 * message so it's obvious which fixture is missing.
 */
export function mockTransport(handlers: MockHandlers = {}): Transport {
  const lookup = (serviceType: string, methodName: string): MockHandler | undefined => {
    for (const key of normalizeKey(serviceType, methodName)) {
      if (key in handlers) return handlers[key];
    }
    return undefined;
  };

  const transport: Transport = {
    async unary(method, _signal, _timeoutMs, _header, input) {
      const handler = lookup(method.parent.typeName, method.name);
      if (!handler) {
        return Promise.reject(
          new ConnectError(
            `mockTransport: no handler for "${method.parent.typeName}/${method.name}" — ` +
              `register one of [${Object.keys(handlers).join(", ") || "(empty)"}]`,
            Code.Unimplemented,
          ),
        );
      }
      if (typeof handler === "function") {
        const result = await handler(input as unknown);
        return makeUnaryResponse(method, result);
      }
      if ("error" in handler) {
        return Promise.reject(toConnectError(handler.error));
      }
      return makeUnaryResponse(method, handler.response);
    },
    // Streaming is intentionally not supported by the test helper. Tests
    // that need to exercise a streaming RPC should build a tailored
    // transport in the test file; the common case (unary RPCs behind
    // React Query hooks) needs nothing more than the unary shape above.
    async stream(method): Promise<StreamResponse<never, never>> {
      return Promise.reject(
        new ConnectError(
          `mockTransport: streaming RPC "${method.parent.typeName}/${method.name}" is not supported by the test helper`,
          Code.Unimplemented,
        ),
      );
    },
  };
  return transport;
}

// freshQueryClient returns a QueryClient configured for tests: no retries
// (so a rejection surfaces immediately), no cache between tests.
function freshQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        staleTime: 0,
        gcTime: 0,
        refetchOnWindowFocus: false,
      },
      mutations: { retry: false },
    },
  });
}

export interface RenderWithTransportOptions extends Omit<RenderOptions, "wrapper"> {
  transport: Transport;
  queryClient?: QueryClient;
}

/**
 * renderWithTransport wraps the UI in QueryClientProvider and swaps the
 * active Connect transport so any generated hook in the tree dispatches
 * against the mock.
 *
 * Returns the standard Testing Library RenderResult plus the
 * QueryClient so callers can call `queryClient.clear()` or inspect
 * the cache between assertions.
 */
export function renderWithTransport(
  ui: ReactElement,
  { transport, queryClient, ...renderOptions }: RenderWithTransportOptions,
): RenderResult & { queryClient: QueryClient } {
  // Swap the live binding before render so module-level
  // `const client = connectClient(Service)` in generated hooks resolves
  // calls through the mock. connect.ts exposes `transport` as a Proxy
  // delegating to the active reference, so existing clients pick up the
  // swap without rebuild.
  setTransport(transport);
  const client = queryClient ?? freshQueryClient();
  const result = render(ui, {
    wrapper: ({ children }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    ),
    ...renderOptions,
  });
  return { ...result, queryClient: client };
}
