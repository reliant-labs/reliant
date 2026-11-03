// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The mock transport ENGINE. A generated frontend used to carry this entire
// pipeline as ~300 lines of Tier-1 scaffold; what varies per project is only
// the table of entities to dispatch on, so the engine lives here and the
// project keeps a declarative descriptor list.
//
// Dispatch order (unchanged from the scaffolded original):
//
//   1. A scenario selected via `?scenario=<name>` (or the auto-generated
//      `default` scenario) gets first crack at every unary RPC. Handlers
//      keyed by `${serviceTypeName}/${methodName}` return typed proto
//      messages and short-circuit the rest of the pipeline.
//   2. If the active scenario has `passthrough: true` AND a fallback
//      transport was supplied (hybrid mode — VITE_MOCK_API=hybrid /
//      NEXT_PUBLIC_MOCK_API=hybrid), unmatched RPCs are forwarded to the
//      real backend. This lets one scenario stub a single endpoint while
//      everything else exercises the live server.
//   3. Otherwise RPCs not overridden fall through to the deterministic
//      per-entity fixtures described by the entity descriptors.
//   4. Anything else rejects with Code.Unimplemented so callers see a
//      clear error instead of a silent empty response.
//
// A fixture List answers the way the backend's generated CRUD List does
// (pkg/crud HandleList), so a filtered table, a paged table and a "how many"
// tile read the same against fixtures as against a server — see
// `serveList` for the exact contract. Custom (non-CRUD) RPCs are not
// interpreted at all; they stay hand-written scenario handlers.
//
// Imported through the "@reliantlabs/forge-web-runtime/mock-transport" subpath,
// never the barrel: a production bundle must be able to shake the fixtures
// and this engine out entirely, which is why the generated `connect.ts`
// reaches its own shim through a dynamic `require()` gated on the mock-mode
// env var.
import {
  create,
  ScalarType,
  type DescField,
  type DescMessage,
  type MessageInitShape,
} from "@bufbuild/protobuf";
import {
  Code,
  ConnectError,
  type Transport,
  type UnaryResponse,
} from "@connectrpc/connect";

/**
 * The shape the generated scenario files satisfy. Structural on purpose: the
 * project's own `Scenario` type (src/mocks/scenario-types.ts) carries a
 * richer, per-RPC-typed `handlers` map, and this is the subset the engine
 * dispatches through.
 */
export interface MockScenario {
  name: string;
  /** Per-RPC overrides keyed by `${serviceTypeName}/${methodName}`. */
  handlers: Record<string, ((req: never) => unknown) | undefined>;
  /** Forward unmatched RPCs to the real backend (hybrid mode only). */
  passthrough?: boolean;
  /** Non-RPC side effects, run once before the first RPC fires. */
  setup?: () => void;
}

/** The generated `src/mocks/scenarios` barrel, as the engine consumes it. */
export interface MockScenarioRegistry {
  byName(name: string): MockScenario | undefined;
  defaultScenario: MockScenario;
}

/**
 * One CRUD arm of an entity's dispatch table.
 *
 * The List arm needs nothing beyond these three fields to honour filters,
 * paging and total_count: the REQUEST schema arrives on every call as the
 * Connect method's `input`, and the entity schema is the element type of
 * `itemsField` on `responseSchema`.
 */
export interface MockListRpc {
  rpc: string;
  responseSchema: DescMessage;
  /**
   * The camelCase name of the list response's repeated field — the ACTUAL
   * proto field (e.g. `keys`), not the camelCased entity plural.
   */
  itemsField: string;
}

export interface MockGetRpc {
  rpc: string;
  responseSchema: DescMessage;
  /** The camelCase field wrapping the entity on the response message. */
  entityField: string;
}

export interface MockWriteRpc extends MockGetRpc {
  /**
   * The camelCase field wrapping the entity on the REQUEST message. AIP-134
   * requests nest the entity ({ item: {...}, updateMask }); the generated
   * forms submit it flattened. Both are accepted.
   */
  requestField: string;
}

export interface MockDeleteRpc {
  rpc: string;
}

/**
 * Everything the engine needs to serve one entity's CRUD quintet from
 * fixtures. Generated into `src/lib/mock-transport.ts` from the project's
 * proto descriptors.
 */
