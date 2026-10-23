// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The query-key contract behind the generated service hooks.
//
// WHAT IS TESTED HERE, AND WHY IT IS THE KEYS. Cache keys are the part of this
// module that is a COMPATIBILITY promise rather than an implementation
// detail: an app that already calls
// `invalidateQueries({ queryKey: orderServiceKeys.order })` must keep matching
// the keys its queries were registered under, or a version bump silently stops
// refetching after every mutation. So the tuple shapes are pinned byte for
// byte against what forge previously inlined into each generated file.
//
// The HOOKS themselves (createQueryHook / createMutationHook) need a React
// renderer, which this package deliberately does not carry — no jsdom, no
// @testing-library. They are covered where they actually run: the scaffolded
// `<svc>-service-hooks.test.tsx` renders every generated hook against the mock
// transport in a real Next.js project, which is a stronger test than a stub
// renderer here would be.
import {
  create,
  createFileRegistry,
  type DescMessage,
} from "@bufbuild/protobuf";
import {
  FileDescriptorProtoSchema,
  type DescriptorProto,
} from "@bufbuild/protobuf/wkt";
import { describe, expect, it, vi } from "vitest";

import {
  createMutationHook,
  createQueryHook,
  requestKey,
  serviceKeys,
} from "./service-hooks.js";

// React Query is stubbed rather than rendered. The properties under test in
// the hook factories are about the OPTIONS OBJECT they hand to useQuery /
// useMutation — which key, which queryFn, and (the load-bearing one) that a
// caller's onSuccess is composed with the invalidation rather than replacing
// it. Those are decidable from the object itself, with no renderer. Capturing
// it here also lets the assertions be exact where a rendered test could only
// observe the effect.
//
// The hooks are additionally exercised for real, end to end, by the
// scaffolded `<svc>-service-hooks.test.tsx` in every generated project.
const captured: {
  query?: Record<string, unknown>;
  mutation?: Record<string, unknown>;
  invalidated: unknown[];
} = { invalidated: [] };

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: Record<string, unknown>) => {
    captured.query = options;
    return { __kind: "query" };
  },
  useMutation: (options: Record<string, unknown>) => {
    captured.mutation = options;
    return { __kind: "mutation" };
  },
  useQueryClient: () => ({
    invalidateQueries: (args: { queryKey: unknown }) => {
      captured.invalidated.push(args.queryKey);
    },
  }),
}));

const PACKAGE = "demo.v1";

const OPTIONAL = 1;
const STRING = 9;
const INT64 = 3;

/**
 * Compile real protobuf schemas in-memory, as the mock-transport suite does.
 * Using the actual protobuf-es runtime is the point: protojson's int64
 * handling is exactly what requestKey exists to survive, and a hand-rolled
 * stub would not reproduce it.
 */
function compileSchemas(messages: DescriptorProto[]): Map<string, DescMessage> {
  const fdp = create(FileDescriptorProtoSchema, {
    name: "service-hooks-test.proto",
    syntax: "proto3",
    package: PACKAGE,
    messageType: messages,
  });
  const registry = createFileRegistry(fdp, () => undefined);
  const out = new Map<string, DescMessage>();
  for (const message of messages) {
    const desc = registry.getMessage(`${PACKAGE}.${message.name}`);
    if (!desc) {
      throw new Error(`schema ${message.name} did not compile`);
    }
    out.set(message.name, desc);
  }
  return out;
}

/** Look one compiled schema up, failing loudly rather than yielding undefined. */
function schema(name: string): DescMessage {
  const desc = schemas.get(name);
  if (!desc) {
    throw new Error(`no compiled schema named ${name}`);
  }
  return desc;
}

const schemas = compileSchemas([
  {
    name: "GetOrderRequest",
    field: [
      { name: "id", number: 1, type: STRING, label: OPTIONAL, jsonName: "id" },
    ],
  },
  {
    name: "ListOrdersRequest",
    field: [
      {
        name: "page_size",
        number: 1,
        type: INT64,
        label: OPTIONAL,
        jsonName: "pageSize",
      },
    ],
  },
] as DescriptorProto[]);

