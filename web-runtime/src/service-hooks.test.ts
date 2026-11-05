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
  type Message,
} from "@bufbuild/protobuf";
import type { GenMessage } from "@bufbuild/protobuf/codegenv2";
import {
  FileDescriptorProtoSchema,
  type DescriptorProto,
} from "@bufbuild/protobuf/wkt";
import { QueryClient, useQueries } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

import {
  createInfiniteQueryHook,
  createInfiniteQueryOptions,
  createMutationHook,
  createQueryHook,
  createQueryOptions,
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
  infinite?: Record<string, unknown>;
  mutation?: Record<string, unknown>;
  invalidated: unknown[];
} = { invalidated: [] };

//
// Only the React HOOKS are stubbed. The rest of React Query is real —
// queryOptions / infiniteQueryOptions, and the QueryClient the query-options
// builders are exercised against — so "a fetchQuery outside React lands on
// the entry the hook reads" is asserted on a real cache, not a stub.
vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQueries: (options: { queries: unknown[] }) =>
    options.queries.map(() => ({ __kind: "query" })),
  useQuery: (options: Record<string, unknown>) => {
    captured.query = options;
    return { __kind: "query" };
  },
  useInfiniteQuery: (options: Record<string, unknown>) => {
    captured.infinite = options;
    return { __kind: "infinite" };
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
      {
        name: "page_token",
        number: 2,
        type: STRING,
        label: OPTIONAL,
        jsonName: "pageToken",
      },
    ],
  },
] as DescriptorProto[]);

const GetOrderRequestSchema = schema("GetOrderRequest");
const ListOrdersRequestSchema = schema("ListOrdersRequest");

// The same schema typed the way protoc-gen-es types it, so the type-level
// assertions below see the concrete request fields a generated file does.
type ListOrdersRequest = Message<"demo.v1.ListOrdersRequest"> & {
  pageSize: bigint;
  pageToken: string;
};
const TypedListOrdersRequestSchema =
  ListOrdersRequestSchema as GenMessage<ListOrdersRequest>;

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

describe("createQueryOptions", () => {
  const keys = serviceKeys("orderService");
  type Order = { id: string; total: number };

  it("keys the read with the factory's key and fetches through the RPC", async () => {
    const call = vi.fn(async () => ({ id: "abc" }));
    const getOrder = createQueryOptions(
      keys.query("getOrder", GetOrderRequestSchema, "order"),
      call,
    );

    const opts = getOrder({ id: "abc" });

    expect(opts.queryKey).toEqual([
      "orderService",
      "order",
      "getOrder",
      { id: "abc" },
    ]);
    const signal = new AbortController().signal;
    await (opts.queryFn as (ctx: { signal: AbortSignal }) => Promise<unknown>)({
      signal,
    });
    // The signal is React Query's: an unmounted or superseded query aborts
    // its request rather than landing it on an entry nobody reads.
    expect(call).toHaveBeenCalledWith({ id: "abc" }, signal);
  });

  it("passes caller options through but never lets them replace the key or the fetch", async () => {
    const call = vi.fn(async () => ({ id: "abc" }));
    const getOrder = createQueryOptions(
      keys.query("getOrder", GetOrderRequestSchema, "order"),
      call,
    );
    const opts = getOrder(
      { id: "abc" },
      { enabled: false, staleTime: 5_000, meta: { silenceErrorToast: true } },
    );
    expect(opts.enabled).toBe(false);
    expect(opts.staleTime).toBe(5_000);
    expect(opts.meta).toEqual({ silenceErrorToast: true });

    const hijacked = getOrder({ id: "abc" }, {
      queryKey: ["hijacked"],
      queryFn: async () => ({}),
    } as never);
    expect(hijacked.queryKey).toEqual(getOrder({ id: "abc" }).queryKey);
    await (hijacked.queryFn as (ctx: object) => Promise<unknown>)({});
    expect(call).toHaveBeenCalledTimes(1);
    // @ts-expect-error — the key is the read's own, not an option.
    void (() => getOrder({ id: "abc" }, { queryKey: ["x"] }));
  });

  it("fetchQuery, ensureQueryData and prefetchQuery outside React share the hook's cache entry", async () => {
    // The defect this replaces: code outside React (an imperative action, a
    // stream handler) hand-wrote queryClient.fetchQuery({ queryKey, queryFn })
    // by copying the generated hook's key and client call, and the copies
    // drifted. Now there is one definition, and the hook is useQuery over it.
    const call = vi.fn(async (): Promise<Order> => ({ id: "abc", total: 3 }));
    const getOrder = createQueryOptions(
      keys.query("getOrder", GetOrderRequestSchema, "order"),
      call,
    );
    const useGetOrder = createQueryHook(getOrder);
    const client = new QueryClient();

    const fetched = await client.fetchQuery(getOrder({ id: "abc" }));
    // Typed as the response, with no annotation at the call site.
    const total: number = fetched.total;
    expect(total).toBe(3);

    useGetOrder({ id: "abc" });
    const hookKey = captured.query?.queryKey as readonly unknown[];
    expect(hookKey).toEqual(getOrder({ id: "abc" }).queryKey);
    expect(client.getQueryData(hookKey)).toEqual({ id: "abc", total: 3 });

    // A fresh entry is reused: neither refetches.
    await client.ensureQueryData(getOrder({ id: "abc" }));
    await client.prefetchQuery({
      ...getOrder({ id: "abc" }),
      staleTime: 60_000,
    });
    expect(call).toHaveBeenCalledTimes(1);

    // The key is data-tagged: getQueryData / setQueryData are typed.
    const cached = client.getQueryData(getOrder({ id: "abc" }).queryKey);
    const cachedId: string | undefined = cached?.id;
    expect(cachedId).toBe("abc");
    // @ts-expect-error — the tag is the response, so a wrong field is rejected.
    void (() => cached?.nonexistent);
    client.clear();
  });

  it("feeds useQueries, typed per entry by its select", () => {
    const getOrder = createQueryOptions(
      keys.query("getOrder", GetOrderRequestSchema, "order"),
      async (): Promise<Order> => ({ id: "abc", total: 3 }),
    );
    const results = useQueries({
      queries: ["a", "b"].map((id) =>
        getOrder({ id }, { select: (r) => r.total }),
      ),
    });
    const first: number | undefined = results[0]?.data;
    void first;
    expect(results).toHaveLength(2);
  });
});