export interface MockEntityDescriptor {
  /** Fully-qualified proto service name, e.g. "demo.v1.ClinicService". */
  service: string;
  /** Lowercase entity name, used verbatim in the NotFound message. */
  label: string;
  /**
   * The camelCase PRIMARY-KEY field. The session store keys records by this
   * field; hardcoding "id" breaks every surrogate-PK entity (a
   * `usage_event_id` PK projects to a message with no `id` field).
   */
  pkField: string;
  /** Deterministic seed rows — the same values the dev database is seeded with. */
  fixtures: readonly unknown[];
  /** The entity message schema. Required by the create/update write paths. */
  entitySchema?: DescMessage;
  list?: MockListRpc;
  get?: MockGetRpc;
  create?: MockWriteRpc;
  update?: MockWriteRpc;
  delete?: MockDeleteRpc;
}

export interface CreateMockTransportOptions {
  /** The resolved scenario — see {@link resolveActiveScenario}. */
  scenario: MockScenario;
  /** Per-entity fixture dispatch. Omit for a scenario-only project. */
  entities?: readonly MockEntityDescriptor[];
  /**
   * Optional real Connect transport. When the active scenario declares
   * `passthrough: true`, any RPC not matched by a scenario handler is
   * forwarded here instead of falling through to the fixtures.
   */
  fallback?: Transport;
}

/**
 * Mutable session stores, keyed by descriptor identity.
 *
 * Create/Update/Delete round-trip within one browser session (a created row
 * appears in the next List; a deleted one disappears) and reset to the
 * deterministic fixtures on reload. The scaffolded original got that from
 * module-scope `const store = new Map(...)`; keying off the descriptor
 * object preserves it — a module-level descriptor array hands back the same
 * store on every call, so building the transport twice (hybrid mode, a test
 * swapping transports) does not silently fork the data.
 */
const stores = new WeakMap<MockEntityDescriptor, Map<string, unknown>>();

function storeFor(entity: MockEntityDescriptor): Map<string, unknown> {
  let store = stores.get(entity);
  if (!store) {
    store = new Map(
      entity.fixtures.map((row) => [
        String((row as Record<string, unknown>)[entity.pkField]),
        row,
      ]),
    );
    stores.set(entity, store);
  }
  return store;
}

/** Scenarios whose `setup()` has already run. */
const initialized = new WeakSet<MockScenario>();

/**
 * Resolve the scenario the app should run under, and run its `setup()` once.
 *
 * Call this at MODULE SCOPE (the generated `src/lib/mock-transport.ts` does).
 * Reading the URL once — rather than on every RPC — means navigating away
 * from `?scenario=` keeps the scenario active until a full reload, matching
 * the agent-driven flow where the URL is the single source of truth.
 *
 * `setup()` is for non-RPC state (localStorage flags, sessionStorage). It is
 * synchronous and makes no network calls.
 */
export function resolveActiveScenario(
  registry: MockScenarioRegistry,
): MockScenario {
  const requested =
    typeof globalThis !== "undefined" && globalThis.location
      ? new URLSearchParams(globalThis.location.search).get("scenario")
      : null;
  // A ternary, not `&&`: with `??` alone the empty-string falsy branch leaks
  // '' into the inferred union, and '' survives `??` (which only replaces
  // null/undefined), so downstream access on .setup / .handlers would fail
  // under strict tsc.
  const active =
    (requested ? registry.byName(requested) : undefined) ??
    registry.defaultScenario;

  if (!initialized.has(active)) {
    initialized.add(active);
    active.setup?.();
  }
  return active;
}

/**
 * Normalize whatever a streaming scenario handler returns into an
 * AsyncIterable<unknown>. Accepts arrays, iterables, async iterables, and
 * promises that resolve to any of the above. Single (non-iterable) values
 * are wrapped as a one-element stream — handy for handlers that conceptually
 * yield once.
 */
export async function* toAsyncIterable(value: unknown): AsyncIterable<unknown> {
  const resolved = await Promise.resolve(value);
  if (resolved == null) return;
  // AsyncIterable
  if (
    typeof (resolved as { [Symbol.asyncIterator]?: unknown })[
      Symbol.asyncIterator
    ] === "function"
  ) {
    for await (const m of resolved as AsyncIterable<unknown>) yield m;
    return;
  }
  // Iterable (including arrays)
  if (
    typeof (resolved as { [Symbol.iterator]?: unknown })[Symbol.iterator] ===
    "function"
  ) {
    for (const m of resolved as Iterable<unknown>) yield m;
    return;
  }
  // Single value
  yield resolved;
}

