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
  createMutationCall,
  createMutationHook,
  createQueryHook,
  createQueryOptions,
  createServiceMutations,
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

  it("skips the invalidation on invalidate: false, and still calls onSuccess", () => {
    // The report this guards: a caller wrote the response into the cache in
    // onSuccess, and the default invalidation refetched over it (41 → 42 →
    // back to 41 against a static mock). onSuccess is additive by design, so
    // opting out of the refresh is its own, explicit option.
    captured.invalidated = [];
    const seen: unknown[] = [];
    const useClick = createMutationHook(keys.all, async () => ({ count: 42 }));

    useClick({
      invalidate: false,
      onSuccess: (res) => {
        seen.push(res);
      },
    });
    (captured.mutation?.onSuccess as (...a: unknown[]) => void)(
      { count: 42 },
      {},
      undefined,
    );

    expect(captured.invalidated).toEqual([]);
    expect(seen).toEqual([{ count: 42 }]);
  });

  it("replaces the invalidation with an invalidate function, run before onSuccess", () => {
    captured.invalidated = [];
    const order: string[] = [];
    const useCreateOrder = createMutationHook(
      keys.entity("order"),
      async () => ({ id: "new" }),
    );

    useCreateOrder({
      invalidate: (qc) => {
        order.push("invalidate");
        void qc.invalidateQueries({ queryKey: keys.entity("invoice") });
      },
      onSuccess: () => {
        order.push("caller");
      },
    });
    (captured.mutation?.onSuccess as (...a: unknown[]) => void)(
      { id: "new" },
      {},
      undefined,
    );

    // Only the caller's scope: the generated entity scope is replaced.
    expect(captured.invalidated).toEqual([["orderService", "invoice"]]);
    expect(order).toEqual(["invalidate", "caller"]);
  });

  it("keeps invalidate out of the options React Query receives", () => {
    const useCreateOrder = createMutationHook(
      keys.entity("order"),
      async () => ({}),
    );
    useCreateOrder({ invalidate: false });
    expect(captured.mutation).not.toHaveProperty("invalidate");
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

describe("createMutationCall + createServiceMutations", () => {
  const keys = serviceKeys("orderService");
  type Order = { id: string };

  // A real QueryClient: "a write outside React refetches the reads the hook's
  // write would" is asserted on a real cache, not on a stubbed invalidate.
  function seeded() {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const orderKey = keys.query(
      "getOrder",
      GetOrderRequestSchema,
      "order",
    )({
      id: "a",
    });
    const otherKey = keys.query(
      "getLine",
      GetOrderRequestSchema,
      "line",
    )({
      id: "a",
    });
    client.setQueryData(orderKey, { id: "a" });
    client.setQueryData(otherKey, { id: "a" });
    return { client, orderKey, otherKey };
  }

  it("calls the rpc with the request and signal, and resolves with its response", async () => {
    const call = vi.fn(async () => ({ id: "new" }));
    const mutations = createServiceMutations({
      createOrder: createMutationCall<typeof GetOrderRequestSchema, Order>(
        keys.entity("order"),
        call,
      ),
    });
    const signal = new AbortController().signal;

    const got = await mutations(new QueryClient()).createOrder(
      { id: "x" },
      { signal },
    );

    expect(got).toEqual({ id: "new" });
    expect(call).toHaveBeenCalledWith({ id: "x" }, signal);
  });

  it("invalidates the scope it was given on success, and nothing outside it", async () => {
    const { client, orderKey, otherKey } = seeded();
    const mutations = createServiceMutations({
      createOrder: createMutationCall(keys.entity("order"), async () => ({})),
    });

    await mutations(client).createOrder({ id: "x" });

    expect(client.getQueryState(orderKey)?.isInvalidated).toBe(true);
    expect(client.getQueryState(otherKey)?.isInvalidated).toBe(false);
  });

  it("invalidates exactly what createMutationHook invalidates for the same scope", async () => {
    // The generated file hands both the same key expression; this pins that
    // the two factories then do the same thing with it.
    for (const scope of [keys.entity("order"), keys.all]) {
      captured.invalidated = [];
      const useWrite = createMutationHook(scope, async () => ({}));
      useWrite();
      (captured.mutation?.onSuccess as (...a: unknown[]) => void)(
        {},
        {},
        undefined,
      );

      const invalidated: unknown[] = [];
      const client = new QueryClient();
      client.invalidateQueries = (async (filters: { queryKey: unknown }) => {
        invalidated.push(filters.queryKey);
      }) as QueryClient["invalidateQueries"];
      await createServiceMutations({
        write: createMutationCall(scope, async () => ({})),
      })(client).write({});

      expect(invalidated).toEqual(captured.invalidated);
    }
  });

  it("replaces broad invalidation with the app's exact scope after success", async () => {
    const { client, orderKey, otherKey } = seeded();
    const response = { id: "written" };
    let completed = false;
    const call = vi.fn(async () => {
      completed = true;
      return response;
    });
    const invalidate = vi.fn((qc: QueryClient) => {
      expect(completed).toBe(true);
      expect(qc).toBe(client);
      void qc.invalidateQueries({ queryKey: orderKey });
    });
    const write = createServiceMutations({
      write: createMutationCall(keys.all, call),
    })(client).write;

    expect(await write({}, { invalidate })).toBe(response);
    expect(invalidate).toHaveBeenCalledTimes(1);
    expect(client.getQueryState(orderKey)?.isInvalidated).toBe(true);
    expect(client.getQueryState(otherKey)?.isInvalidated).toBe(false);
    expect(call).toHaveBeenCalledTimes(1);
    expect(client.getMutationCache().getAll()).toEqual([]);

    // Overrides belong to one call, not to the bound table or later writes.
    await write({});
    expect(client.getQueryState(otherKey)?.isInvalidated).toBe(true);
  });

  it("invalidates nothing on invalidate: false", async () => {
    const { client, orderKey, otherKey } = seeded();
    const write = createServiceMutations({
      write: createMutationCall(keys.all, async () => ({ id: "written" })),
    })(client).write;

    expect(await write({}, { invalidate: false })).toEqual({ id: "written" });
    expect(client.getQueryState(orderKey)?.isInvalidated).toBe(false);
    expect(client.getQueryState(otherKey)?.isInvalidated).toBe(false);
  });

  it("does not call the override on RPC rejection or aborted rejection", async () => {
    for (const aborted of [false, true]) {
      const { client, orderKey, otherKey } = seeded();
      const controller = new AbortController();
      if (aborted) controller.abort();
      const error = new Error(aborted ? "aborted" : "failed");
      const invalidate = vi.fn();
      const call = vi.fn(async () => {
        throw error;
      });
      const write = createServiceMutations({
        write: createMutationCall(keys.all, call),
      })(client).write;
      await expect(
        write({}, { signal: controller.signal, invalidate }),
      ).rejects.toBe(error);
      expect(call).toHaveBeenCalledTimes(1);
      expect(invalidate).not.toHaveBeenCalled();
      expect(client.getQueryState(orderKey)?.isInvalidated).toBe(false);
      expect(client.getQueryState(otherKey)?.isInvalidated).toBe(false);
    }
  });

  it("does not await refetches or retry a callback failure after a successful write", async () => {
    const call = vi.fn(async () => ({ id: "written" }));
    const write = createServiceMutations({
      write: createMutationCall(keys.all, call),
    })(new QueryClient()).write;
    const pending = new Promise<void>(() => {});
    expect(await write({}, { invalidate: () => pending })).toEqual({
      id: "written",
    });
    const error = new Error("local invalidation failed after write");
    await expect(
      write(
        {},
        {
          invalidate: () => {
            throw error;
          },
        },
      ),
    ).rejects.toBe(error);
    expect(call).toHaveBeenCalledTimes(2); // one RPC per invocation, never retry
  });

  it("rejects with the rpc's error and invalidates nothing", async () => {
    const { client, orderKey } = seeded();
    const boom = new Error("denied");
    const mutations = createServiceMutations({
      createOrder: createMutationCall(keys.entity("order"), async () => {
        throw boom;
      }),
    });

    await expect(mutations(client).createOrder({ id: "x" })).rejects.toBe(boom);
    expect(client.getQueryState(orderKey)?.isInvalidated).toBe(false);
  });

  it("types each write by its table entry", () => {
    const mutations = createServiceMutations({
      createOrder: createMutationCall<typeof GetOrderRequestSchema, Order>(
        keys.entity("order"),
        async () => ({ id: "new" }),
      ),
    })(new QueryClient());
    // Type-level only, never awaited.
    const typed: () => Promise<Order> = () =>
      mutations.createOrder({ id: "x" });
    void typed;
    // @ts-expect-error — the table declares no deleteOrder.
    void (() => mutations.deleteOrder({ id: "x" }));
  });
});

describe("callOptions — per-call Connect options on every generated call", () => {
  // A generated file's call: the third parameter is what a file at least as
  // new as this runtime declares (`client.<m>(req, { ...callOptions, signal })`).
  type Recorded = { req: unknown; signal?: AbortSignal; opts?: object };
  function recordingCall() {
    const calls: Recorded[] = [];
    const call = (
      req: unknown,
      signal?: AbortSignal,
      callOptions?: object,
    ): Promise<{ id: string; nextPageToken?: string }> => {
      calls.push({ req, signal, opts: callOptions });
      return Promise.resolve({ id: "r" });
    };
    return { calls, call };
  }
  const keys = serviceKeys("orderService");
  const getOrderKey = keys.query("getOrder", GetOrderRequestSchema, "order");
  type QueryFn = (ctx: Record<string, unknown>) => Promise<unknown>;

  it("are transport metadata: never in the key, never handed to React Query", async () => {
    const { calls, call } = recordingCall();
    const getOrder = createQueryOptions(getOrderKey, call);
    const opts = getOrder(
      { id: "a" },
      { staleTime: 5, callOptions: { headers: { "Forge-Panel": "d/p" } } },
    );

    expect(opts.queryKey).toEqual(getOrder({ id: "a" }).queryKey);
    expect(opts).not.toHaveProperty("callOptions");
    expect(opts.staleTime).toBe(5);

    const signal = new AbortController().signal;
    await (opts.queryFn as QueryFn)({ signal });
    expect(calls[0]?.signal).toBe(signal);
    expect(calls[0]?.opts).toEqual({ headers: { "Forge-Panel": "d/p" } });
  });

  it("without callOptions, calls exactly as before: (req, signal)", async () => {
    const call = vi.fn(async () => ({ id: "r" }));
    const signal = new AbortController().signal;
    await (
      createQueryOptions(getOrderKey, call)({ id: "a" }).queryFn as QueryFn
    )({ signal });
    expect(call.mock.calls[0]).toEqual([{ id: "a" }, signal]);
  });

  it("the function form reads React Query's context — meta included — on every fetch", async () => {
    const { calls, call } = recordingCall();
    const getOrder = createQueryOptions(getOrderKey, call);
    const seen: unknown[] = [];
    const client = new QueryClient();

    await client.fetchQuery(
      getOrder(
        { id: "a" },
        {
          meta: { poll: true },
          callOptions: (ctx) => {
            seen.push(ctx.meta, ctx.queryKey);
            return { headers: { "Forge-Poll": ctx.meta?.poll ? "1" : "0" } };
          },
        },
      ),
    );

    expect(seen).toEqual([{ poll: true }, getOrder({ id: "a" }).queryKey]);
    expect(calls[0]?.opts).toEqual({ headers: { "Forge-Poll": "1" } });
    client.clear();
  });

  it("combine the caller's signal with React Query's: either aborts, neither replaces the other", async () => {
    for (const which of ["query", "caller"] as const) {
      const { calls, call } = recordingCall();
      const query = new AbortController();
      const caller = new AbortController();
      await (
        createQueryOptions(getOrderKey, call)(
          { id: "a" },
          { callOptions: { signal: caller.signal, timeoutMs: 50 } },
        ).queryFn as QueryFn
      )({ signal: query.signal });

      const sent = calls[0]?.signal;
      expect(calls[0]?.opts).toEqual({ timeoutMs: 50 });
      expect(sent?.aborted).toBe(false);
      (which === "query" ? query : caller).abort(which);
      expect(sent?.aborted, which).toBe(true);
      expect(sent?.reason, which).toBe(which);
    }
  });

  it("reach every page of an infinite read, whose function form sees the page", async () => {
    const { calls, call } = recordingCall();
    const listOrders = createInfiniteQueryOptions(
      keys.infinite("listOrders", ListOrdersRequestSchema, "order"),
      call,
    );
    const opts = listOrders(
      { pageSize: 2n },
      {
        callOptions: ({ pageParam }) => ({
          headers: { "x-page": pageParam || "first" },
        }),
      },
    );
    expect(opts).not.toHaveProperty("callOptions");
    const queryFn = opts.queryFn as QueryFn;
    await queryFn({ pageParam: "", signal: undefined });
    await queryFn({ pageParam: "p2", signal: undefined });
    expect(calls.map((c) => c.opts)).toEqual([
      { headers: { "x-page": "first" } },
      { headers: { "x-page": "p2" } },
    ]);
  });

  it("reach a mutation hook's writes, static or from each mutate's variables", async () => {
    const { calls, call } = recordingCall();
    const useCreateOrder = createMutationHook(keys.entity("order"), call);
    type MutationFn = (vars: unknown) => Promise<unknown>;

    useCreateOrder({ callOptions: { headers: { "x-static": "1" } } });
    expect(captured.mutation).not.toHaveProperty("callOptions");
    await (captured.mutation?.mutationFn as MutationFn)({ id: "a" });

    useCreateOrder({
      callOptions: (vars) => ({ headers: { "x-id": String(vars.id) } }),
    });
    await (captured.mutation?.mutationFn as MutationFn)({ id: "b" });

    expect(calls.map((c) => c.opts)).toEqual([
      { headers: { "x-static": "1" } },
      { headers: { "x-id": "b" } },
    ]);
  });

  it("without callOptions, a mutation hook calls (req) exactly as before", async () => {
    const call = vi.fn(async () => ({ id: "r" }));
    createMutationHook(keys.entity("order"), call)();
    await (captured.mutation?.mutationFn as (v: unknown) => Promise<unknown>)({
      id: "a",
    });
    expect(call.mock.calls[0]).toEqual([{ id: "a" }]);
  });

  it("reach an imperative write, its signal combined with the option's", async () => {
    const { calls, call } = recordingCall();
    const mutations = createServiceMutations({
      createOrder: createMutationCall(keys.entity("order"), call),
    })(new QueryClient());
    const own = new AbortController();
    const inOptions = new AbortController();

    await mutations.createOrder(
      { id: "a" },
      {
        signal: own.signal,
        callOptions: { signal: inOptions.signal, headers: { "x-w": "1" } },
      },
    );

    expect(calls[0]?.opts).toEqual({ headers: { "x-w": "1" } });
    inOptions.abort("opt");
    expect(calls[0]?.signal?.aborted).toBe(true);
  });

  it("warn once when a generated file predates them, instead of dropping them silently", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    try {
      // An older generated file: (req, signal) => client.<m>(req, { signal }).
      const old = vi.fn((_req: unknown, _signal?: AbortSignal) =>
        Promise.resolve({ id: "r" }),
      );
      const getOrder = createQueryOptions(getOrderKey, old);
      const queryFn = getOrder({ id: "a" }, { callOptions: { timeoutMs: 1 } })
        .queryFn as QueryFn;
      await queryFn({});
      await queryFn({});
      expect(warn).toHaveBeenCalledTimes(1);
      expect(String(warn.mock.calls[0]?.[0])).toContain("forge generate");
    } finally {
      warn.mockRestore();
    }
  });
});
