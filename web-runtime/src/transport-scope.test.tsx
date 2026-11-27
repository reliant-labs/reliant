// @vitest-environment jsdom
//
// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// TransportScope, end to end: real React Query, real Connect clients over
// createRouterTransport transports, rendered with react-dom into jsdom. The
// service-hooks below are built exactly the way a generated hook file builds
// them once it resolves its client from the query's scope:
//
//   const clientOf = scopedClient(Svc, connectClient(Svc));
//   (req, signal, options, ctx) => clientOf(ctx).method(req, { ...options, signal })
//
// so what is asserted here is what a generated file gets.
import { createRouterTransport, type Transport } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactElement, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, describe, expect, it } from "vitest";

import {
  createMutationCall,
  createMutationHook,
  createQueryHook,
  createQueryOptions,
  createServiceMutations,
  serviceKeys,
  type ServiceCall,
} from "./service-hooks.js";
import {
  BillingService,
  GetInvoiceRequestSchema,
  type GetInvoiceResponse,
  TelemetryService,
} from "./testdata/mockgen/gen/acme/v1/billing_pb.js";
import {
  clientFor,
  createTransportScope,
  scopedClient,
  transportOf,
  TransportScopeProvider,
  useClient,
  useTransport,
  useTransportScope,
  type TransportScope,
} from "./transport-scope.js";

(
  globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }
).IS_REACT_ACT_ENVIRONMENT = true;

/**
 * A transport that answers GetInvoice with its own name as the customer
 * email, and records every request id it was sent.
 */
function namedTransport(name: string): {
  transport: Transport;
  seen: string[];
} {
  const seen: string[] = [];
  const transport = createRouterTransport((router) => {
    router.service(BillingService, {
      getInvoice: (req) => {
        seen.push(req.id);
        return { invoice: { id: req.id, customerEmail: name } };
      },
    });
  });
  return { transport, seen };
}

const live = namedTransport("live");

// The generated file: a module-scope client as the fallback, and every call
// resolving its client from the context it runs in.
const clientOf = scopedClient(
  BillingService,
  clientFor(BillingService, live.transport),
);
const call: ServiceCall<typeof GetInvoiceRequestSchema, GetInvoiceResponse> = (
  req,
  signal,
  options,
  ctx,
) => clientOf(ctx).getInvoice(req, { ...options, signal });
const keys = serviceKeys("acme.v1.BillingService");
const getInvoiceKey = keys.query("getInvoice", GetInvoiceRequestSchema);
const billingQueries = {
  getInvoice: createQueryOptions<
    typeof GetInvoiceRequestSchema,
    GetInvoiceResponse
  >(getInvoiceKey, call),
};
const useGetInvoice = createQueryHook(billingQueries.getInvoice);
const useWriteInvoice = createMutationHook<
  typeof GetInvoiceRequestSchema,
  GetInvoiceResponse
>(keys.all, call);
const billingMutations = createServiceMutations({
  writeInvoice: createMutationCall<
    typeof GetInvoiceRequestSchema,
    GetInvoiceResponse
  >(keys.all, call),
});

function quiet(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function fixtureScope(id: string): {
  scope: TransportScope;
  seen: string[];
} {
  const { transport, seen } = namedTransport(id);
  return {
    scope: createTransportScope({ id, transport, queryClient: quiet() }),
    seen,
  };
}

let unmount: (() => void) | undefined;
afterEach(() => {
  unmount?.();
  unmount = undefined;
});

async function render(node: ReactNode): Promise<HTMLElement> {
  const host = document.createElement("div");
  const root = createRoot(host);
  await act(async () => root.render(node));
  unmount = () => act(() => root.unmount());
  return host;
}

/** Waits until the rendered text satisfies `ok`, letting React Query settle. */
async function settle(host: HTMLElement, ok: (text: string) => boolean) {
  for (let i = 0; i < 50 && !ok(host.textContent ?? ""); i++) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 5));
    });
  }
  return host.textContent ?? "";
}

function Invoice({ id }: { id: string }): ReactElement {
  const { data, error } = useGetInvoice({ id });
  return (
    <span>
      {error ? `error:${error.message}` : (data?.invoice?.customerEmail ?? "…")}
    </span>
  );
}