function makeUnaryResponse<T>(
  method: { name: string; parent: { typeName: string } },
  message: T,
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
 * A fixture-backed handler for one RPC key. `requestSchema` is the Connect
 * method's input descriptor — absent only when a caller hand-builds a method
 * object without one, in which case List serves paging alone.
 */
type FixtureHandler = (
  input: unknown,
  requestSchema: DescMessage | undefined,
) => unknown;

// ── List semantics ─────────────────────────────────────────────────────
//
// What the backend's generated List does, and therefore what the fixture
// List does (pkg/crud HandleList + the generated Filters closure):
//
//   - Every `optional` request field that is SET filters by equality on the
//     entity field of the same name. Enums compare by value. A field with
//     IMPLICIT presence (no `optional`) is never a filter — the backend
//     generates none for it, so the mock must not invent one.
//   - `search` matches case-insensitively against the entity's string
//     fields and enum value names (enum columns are stored as names).
//   - `order_by` (snake_case column, comma-separated, optional ASC/DESC per
//     column) plus `descending` sorts; an unknown column is InvalidArgument.
//   - `page_size` defaults to 50 and is clamped to 100. `total_count` is the
//     filtered count BEFORE paging. `next_page_token` is minted only for the
//     default (primary-key) order — an ordered list is single-page on the
//     backend, and a mock that paged it would hide that.
//
// Paging, ordering and search arrive through these request fields, by the
// names forge's CRUD List requests declare. They are never equality filters.
const LIST_CONTROL_FIELDS = new Set([
  "pageSize",
  "pageToken",
  "orderBy",
  "descending",
  "search",
]);

/** pkg/crud ListOp's defaults: 0 means 50, and no page exceeds 100. */
const DEFAULT_PAGE_SIZE = 50;
const MAX_PAGE_SIZE = 100;

/** FeatureSet_FieldPresence.EXPLICIT — proto3 `optional`, oneofs, messages. */
const EXPLICIT_PRESENCE = 1;

type Row = Record<string, unknown>;

/** The entity message a List response repeats, read off its descriptor. */
function listItemSchema(list: MockListRpc): DescMessage | undefined {
  const items = list.responseSchema.field[list.itemsField];
  return items?.fieldKind === "list" && items.listKind === "message"
    ? items.message
    : undefined;
}

/** Scalars and enums compare by value; messages, bytes, lists and maps do not. */
function isComparable(field: DescField): boolean {
  if (field.fieldKind === "enum") return true;
  return field.fieldKind === "scalar" && field.scalar !== ScalarType.BYTES;
}

function sameValue(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  // A 64-bit field is a bigint — or a string under jstype=JS_STRING — and
  // the two sides are not guaranteed to agree on which.
  if (a == null || b == null) return false;
  return (
    (typeof a === "bigint" || typeof b === "bigint") && String(a) === String(b)
  );
}

function enumName(field: DescField, value: unknown): string | undefined {
  return field.fieldKind === "enum"
    ? field.enum.values.find((v) => v.number === value)?.name
    : undefined;
}

/** The value a column sorts by, as Postgres would order the stored column. */
function sortKey(field: DescField, value: unknown): unknown {
  if (value == null) return undefined;
  // Enum columns hold the value NAME, so they sort alphabetically by name.
  if (field.fieldKind === "enum") return enumName(field, value) ?? "";
  if (field.fieldKind === "scalar") return value;
  if (
    field.fieldKind === "message" &&
    field.message.typeName === "google.protobuf.Timestamp"
  ) {
    const ts = value as { seconds: bigint; nanos: number };
    return BigInt(ts.seconds) * 1_000_000_000n + BigInt(ts.nanos);
  }
  return undefined;
}

/** Postgres's default: NULL sorts after every value ascending. */
function compareKeys(a: unknown, b: unknown): number {
  if (a === undefined || b === undefined) {
    return a === b ? 0 : a === undefined ? 1 : -1;
  }
  const x = a as string | number | bigint | boolean;
  const y = b as string | number | bigint | boolean;
  return x < y ? -1 : x > y ? 1 : 0;
}

function invalidArgument(message: string): ConnectError {
  return new ConnectError(`mock-transport: ${message}`, Code.InvalidArgument);
}

/** Parse `order_by` the way orm.ValidateOrderBy reads it. */
function parseOrderBy(
  clause: string,
  descending: boolean,
  itemSchema: DescMessage,
): { field: DescField; desc: boolean }[] {
  return clause.split(",").map((part) => {
    const [column = "", direction] = part.trim().split(/\s+/);
    const field = itemSchema.fields.find(
      (f) => f.name === column || f.localName === column,
    );
    if (!field) {
      throw invalidArgument(`unknown order-by column "${column}"`);
    }
    const dir = direction?.toUpperCase();
    if (dir !== undefined && dir !== "ASC" && dir !== "DESC") {
      throw invalidArgument(`invalid order-by direction "${direction}"`);
    }
    return { field, desc: dir === undefined ? descending : dir === "DESC" };
  });
}

/** A count in whatever JS type the response's total_count field takes. */
function countValue(field: DescField, n: number): unknown {
  if (field.fieldKind !== "scalar") return n;
  switch (field.scalar) {
    case ScalarType.INT64:
    case ScalarType.UINT64:
    case ScalarType.SINT64:
    case ScalarType.FIXED64:
    case ScalarType.SFIXED64:
      return field.longAsString ? String(n) : BigInt(n);
    default:
      return n;
  }
}

/**
 * Answer one List call from the entity's rows. See the List semantics note
 * above for the contract; the engine's tests pin each clause.
 */
function serveList(
  rows: readonly unknown[],
  list: MockListRpc,
  input: unknown,
  requestSchema: DescMessage | undefined,
): unknown {
  const req = (input ?? {}) as Row;
  const itemSchema = listItemSchema(list);
  let matched = rows as readonly Row[];

  if (itemSchema && requestSchema) {
    for (const filter of requestSchema.fields) {
      if (LIST_CONTROL_FIELDS.has(filter.localName)) continue;
      if (filter.presence !== EXPLICIT_PRESENCE || !isComparable(filter)) {
        continue;
      }
      const want = req[filter.localName];
      if (want == null) continue; // unset: no filter
      const target = itemSchema.field[filter.localName];
      if (!target || !isComparable(target)) continue;
      matched = matched.filter((row) => sameValue(row[filter.localName], want));
    }
  }

  const search =
    typeof req.search === "string" ? req.search.trim().toLowerCase() : "";
  if (itemSchema && search) {
    const searchable = itemSchema.fields.filter(
      (f) =>
        f.fieldKind === "enum" ||
        (f.fieldKind === "scalar" && f.scalar === ScalarType.STRING),
    );
    matched = matched.filter((row) =>
      searchable.some((f) => {
        const value = row[f.localName];
        const text = f.fieldKind === "enum" ? enumName(f, value) : value;
        return typeof text === "string" && text.toLowerCase().includes(search);
      }),
    );
  }

  const orderBy = typeof req.orderBy === "string" ? req.orderBy.trim() : "";
  if (itemSchema && orderBy) {
    const keys = parseOrderBy(orderBy, req.descending === true, itemSchema);
    matched = [...matched].sort((a, b) => {
      for (const { field, desc } of keys) {
        const c = compareKeys(
          sortKey(field, a[field.localName]),
          sortKey(field, b[field.localName]),
        );
        if (c !== 0) return desc ? -c : c;
      }
      return 0;
    });
  }

  const total = matched.length;

  let offset = 0;
  const token = typeof req.pageToken === "string" ? req.pageToken : "";
  if (token) {
    // The mock's own cursor: an offset into the filtered rows. Opaque to the
    // client, exactly like the backend's keyset cursor.
    if (!/^\d+$/.test(token)) throw invalidArgument("invalid page token");
    offset = Number(token);
  }
  const requested = Number(req.pageSize ?? 0);
  const pageSize =
    requested > 0 ? Math.min(requested, MAX_PAGE_SIZE) : DEFAULT_PAGE_SIZE;
  const end = offset + pageSize;

  const init: Row = { [list.itemsField]: matched.slice(offset, end) };
  const totalField = list.responseSchema.field.totalCount;
  if (totalField) init.totalCount = countValue(totalField, total);
  if (list.responseSchema.field.nextPageToken && !orderBy && end < total) {
    init.nextPageToken = String(end);
  }
  return create(list.responseSchema, init as MessageInitShape<DescMessage>);
}

/**
 * Compile the entity descriptors into a flat `${service}/${rpc}` → handler
 * map. Building it once per transport keeps per-RPC dispatch to a single
 * lookup, exactly like the generated `switch` it replaces.
 */
function buildFixtureTable(
  entities: readonly MockEntityDescriptor[],
): Map<string, FixtureHandler> {
  const table = new Map<string, FixtureHandler>();

  for (const entity of entities) {
    const pk = entity.pkField;
    const key = (rpc: string) => `${entity.service}/${rpc}`;

    if (entity.list) {
      const list = entity.list;
      table.set(key(list.rpc), (input, requestSchema) =>
        serveList(
          Array.from(storeFor(entity).values()),
          list,
          input,
          requestSchema,
        ),
      );
    }

    if (entity.get) {
      const { rpc, responseSchema, entityField } = entity.get;
      table.set(key(rpc), (input) => {
        // Read the key off the SAME field the store is keyed by, not a
        // hardcoded `id` — a surrogate-PK entity would otherwise look up
        // String(undefined) and miss every record.
        const req = input as Record<string, unknown> | undefined;
        const found = storeFor(entity).get(String(req?.[pk]));
        if (!found) {
          // A miss is NotFound — exactly what the real backend returns.
          // (Falling back to the first fixture made every detail page
          // "work" against the wrong record and hid bad-id bugs.)
          throw new ConnectError(
            `${entity.label} ${String(req?.[pk])} not found`,
            Code.NotFound,
          );
        }
        return create(responseSchema, {
          [entityField]: found,
        } as MessageInitShape<DescMessage>);
      });
    }

    if (entity.create && entity.entitySchema) {
      const { rpc, responseSchema, entityField, requestField } = entity.create;
      const entitySchema = entity.entitySchema;
      table.set(key(rpc), (input) => {
        // Create requests carry the entity fields flattened (that's how the
        // generated form submits); a nested { <entity>: {...} } payload is
        // accepted too.
        const req = (input ?? {}) as Record<string, unknown>;
        const init = {
          ...((req[requestField] ?? req) as Record<string, unknown>),
        };
        delete init.$typeName;
        // Mint the primary key on the PK field the store is keyed by — for a
        // surrogate-PK entity this is NOT `id`, and writing a generated `id`
        // would leave the store key undefined so the row never round-trips.
        const id =
          typeof init[pk] === "string" && init[pk]
            ? (init[pk] as string)
            : crypto.randomUUID();
        const created = create(entitySchema, {
          ...init,
          [pk]: id,
        } as MessageInitShape<DescMessage>);
        storeFor(entity).set(String(id), created);
        return create(responseSchema, {
          [entityField]: created,
        } as MessageInitShape<DescMessage>);
      });
    }

    if (entity.update && entity.entitySchema) {
      const { rpc, responseSchema, entityField, requestField } = entity.update;
      const entitySchema = entity.entitySchema;
      table.set(key(rpc), (input) => {
        const req = (input ?? {}) as Record<string, unknown>;
        // AIP-134 requests wrap the entity ({ <entity>: {...}, updateMask })
        // — the id lives inside the wrapper; flat requests carry it at the
        // top level.
        const patch = {
          ...((req[requestField] ?? req) as Record<string, unknown>),
        };
        delete patch.$typeName;
        const id = String(patch[pk] ?? req[pk] ?? "");
        const store = storeFor(entity);
        const existing = store.get(id);
        if (!existing) {
          throw new ConnectError(
            `${entity.label} ${id} not found`,
            Code.NotFound,
          );
        }
        const updated = create(entitySchema, {
          ...(existing as Record<string, unknown>),
          ...patch,
          [pk]: id,
        } as MessageInitShape<DescMessage>);
        store.set(id, updated);
        return create(responseSchema, {
          [entityField]: updated,
        } as MessageInitShape<DescMessage>);
      });
    }

    if (entity.delete) {
      table.set(key(entity.delete.rpc), (input) => {
        // Delete by the SAME field the store is keyed by.
        const req = input as Record<string, unknown> | undefined;
        storeFor(entity).delete(String(req?.[pk]));
        // Delete returns an empty response — the standard CRUD shape.
        return {};
      });
    }
  }

  return table;
}

/**
 * Create the mock transport.
 *
 * The generated `src/lib/mock-transport.ts` supplies the resolved scenario
 * and the project's entity descriptors; `connect.ts` supplies the optional
 * real transport when running in hybrid mode.
 */
export function createMockTransport(
  options: CreateMockTransportOptions,
): Transport {
  const { scenario, entities = [], fallback } = options;
  const passthrough = scenario.passthrough === true && fallback != null;
  const fixtures = buildFixtureTable(entities);

  // Bind the object literal to a Transport-typed variable rather than
  // casting at the return site. A trailing assertion cast does not propagate
  // Connect's Transport signature backwards into the literal's method
  // bodies, so under `strict` tsc every callback parameter on unary/stream
  // errors with TS7006 ("implicitly has an 'any' type").
  const transport: Transport = {
    // Connect v2's Transport.unary signature is
    //   (method, signal, timeoutMs, header, input, contextValues)
    // — method first, no separate service arg. `method.parent.typeName` is
    // the fully-qualified service name; that is what handler keys and
    // fixture keys are matched against.
    async unary(method, signal, timeoutMs, header, input, contextValues) {
      const key = `${method.parent.typeName}/${method.name}`;

      // 1) Scenario overlay.
      const handler = scenario.handlers[key];
      if (handler) {
        const result = await handler(input as never);
        return makeUnaryResponse(method, result);
      }

      // 2) Passthrough: route to the real backend instead of fixtures.
      //    Skips fixtures entirely — in hybrid mode the whole point is
      //    "use real data for anything not explicitly stubbed".
      if (passthrough) {
        return fallback!.unary(
          method,
          signal,
          timeoutMs,
          header,
          input,
          contextValues,
        );
      }

      // 3) Base fixture dispatch, backed by the mutable per-entity stores so
      //    writes round-trip within the session. Misses are REAL errors
      //    (Code.NotFound), never a silent wrong-record fallback.
      const fixture = fixtures.get(key);
      if (fixture) {
        // `method.input` is typed as always present, but a hand-built method
        // object (a test, a custom harness) may omit it.
        const requestSchema = (method as { input?: DescMessage }).input;
        return makeUnaryResponse(method, fixture(input, requestSchema));
      }

      // 4) Nothing matched. Surface a clear error instead of hanging.
      return Promise.reject(
        new ConnectError(
          `mock-transport: no scenario handler or entity fixture for ${key}`,
          Code.Unimplemented,
        ),
      );
    },

    // Streaming: a scenario handler can return an array, iterable, async
    // iterable, or a Promise resolving to any of those. The transport adapts
    // it into the AsyncIterable<Response> shape Connect expects. If no
    // handler matches, reject with Unimplemented — base-fixture streaming is
    // intentionally not served (there is no canonical "list of N messages"
    // shape for an arbitrary streaming RPC).
    //
    // No explicit return-type annotation: the outer `const transport:
    // Transport` binding lets tsc infer the per-callback signature from
    // Connect's generic Transport.stream<I, O>. Annotating this as
    // Promise<StreamResponse<never, never>> makes the passthrough branch
    // ill-typed (TS2322) because fallback.stream returns StreamResponse<I,O>.
    async stream(method, signal, timeoutMs, header, input, contextValues) {
      const key = `${method.parent.typeName}/${method.name}`;
      const handler = scenario.handlers[key];
      if (!handler) {
        // Passthrough mirrors the unary path: in hybrid mode, unmatched
        // streaming RPCs go to the real backend.
        if (passthrough) {
          return fallback!.stream(
            method,
            signal,
            timeoutMs,
            header,
            input,
            contextValues,
          );
        }
        return Promise.reject(
          new ConnectError(
            `mock-transport: no scenario handler for streaming RPC ${key}`,
            Code.Unimplemented,
          ),
        );
      }
      // Read the request stream eagerly into an array so the scenario handler
      // sees the full client-streaming payload. For server-stream and
      // unary-into-stream-out RPCs this is a single message.
      const requestMessages: unknown[] = [];
      try {
        for await (const m of input as AsyncIterable<unknown>) {
          requestMessages.push(m);
        }
      } catch {
        // Synthetic clients sometimes pass a single value rather than an
        // iterable; treat that as an empty request payload.
      }
      const handlerInput =
        requestMessages.length <= 1 ? requestMessages[0] : requestMessages;
      const result = handler(handlerInput as never);
      return {
        service: method.parent as never,
        method: method as never,
        stream: true,
        header: new Headers(),
        message: toAsyncIterable(result) as AsyncIterable<never>,
        trailer: new Headers(),
      };
    },
  };
  return transport;
}