describe("createQueryHook", () => {
  const keys = serviceKeys("orderService");

  it("is useQuery over the read's query options, adding nothing", async () => {
    const call = vi.fn(async () => ({ id: "abc" }));
    const getOrder = createQueryOptions(
      keys.query("getOrder", GetOrderRequestSchema, "order"),
      call,
    );
    const useGetOrder = createQueryHook(getOrder);

    useGetOrder({ id: "abc" }, { staleTime: 1_000 });

    const { queryFn, ...hookRest } = captured.query ?? {};
    const { queryFn: _fn, ...optsRest } = getOrder(
      { id: "abc" },
      { staleTime: 1_000 },
    );
    expect(hookRest).toEqual(optsRest);
    const signal = new AbortController().signal;
    await (queryFn as (ctx: { signal: AbortSignal }) => Promise<unknown>)({
      signal,
    });
    expect(call).toHaveBeenCalledWith({ id: "abc" }, signal);
  });

  it("types data as what select returns, defaulting to the response", () => {
    // The bug this guards: options were typed as the response's own
    // UseQueryOptions, so `select` could not change the type and a call
    // site mapping proto to a view model had to useMemo over `data`
    // instead. TData is now the HOOK's parameter, inferred from select.
    // `npm run typecheck` compiles this file, so the assignments below are
    // the type-level half; the captured select is the runtime half.
    const useGetOrder = createQueryHook(
      createQueryOptions(
        keys.query("getOrder", GetOrderRequestSchema, "order"),
        async () => ({ id: "abc", total: 3 }),
      ),
    );

    const mapped = useGetOrder({ id: "abc" }, { select: (r) => r.total * 2 });
    const doubled: number | undefined = mapped.data;
    // @ts-expect-error — data is select's number, no longer the response.
    void (() => mapped.data?.id);
    void doubled;
    expect(
      (captured.query?.select as (r: { total: number }) => number)({
        total: 3,
      }),
    ).toBe(6);

    const plain = useGetOrder({ id: "abc" });
    const id: string | undefined = plain.data?.id;
    void id;
  });

  it("lets caller options through — nothing is composed on a read", () => {
    const useGetOrder = createQueryHook(
      createQueryOptions(
        keys.query("getOrder", GetOrderRequestSchema, "order"),
        async () => ({}),
      ),
    );
    useGetOrder({ id: "abc" }, { enabled: false, staleTime: 5_000 });
    expect(captured.query?.enabled).toBe(false);
    expect(captured.query?.staleTime).toBe(5_000);
  });
});