describe("a generated read", () => {
  it("under a scope reads the scope's transport", async () => {
    const { scope, seen } = fixtureScope("fixture");
    const before = live.seen.length;
    const host = await render(
      <TransportScopeProvider scope={scope}>
        <Invoice id="inv_1" />
      </TransportScopeProvider>,
    );
    expect(await settle(host, (t) => t !== "…")).toBe("fixture");
    expect(seen).toEqual(["inv_1"]);
    expect(live.seen.length).toBe(before);
  });

  it("outside any scope reads the module fallback, as before", async () => {
    const app = quiet();
    const host = await render(
      <QueryClientProvider client={app}>
        <Invoice id="inv_2" />
      </QueryClientProvider>,
    );
    expect(await settle(host, (t) => t !== "…")).toBe("live");
    expect(live.seen).toContain("inv_2");
    expect(transportOf(app)).toBeUndefined();
  });

  it("fetched outside React on the scope's QueryClient reads the scope's transport too", async () => {
    const { scope, seen } = fixtureScope("fixture");
    const res = await scope.queryClient.fetchQuery(
      billingQueries.getInvoice({ id: "inv_3" }),
    );
    expect(res.invoice?.customerEmail).toBe("fixture");
    expect(seen).toEqual(["inv_3"]);

    const app = quiet();
    const outside = await app.fetchQuery(
      billingQueries.getInvoice({ id: "inv_3" }),
    );
    expect(outside.invoice?.customerEmail).toBe("live");
  });

  it("caches per scope: two scopes, one key, two entries", async () => {
    const a = fixtureScope("scope-a");
    const b = fixtureScope("scope-b");
    const host = await render(
      <>
        <TransportScopeProvider scope={a.scope}>
          <Invoice id="same" />
        </TransportScopeProvider>
        <TransportScopeProvider scope={b.scope}>
          <Invoice id="same" />
        </TransportScopeProvider>
      </>,
    );
    expect(await settle(host, (t) => !t.includes("…"))).toBe("scope-ascope-b");
    // The key is the module constant in both: the scope id is not in it.
    const key = billingQueries.getInvoice({ id: "same" }).queryKey;
    expect(a.scope.queryClient.getQueryData(key)?.invoice?.customerEmail).toBe(
      "scope-a",
    );
    expect(b.scope.queryClient.getQueryData(key)?.invoice?.customerEmail).toBe(
      "scope-b",
    );
    expect(a.seen).toEqual(["same"]);
    expect(b.seen).toEqual(["same"]);
  });

  it("falls back on a TanStack Query that passes no context.client (before 5.66)", async () => {
    // What an old TanStack hands a queryFn: no `client`.
    const queryFn = billingQueries.getInvoice({ id: "inv_old" }).queryFn as (
      ctx: object,
    ) => Promise<GetInvoiceResponse>;
    const res = await queryFn({
      queryKey: [],
      signal: new AbortController().signal,
      meta: undefined,
    });
    expect(res.invoice?.customerEmail).toBe("live");
  });
});

describe("a generated write", () => {
  it("from a hook under a scope goes to the scope's transport", async () => {
    const { scope, seen } = fixtureScope("fixture");
    const before = live.seen.length;
    let write: ((id: string) => Promise<GetInvoiceResponse>) | undefined;
    function Writer(): ReactElement {
      const m = useWriteInvoice();
      write = (id) => m.mutateAsync({ id });
      return <span />;
    }
    await render(
      <TransportScopeProvider scope={scope}>
        <Writer />
      </TransportScopeProvider>,
    );
    let res: GetInvoiceResponse | undefined;
    await act(async () => {
      res = await write?.("w_1");
    });
    expect(res?.invoice?.customerEmail).toBe("fixture");
    expect(seen).toEqual(["w_1"]);
    expect(live.seen.length).toBe(before);
  });

  it("imperative, bound to the scope's QueryClient, goes to the scope's transport", async () => {
    const { scope, seen } = fixtureScope("fixture");
    const res = await billingMutations(scope.queryClient).writeInvoice({
      id: "w_2",
    });
    expect(res.invoice?.customerEmail).toBe("fixture");
    expect(seen).toEqual(["w_2"]);

    const outside = await billingMutations(quiet()).writeInvoice({ id: "w_3" });
    expect(outside.invoice?.customerEmail).toBe("live");
  });
});

describe("scopedClient", () => {
  const fallback = clientFor(BillingService, live.transport);
  const of = scopedClient(BillingService, fallback);

  it("returns the fallback with no context, no client, or an unbound client", () => {
    expect(of()).toBe(fallback);
    expect(of({})).toBe(fallback);
    expect(of({ client: new QueryClient() })).toBe(fallback);
  });

  it("returns the scope's client for the scope's QueryClient", () => {
    const { scope } = fixtureScope("fixture");
    expect(of({ client: scope.queryClient })).toBe(
      clientFor(BillingService, scope.transport),
    );
  });
});

