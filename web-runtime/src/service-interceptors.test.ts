// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The per-service transport seam: forService, and buildRuntimeInterceptors'
// serviceInterceptors. Then the whole path it exists for, end to end: a
// generated read's callOptions carry a contextValue the call site derived
// from React Query's `meta`, and a per-service interceptor turns it into a
// header the server receives.
//
// The transport is Connect's own in-memory router, so the interceptors run
// exactly as they do over HTTP, and what the "server" sees is real.
import {
  create,
  createFileRegistry,
  type DescService,
} from "@bufbuild/protobuf";
import { FileDescriptorProtoSchema } from "@bufbuild/protobuf/wkt";
import {
  Code,
  ConnectError,
  createClient,
  createContextKey,
  createContextValues,
  createRouterTransport,
  type CallOptions,
  type Interceptor,
  type Transport,
} from "@connectrpc/connect";
import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";

import { ConnectClientError } from "./errors.js";
import { buildRuntimeInterceptors, forService } from "./interceptors.js";
import { createQueryOptions, serviceKeys } from "./service-hooks.js";

/** Two services with one unary rpc each, compiled in memory. */
function services(): { orders: DescService; billing: DescService } {
  const method = (name: string) => ({
    name,
    inputType: ".seam.v1.Req",
    outputType: ".seam.v1.Res",
  });
  const registry = createFileRegistry(
    create(FileDescriptorProtoSchema, {
      name: "seam_test.proto",
      package: "seam.v1",
      syntax: "proto3",
      messageType: [
        {
          name: "Req",
          field: [{ name: "id", number: 1, type: 9, label: 1, jsonName: "id" }],
        },
        { name: "Res" },
      ],
      service: [
        { name: "OrderService", method: [method("GetOrder")] },
        { name: "BillingService", method: [method("GetInvoice")] },
      ],
    }),
    () => undefined,
  );
  const orders = registry.getService("seam.v1.OrderService");
  const billing = registry.getService("seam.v1.BillingService");
  if (!orders || !billing) throw new Error("test services did not register");
  return { orders, billing };
}

const { orders, billing } = services();

type Client = {
  getOrder(req: { id?: string }, opts?: CallOptions): Promise<unknown>;
  getInvoice(req: { id?: string }, opts?: CallOptions): Promise<unknown>;
};

/**
 * A transport whose handlers record the request headers each rpc received,
 * and clients of both services over it.
 */
function harness(interceptors: Interceptor[]): {
  seen: Array<{ rpc: string; headers: Headers }>;
  orderClient: Client;
  billingClient: Client;
} {
  const seen: Array<{ rpc: string; headers: Headers }> = [];
  const transport: Transport = createRouterTransport(
    (router) => {
      router.service(orders, {
        getOrder: (_req: unknown, ctx: { requestHeader: Headers }) => {
          seen.push({ rpc: "GetOrder", headers: ctx.requestHeader });
          return {};
        },
      } as never);
      router.service(billing, {
        getInvoice: (_req: unknown, ctx: { requestHeader: Headers }) => {
          seen.push({ rpc: "GetInvoice", headers: ctx.requestHeader });
          return {};
        },
      } as never);
    },
    { transport: { interceptors } },
  );
  return {
    seen,
    orderClient: createClient(orders, transport) as unknown as Client,
    billingClient: createClient(billing, transport) as unknown as Client,
  };
}

/** An interceptor that stamps one header, recording the order it ran in. */
function stamp(name: string, order?: string[]): Interceptor {
  return (next) => (req) => {
    order?.push(name);
    req.header.set(`x-${name}`, "1");
    return next(req);
  };
}