describe("serviceKeys.infinite", () => {
  const keys = serviceKeys("orderService");
  const listOrders = keys.infinite(
    "listOrders",
    ListOrdersRequestSchema,
    "order",
  );

  it('builds [service, entity, method, protojson(req), "infinite"]', () => {
    expect(listOrders({ pageSize: 20n })).toEqual([
      "orderService",
      "order",
      "listOrders",
      { pageSize: "20" },
      "infinite",
    ]);
  });

  it("nests under the service and entity scopes, so invalidation reaches it", () => {
    // The hand-written paged reads this replaces keyed themselves outside the
    // generated factory, so a mutation's entity invalidation never refetched
    // them. Prefix matching is the whole mechanism.
    const key = listOrders({ pageSize: 20n });
    expect(key.slice(0, 1)).toEqual([...keys.all]);
    expect(key.slice(0, 2)).toEqual([...keys.entity("order")]);
  });

  it("never shares an entry with the plain query of the same request", () => {
    // An infinite query caches { pages, pageParams }; a plain hook reading
    // that entry would get the wrong shape.
    const plain = keys.query("listOrders", ListOrdersRequestSchema, "order");
    expect(listOrders({ pageSize: 20n })).not.toEqual(plain({ pageSize: 20n }));
  });

  it("ignores a page token, so every page of a listing is one entry", () => {
    expect(listOrders({ pageSize: 20n, pageToken: "p2" } as never)).toEqual(
      listOrders({ pageSize: 20n }),
    );
  });

  it("omits the entity segment for a non-CRUD rpc", () => {
    expect(keys.infinite("searchOrders", ListOrdersRequestSchema)({})).toEqual([
      "orderService",
      "searchOrders",
      {},
      "infinite",
    ]);
  });
});

describe("serviceKeys.query(...).all — the rpc scope", () => {
  const keys = serviceKeys("orderService");

  it("is [service, entity?, method]", () => {
    expect(keys.query("getOrder", GetOrderRequestSchema, "order").all).toEqual([
      "orderService",
      "order",
      "getOrder",
    ]);
    expect(keys.query("pingOrders", GetOrderRequestSchema).all).toEqual([
      "orderService",
      "pingOrders",
    ]);
  });

  // The property `.all` exists for, on a real cache: invalidating it reaches
  // every entry the rpc caches under and nothing else. Each case seeds the
  // rpc's own entries — two requests, the plain query and the infinite read
  // of the same request — next to the entries most likely to be caught by
  // mistake: a sibling rpc on the same entity, a method whose name the rpc's
  // name is a string prefix of, the same method name unscoped, and the same
  // method on another service.
  function seededCache(entityScope: string | undefined) {
    const client = new QueryClient();
    const own = keys.query("listOrders", ListOrdersRequestSchema, entityScope);
    const ownInfinite = keys.infinite(
      "listOrders",
      ListOrdersRequestSchema,
      entityScope,
    );
    const ownKeys = [
      own({ pageSize: 10n }),
      own({ pageSize: 20n }),
      ownInfinite({ pageSize: 10n }),
    ];
    const otherKeys = [
      keys.query("getOrder", GetOrderRequestSchema, entityScope)({ id: "a" }),
      keys.query(
        "listOrdersArchived",
        ListOrdersRequestSchema,
        entityScope,
      )({}),
      keys.infinite(
        "listOrdersArchived",
        ListOrdersRequestSchema,
        entityScope,
      )({}),
      entityScope === undefined
        ? keys.query("listOrders", ListOrdersRequestSchema, "order")({})
        : keys.query("listOrders", ListOrdersRequestSchema)({}),
      serviceKeys("archiveService").query(
        "listOrders",
        ListOrdersRequestSchema,
        entityScope,
      )({}),
    ];
    for (const key of [...ownKeys, ...otherKeys]) {
      client.setQueryData(key, { seeded: true });
    }
    return { client, all: own.all, ownKeys, otherKeys };
  }

  for (const [label, entityScope] of [
    ["with a query-scope (entity at index 1)", "order"],
    ["without one", undefined],
  ] as const) {
    it(`invalidateQueries reaches every request of the rpc and nothing else, ${label}`, async () => {
      const { client, all, ownKeys, otherKeys } = seededCache(entityScope);
      await client.invalidateQueries({ queryKey: all });
      for (const key of ownKeys) {
        expect(
          client.getQueryState(key)?.isInvalidated,
          JSON.stringify(key),
        ).toBe(true);
      }
      for (const key of otherKeys) {
        expect(
          client.getQueryState(key)?.isInvalidated,
          JSON.stringify(key),
        ).toBe(false);
      }
      expect(client.getQueryCache().findAll({ queryKey: all })).toHaveLength(
        ownKeys.length,
      );
      client.clear();
    });
  }

  it("setQueriesData patches exactly the rpc's entries", () => {
    const { client, all, ownKeys, otherKeys } = seededCache("order");
    client.setQueriesData({ queryKey: all }, () => ({ patched: true }));
    for (const key of ownKeys) {
      expect(client.getQueryData(key)).toEqual({ patched: true });
    }
    for (const key of otherKeys) {
      expect(client.getQueryData(key)).toEqual({ seeded: true });
    }
    client.clear();
  });

  it("is what the generated query options key under", () => {
    // createQueryOptions keeps the factory's function, `.all` included, so
    // a generated `<svc>Keys.<rpc>` handed to it stays the scope of what
    // the hook caches.
    const getOrderKey = keys.query("getOrder", GetOrderRequestSchema, "order");
    const getOrder = createQueryOptions(getOrderKey, async () => ({}));
    expect(getOrder({ id: "abc" }).queryKey.slice(0, 3)).toEqual([
      ...getOrderKey.all,
    ]);
  });
});