describe("clientFor", () => {
  it("memoizes per transport and per service", () => {
    const a = namedTransport("a").transport;
    const b = namedTransport("b").transport;
    expect(clientFor(BillingService, a)).toBe(clientFor(BillingService, a));
    expect(clientFor(BillingService, a)).not.toBe(clientFor(BillingService, b));
    expect(clientFor(TelemetryService, a)).not.toBe(
      clientFor(BillingService, a) as unknown,
    );
  });
});

describe("createTransportScope", () => {
  it("binds the QueryClient to the transport, defaults readOnly to false and makes a QueryClient when given none", () => {
    const { transport } = namedTransport("x");
    const scope = createTransportScope({ id: "x", transport });
    expect(scope.queryClient).toBeInstanceOf(QueryClient);
    expect(transportOf(scope.queryClient)).toBe(transport);
    expect(scope.readOnly).toBe(false);
    expect(
      createTransportScope({ id: "y", transport, readOnly: true }).readOnly,
    ).toBe(true);
  });

  it("refuses a QueryClient already bound to another transport", () => {
    const queryClient = new QueryClient();
    const one = namedTransport("one").transport;
    createTransportScope({ id: "one", transport: one, queryClient });
    // The same binding again is harmless.
    expect(() =>
      createTransportScope({ id: "again", transport: one, queryClient }),
    ).not.toThrow();
    expect(() =>
      createTransportScope({
        id: "two",
        transport: namedTransport("two").transport,
        queryClient,
      }),
    ).toThrow(/already bound to another transport/);
    expect(transportOf(queryClient)).toBe(one);
  });
});

describe("the scope hooks", () => {
  function Probe({
    onRender,
  }: {
    onRender: (v: {
      scope: TransportScope | null;
      transport: Transport;
      client: unknown;
    }) => void;
  }): ReactElement {
    onRender({
      scope: useTransportScope(),
      transport: useTransport(live.transport),
      client: useClient(BillingService, clientOf()),
    });
    return <span />;
  }

  it("read the nearest scope, and the fallback outside any", async () => {
    const outer = fixtureScope("outer").scope;
    const inner = fixtureScope("inner").scope;
    const seen: Record<
      string,
      { scope: unknown; transport: unknown; client: unknown }
    > = {};
    await render(
      <>
        <Probe onRender={(v) => (seen.none = v)} />
        <TransportScopeProvider scope={outer}>
          <Probe onRender={(v) => (seen.outer = v)} />
          <TransportScopeProvider scope={inner}>
            <Probe onRender={(v) => (seen.inner = v)} />
          </TransportScopeProvider>
        </TransportScopeProvider>
      </>,
    );
    expect(seen.none).toEqual({
      scope: null,
      transport: live.transport,
      client: clientOf(),
    });
    expect(seen.outer?.scope).toBe(outer);
    expect(seen.outer?.transport).toBe(outer.transport);
    expect(seen.outer?.client).toBe(clientFor(BillingService, outer.transport));
    expect(seen.inner?.scope).toBe(inner);
    expect(seen.inner?.client).toBe(clientFor(BillingService, inner.transport));
  });
});

describe("a hook file generated before the context parameter", () => {
  it("still compiles and calls the module client, scope or not", async () => {
    // The three-parameter call every generated file before this runtime
    // declares. It must stay assignable to ServiceCall (this file is
    // typechecked) and keep working: it ignores the context.
    const client = clientFor(BillingService, live.transport);
    const old = createQueryOptions<
      typeof GetInvoiceRequestSchema,
      GetInvoiceResponse
    >(getInvoiceKey, (req, signal, options) =>
      client.getInvoice(req, { ...options, signal }),
    );
    // And the two-parameter one from before callOptions.
    const older = createMutationCall<
      typeof GetInvoiceRequestSchema,
      GetInvoiceResponse
    >(keys.all, (req, signal) => client.getInvoice(req, { signal }));
    const { scope } = fixtureScope("fixture");
    const res = await scope.queryClient.fetchQuery(old({ id: "o_1" }));
    expect(res.invoice?.customerEmail).toBe("live");
    const written = await older(scope.queryClient)({ id: "o_2" });
    expect(written.invoice?.customerEmail).toBe("live");
  });
});