const GetOrderRequestSchema = schema("GetOrderRequest");
const ListOrdersRequestSchema = schema("ListOrdersRequest");

describe("requestKey", () => {
  it("survives an int64 field, which JSON.stringify cannot", () => {
    // React Query's default hasher is JSON.stringify, and protobuf-es
    // represents int64 as a bigint — stringifying the raw message THROWS
    // ("Do not know how to serialize a BigInt") and takes the cache with it.
    // Projecting through protojson is what makes the key hashable at all.
    const raw = create(ListOrdersRequestSchema, { pageSize: 25n });
    expect(() => JSON.stringify(raw)).toThrow();

    const key = requestKey(ListOrdersRequestSchema, { pageSize: 25n });
    expect(() => JSON.stringify(key)).not.toThrow();
    expect(key).toEqual({ pageSize: "25" });
  });

  it("normalises equivalent representations into ONE cache entry", () => {
    // { pageSize: 25 } and { pageSize: "25" } are the same request. Hashing
    // the raw init object would split them across two cache entries and
    // double every fetch; protojson collapses them.
    expect(requestKey(ListOrdersRequestSchema, { pageSize: 25n })).toEqual(
      requestKey(ListOrdersRequestSchema, { pageSize: "25" }),
    );
  });

  it("fills proto3 defaults so an empty request hashes stably", () => {
    expect(requestKey(GetOrderRequestSchema, {})).toEqual(
      requestKey(GetOrderRequestSchema, { id: "" }),
    );
  });
});

describe("serviceKeys", () => {
  const keys = serviceKeys("orderService");

  it("scopes the whole service", () => {
    expect(keys.all).toEqual(["orderService"]);
  });

  it("scopes one entity — the tuple mutations invalidate", () => {
    expect(keys.entity("order")).toEqual(["orderService", "order"]);
  });

  it("builds a per-request key as [service, entity, method, protojson(req)]", () => {
    // This exact tuple is what forge used to inline. It is pinned because a
    // change to the order or contents would orphan every cache entry an
    // already-shipped app holds.
    const getOrder = keys.query("getOrder", GetOrderRequestSchema, "order");
    expect(getOrder({ id: "abc" })).toEqual([
      "orderService",
      "order",
      "getOrder",
      { id: "abc" },
    ]);
  });

  it("omits the entity segment for a non-CRUD RPC", () => {
    const ping = keys.query("pingOrders", GetOrderRequestSchema);
    expect(ping({ id: "abc" })).toEqual([
      "orderService",
      "pingOrders",
      { id: "abc" },
    ]);
  });

  it("nests each key under the broader scopes React Query matches by prefix", () => {
    // invalidateQueries matches on key PREFIX, so entity-scoped invalidation
    // only works if the method key literally begins with the entity key,
    // which in turn begins with the service key. This is the property that
    // makes `invalidateQueries({ queryKey: keys.entity("order") })` refetch
    // getOrder and listOrders and nothing else.
    const methodKey = keys.query(
      "getOrder",
      GetOrderRequestSchema,
      "order",
    )({
      id: "abc",
    });
    expect(methodKey.slice(0, 1)).toEqual([...keys.all]);
    expect(methodKey.slice(0, 2)).toEqual([...keys.entity("order")]);
  });

  it("separates two entities on the same service", () => {
    const orderKey = keys.query(
      "getOrder",
      GetOrderRequestSchema,
      "order",
    )({
      id: "a",
    });
    const lineKey = keys.query(
      "getLine",
      GetOrderRequestSchema,
      "line",
    )({
      id: "a",
    });
    expect(orderKey.slice(0, 2)).not.toEqual(lineKey.slice(0, 2));
  });

  it("gives two different requests to the same RPC different keys", () => {
    const getOrder = keys.query("getOrder", GetOrderRequestSchema, "order");
    expect(getOrder({ id: "a" })).not.toEqual(getOrder({ id: "b" }));
  });
});