describe("createInfiniteQueryOptions + createInfiniteQueryHook", () => {
  const keys = serviceKeys("orderService");
  type Page = { orders: string[]; nextPageToken: string };
  // The generated file's shape: the hook is useInfiniteQuery over the read's
  // infinite query options, so what the hook hands useInfiniteQuery
  // (captured.infinite) IS the options object.
  const pagedHook = (
    keyFor: Parameters<
      typeof createInfiniteQueryOptions<
        typeof TypedListOrdersRequestSchema,
        Page
      >
    >[0],
    call: () => Promise<Page>,
  ) =>
    createInfiniteQueryHook(
      createInfiniteQueryOptions<typeof TypedListOrdersRequestSchema, Page>(
        keyFor,
        call,
      ),
    );

  it("fetchInfiniteQuery outside React shares the hook's cache entry, typed as InfiniteData", async () => {
    const call = vi.fn(
      async (): Promise<Page> => ({
        orders: ["o1"],
        nextPageToken: "",
      }),
    );
    const listOrdersInfinite = createInfiniteQueryOptions<
      typeof TypedListOrdersRequestSchema,
      Page
    >(keys.infinite("listOrders", TypedListOrdersRequestSchema, "order"), call);
    const useListOrdersInfinite = createInfiniteQueryHook(listOrdersInfinite);
    const client = new QueryClient();

    const data = await client.fetchInfiniteQuery(
      listOrdersInfinite({ pageSize: 20n }),
    );
    const firstOrders: string[] | undefined = data.pages[0]?.orders;
    expect(firstOrders).toEqual(["o1"]);

    useListOrdersInfinite({ pageSize: 20n });
    const hookKey = captured.infinite?.queryKey as readonly unknown[];
    expect(hookKey).toEqual(listOrdersInfinite({ pageSize: 20n }).queryKey);
    expect(client.getQueryData(hookKey)).toEqual(data);

    await client.ensureInfiniteQueryData(listOrdersInfinite({ pageSize: 20n }));
    await client.prefetchInfiniteQuery({
      ...listOrdersInfinite({ pageSize: 20n }),
      staleTime: 60_000,
    });
    expect(call).toHaveBeenCalledTimes(1);

    const cached = client.getQueryData(
      listOrdersInfinite({ pageSize: 20n }).queryKey,
    );
    const pages: Page[] | undefined = cached?.pages;
    expect(pages).toHaveLength(1);
    client.clear();
  });

  it("fetches the first page without a token and each next one with the last token", async () => {
    const call = vi.fn(
      async (): Promise<Page> => ({
        orders: [],
        nextPageToken: "",
      }),
    );
    const useListOrdersInfinite = pagedHook(
      keys.infinite("listOrders", TypedListOrdersRequestSchema, "order"),
      call,
    );

    useListOrdersInfinite({ pageSize: 20n });

    expect(captured.infinite?.queryKey).toEqual([
      "orderService",
      "order",
      "listOrders",
      { pageSize: "20" },
      "infinite",
    ]);
    expect(captured.infinite?.initialPageParam).toBe("");
    const queryFn = captured.infinite?.queryFn as (ctx: {
      pageParam: string;
      signal: AbortSignal;
    }) => Promise<unknown>;
    const signal = new AbortController().signal;
    await queryFn({ pageParam: "", signal });
    // No token on the first page: an `optional string page_token` stays unset.
    expect(call).toHaveBeenLastCalledWith({ pageSize: 20n }, signal);
    await queryFn({ pageParam: "p2", signal });
    expect(call).toHaveBeenLastCalledWith(
      { pageSize: 20n, pageToken: "p2" },
      signal,
    );
  });

  it("ends the listing on an empty next_page_token", () => {
    const useListOrdersInfinite = pagedHook(
      keys.infinite("listOrders", TypedListOrdersRequestSchema, "order"),
      async (): Promise<Page> => ({ orders: [], nextPageToken: "" }),
    );
    useListOrdersInfinite({});
    const next = captured.infinite?.getNextPageParam as (last: Page) => unknown;
    expect(next({ orders: [], nextPageToken: "p2" })).toBe("p2");
    expect(next({ orders: [], nextPageToken: "" })).toBeUndefined();
  });

  it("passes caller options through but never lets them replace the key or the fetch", () => {
    const useListOrdersInfinite = pagedHook(
      keys.infinite("listOrders", TypedListOrdersRequestSchema, "order"),
      async (): Promise<Page> => ({ orders: [], nextPageToken: "" }),
    );
    const getNextPageParam = () => undefined;
    useListOrdersInfinite(
      {},
      { staleTime: 30_000, enabled: false, getNextPageParam },
    );
    expect(captured.infinite?.staleTime).toBe(30_000);
    expect(captured.infinite?.enabled).toBe(false);
    expect(captured.infinite?.getNextPageParam).toBe(getNextPageParam);

    useListOrdersInfinite({}, { queryKey: ["hijacked"] } as never);
    expect(captured.infinite?.queryKey).toEqual([
      "orderService",
      "order",
      "listOrders",
      {},
      "infinite",
    ]);
  });

  it("types data as what select returns, defaulting to InfiniteData", () => {
    // Type-level half under `npm run typecheck`; flattening the pages is the
    // select every paged list writes.
    const useListOrdersInfinite = pagedHook(
      keys.infinite("listOrders", TypedListOrdersRequestSchema, "order"),
      async (): Promise<Page> => ({ orders: [], nextPageToken: "" }),
    );
    const flat = useListOrdersInfinite(
      {},
      { select: (d) => d.pages.flatMap((p) => p.orders) },
    );
    const orders: string[] | undefined = flat.data;
    void orders;
    const paged = useListOrdersInfinite({});
    const pages: Page[] | undefined = paged.data?.pages;
    void pages;
    // @ts-expect-error — the request has no pageToken: the hook owns it.
    void (() => useListOrdersInfinite({ pageToken: "x" }));
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

  it("types onMutate's context through to onError, so a rollback needs no cast", () => {
    // The bug this guards: the hook's options pinned React Query's TContext
    // to `unknown`, so an optimistic update had to narrow `context` by hand
    // before it could roll back. TContext is now the HOOK's type parameter,
    // inferred from onMutate. `npm run typecheck` compiles this file, so the
    // assignments and the @ts-expect-error below are the type-level half;
    // the calls are the runtime half (both callbacks pass through intact).
    const rolledBack: string[] = [];
    const useApprove = createMutationHook(
      keys.entity("order"),
      async () => ({}),
    );

    const result = useApprove({
      onMutate: (req) => ({ rollback: () => rolledBack.push(String(req)) }),
      onError: (_err, _req, context) => {
        // No cast: context is { rollback } | undefined.
        context?.rollback();
        // Type-level only, never called: a field onMutate never returned is
        // not on it — which also proves context is not `any`.
        // @ts-expect-error — nonexistent is not on { rollback }.
        void (() => context?.nonexistent());
      },
    });
    expect(result).toEqual({ __kind: "mutation" });

    const onMutate = captured.mutation?.onMutate as (r: unknown) => unknown;
    const onError = captured.mutation?.onError as (...a: unknown[]) => void;
    onError(new Error("boom"), "req-1", onMutate("req-1"));
    expect(rolledBack).toEqual(["req-1"]);
  });

  it("defaults the context to unknown when no onMutate is passed", () => {
    const useCreateOrder = createMutationHook(
      keys.entity("order"),
      async () => ({}),
    );
    useCreateOrder({
      onError: (_err, _req, context) => {
        // @ts-expect-error — without onMutate there is no context shape to read.
        context.rollback();
      },
    });
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