describe("forService", () => {
  it("runs its interceptors, first outermost, only for the named service", async () => {
    const order: string[] = [];
    const { seen, orderClient, billingClient } = harness([
      forService("seam.v1.OrderService", stamp("a", order), stamp("b", order)),
    ]);

    await orderClient.getOrder({});
    await billingClient.getInvoice({});

    expect(order).toEqual(["a", "b"]);
    expect(seen[0]?.headers.get("x-a")).toBe("1");
    expect(seen[0]?.headers.get("x-b")).toBe("1");
    // The other service's call passed straight through.
    expect(seen[1]?.headers.get("x-a")).toBeNull();
    expect(seen[1]?.headers.get("x-b")).toBeNull();
  });

  it("takes descriptors and lists of services", async () => {
    const { seen, orderClient, billingClient } = harness([
      forService(orders, stamp("one")),
      forService([orders, "seam.v1.BillingService"], stamp("both")),
    ]);

    await orderClient.getOrder({});
    await billingClient.getInvoice({});

    expect(
      seen.map((s) => [s.headers.get("x-one"), s.headers.get("x-both")]),
    ).toEqual([
      ["1", "1"],
      [null, "1"],
    ]);
  });
});

describe("buildRuntimeInterceptors({ serviceInterceptors })", () => {
  it("adds nothing when unset or empty", () => {
    const base = buildRuntimeInterceptors();
    expect(buildRuntimeInterceptors({ serviceInterceptors: {} })).toHaveLength(
      base.length,
    );
    expect(
      buildRuntimeInterceptors({
        serviceInterceptors: { "seam.v1.OrderService": [] },
      }),
    ).toHaveLength(base.length);
  });

  it("scopes each list to its service, inside error-normalize", async () => {
    const quotaGuard: Interceptor = () => () => {
      throw new ConnectError("over quota", Code.ResourceExhausted);
    };
    const errors: ConnectClientError[] = [];
    const { seen, orderClient, billingClient } = harness(
      buildRuntimeInterceptors({
        rpcSpans: false,
        onError: (err) => errors.push(err),
        serviceInterceptors: {
          "seam.v1.OrderService": [stamp("orders")],
          "seam.v1.BillingService": [quotaGuard],
        },
      }),
    );

    await orderClient.getOrder({});
    expect(seen[0]?.headers.get("x-orders")).toBe("1");

    // A per-service stage's failure reaches the caller typed, and the app's
    // onError sees it: it runs inside error-normalize.
    const failure = await billingClient.getInvoice({}).catch((e: unknown) => e);
    expect(failure).toBeInstanceOf(ConnectClientError);
    expect((failure as ConnectClientError).code).toBe(Code.ResourceExhausted);
    expect(errors).toHaveLength(1);
    // The guard refused the call: the server never saw it.
    expect(seen.map((s) => s.rpc)).toEqual(["GetOrder"]);
  });
});

describe("callOptions through a generated read to a per-service interceptor", () => {
  it("turns a read's meta into a header for that service's calls only", async () => {
    // What reliant's observability client needs: polls marked by the call
    // site (meta.poll), and ONE service's interceptor turning the mark into
    // a header — no wrapper around the generated hooks.
    const pollKey = createContextKey(false, { description: "poll" });
    const pollHeader: Interceptor = (next) => (req) => {
      if (req.contextValues.get(pollKey)) req.header.set("Forge-Poll", "1");
      return next(req);
    };
    const { seen, orderClient } = harness(
      buildRuntimeInterceptors({
        rpcSpans: false,
        serviceInterceptors: { "seam.v1.OrderService": [pollHeader] },
      }),
    );
    // What a generated file emits for one read.
    const GetOrderRequestSchema = orders.methods[0]!.input;
    const getOrder = createQueryOptions<typeof GetOrderRequestSchema, unknown>(
      serviceKeys("orderService").query("getOrder", GetOrderRequestSchema),
      (req, signal, callOptions) =>
        orderClient.getOrder(req as { id?: string }, {
          ...callOptions,
          signal,
        }),
    );
    const qc = new QueryClient();

    await qc.fetchQuery(
      getOrder(
        { id: "a" },
        {
          meta: { poll: true },
          callOptions: ({ meta }) => ({
            contextValues: createContextValues().set(
              pollKey,
              meta?.poll === true,
            ),
          }),
        },
      ),
    );
    await qc.fetchQuery(getOrder({ id: "b" }));

    expect(seen.map((s) => s.headers.get("Forge-Poll"))).toEqual(["1", null]);
    qc.clear();
  });
});