describe("createQueryHook", () => {
  const keys = serviceKeys("orderService");

  it("registers the query under the factory's key and calls the RPC", async () => {
    const call = vi.fn(async () => ({ id: "abc" }));
    const useGetOrder = createQueryHook(
      keys.query("getOrder", GetOrderRequestSchema, "order"),
      call,
    );

    useGetOrder({ id: "abc" });

    expect(captured.query?.queryKey).toEqual([
      "orderService",
      "order",
      "getOrder",
      { id: "abc" },
    ]);
    await (captured.query?.queryFn as () => Promise<unknown>)();
    expect(call).toHaveBeenCalledWith({ id: "abc" });
  });

  it("lets caller options through — nothing is composed on a read", () => {
    const useGetOrder = createQueryHook(
      keys.query("getOrder", GetOrderRequestSchema, "order"),
      async () => ({}),
    );
    useGetOrder({ id: "abc" }, { enabled: false, staleTime: 5_000 });
    expect(captured.query?.enabled).toBe(false);
    expect(captured.query?.staleTime).toBe(5_000);
  });
});

describe("createMutationHook", () => {
  const keys = serviceKeys("orderService");

  it("invalidates the entity scope on success", () => {
    captured.invalidated = [];
    const useCreateOrder = createMutationHook(
      keys.entity("order"),
      async () => ({ id: "new" }),
    );

    useCreateOrder();
    (captured.mutation?.onSuccess as (...a: unknown[]) => void)(
      { id: "new" },
      {},
      undefined,
    );

    expect(captured.invalidated).toEqual([["orderService", "order"]]);
  });

  it("composes the caller's onSuccess AFTER invalidation, never replacing it", () => {
    // The bug this guards: spreading `...options` over a default onSuccess
    // lets a caller that passes onSuccess (to show a toast, to redirect)
    // silently disable the cache refresh, so the list they navigate back to
    // is stale. Destructuring onSuccess out and re-invoking it is what makes
    // a caller's callback ADDITIVE.
    captured.invalidated = [];
    const order: string[] = [];
    const useCreateOrder = createMutationHook(
      keys.entity("order"),
      async () => ({ id: "new" }),
    );

    useCreateOrder({
      onSuccess: () => {
        order.push("caller");
      },
    });
    (captured.mutation?.onSuccess as (...a: unknown[]) => void)(
      { id: "new" },
      {},
      undefined,
    );

    expect(captured.invalidated).toEqual([["orderService", "order"]]);
    expect(order).toEqual(["caller"]);
    // And the raw options object is never spread in — only the rest.
    expect(captured.mutation).not.toHaveProperty("options");
  });

  it("passes the caller's other options through untouched", () => {
    const useCreateOrder = createMutationHook(
      keys.entity("order"),
      async () => ({}),
    );
    useCreateOrder({ retry: 3, meta: { silenceErrorToast: true } });
    expect(captured.mutation?.retry).toBe(3);
    expect(captured.mutation?.meta).toEqual({ silenceErrorToast: true });
  });

  it("falls back to the whole-service scope for a non-CRUD mutation", () => {
    // A bespoke RPC may touch anything, so over-invalidating is the safe
    // default — the generated file passes keys.all for these.
    captured.invalidated = [];
    const useSendReport = createMutationHook(keys.all, async () => ({}));
    useSendReport();
    (captured.mutation?.onSuccess as (...a: unknown[]) => void)(
      {},
      {},
      undefined,
    );
    expect(captured.invalidated).toEqual([["orderService"]]);
  });

  it("calls the RPC through mutationFn", async () => {
    const call = vi.fn(async () => ({ id: "new" }));
    const useCreateOrder = createMutationHook(keys.entity("order"), call);
    useCreateOrder();
    await (captured.mutation?.mutationFn as (r: unknown) => Promise<unknown>)({
      id: "x",
    });
    expect(call).toHaveBeenCalledWith({ id: "x" });
  });
});
