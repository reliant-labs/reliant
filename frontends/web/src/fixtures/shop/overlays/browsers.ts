// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's overlays for the Data and Storage browsers
 * (/forge-data-preview, /forge-storage-preview), by the preview's `?case=` id.
 * The pack's own answers (handlers/inventory.ts, handlers/storage.ts) are the
 * default case; these are the states its story never reaches. Slow answers
 * are the transport's `latency` interceptor, not an overlay.
 */

import { create } from "@bufbuild/protobuf";
import { Code } from "@connectrpc/connect";

import {
  AppDatabaseService,
  DataAccessPolicy,
  DatabaseForeignKeySchema,
  DatabaseTableKind,
  DatabaseTableSchema,
  type BrowseTableRequest,
} from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { StorageAccessPolicy, StorageBrowserService } from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";

import { answering, emptying, failing, refining, stalling } from "../../overlay";
import type { FixturePack } from "../../transport";
import { inventoryScenarioHandlers } from "../handlers/inventory";
import { storageScenarioHandlers } from "../handlers/storage";

const db = AppDatabaseService.method;
const storage = StorageBrowserService.method;

const inventory = inventoryScenarioHandlers();

/** forge's machine-readable cause, as control-plane sends it beside the code. */
const reason = (value: string) => ({ "x-forge-error-reason": value });

const denies = "data access policy denies reads";

/** Every environment's databases under a DENY policy: listed, never read. */
const dataDeny: FixturePack[] = [
  refining("data.deny.list", db.listBrowsableDatabases, inventory, (answer) => {
    for (const database of answer.databases) database.policy = DataAccessPolicy.DENY;
  }),
  failing("data.deny.reads", [db.describeDatabase, db.browseTable], Code.PermissionDenied, denies),
];

/** Twelve more tables (empty) with a foreign key to customers: a long Related section on a customer's row. */
const EXTRA_REFERENCES = 12;
const extraRefs = Array.from({ length: EXTRA_REFERENCES }, (_, i) =>
  create(DatabaseForeignKeySchema, {
    constraint: `ref_${i}_customer_id_fkey`,
    schema: "audit",
    table: `ref_${i}`,
    columns: ["customer_id"],
    refSchema: "public",
    refTable: "customers",
    refColumns: ["id"],
  })
);

const dataManyRefs: FixturePack[] = [
  refining("data.many-refs.describe", db.describeDatabase, inventory, (answer) => {
    const tables = answer.tables.map((table) => (table.schema === "public" && table.name === "customers" ? create(DatabaseTableSchema, { ...table, referencedBy: [...table.referencedBy, ...extraRefs] }) : table));
    const extra = extraRefs.map((ref) => create(DatabaseTableSchema, {
      schema: ref.schema,
      name: ref.table,
      kind: DatabaseTableKind.TABLE,
      estimatedRows: 0n,
      comment: "",
      primaryKey: ["id"],
      columns: [
        { name: "id", pgType: "bigint", isPrimaryKey: true },
        { name: "customer_id", pgType: "bigint" },
      ],
      foreignKeys: [ref],
      referencedBy: [],
    }));
    answer.tables = [...extra, ...tables];
  }),
  // The audit tables answer empty; the rest is the pack's.
  answering("data.many-refs.browse", db.browseTable, (_method, request) => {
    const { database, table } = request as BrowseTableRequest;
    if (table.startsWith("audit.")) return { database, table, columns: [{ name: "id", pgType: "bigint" }, { name: "customer_id", pgType: "bigint" }], rows: [], hasMore: false, nextOffset: 0n, total: 0n };
    return (inventory[`${AppDatabaseService.typeName}/BrowseTable`] as (req: unknown) => unknown)(request);
  }),
];

/** Every environment's bucket under a DENY policy. */
const storageDeny = refining("storage.deny", storage.listBrowsableBuckets, storageScenarioHandlers(), (answer) => {
  answer.policy = StorageAccessPolicy.DENY;
});

export const browserOverlays = {
  data: {
    deny: dataDeny,
    denied: failing("data.denied", db.listBrowsableDatabases, Code.PermissionDenied, "permission denied: db:query"),
    empty: emptying("data.empty", db.listBrowsableDatabases),
    unreachable: failing("data.unreachable", db.describeDatabase, Code.FailedPrecondition, "datasource unreachable", reason("datasource_unreachable")),
    timeout: failing("data.timeout", db.browseTable, Code.DeadlineExceeded, "canceling statement due to statement timeout"),
    "many-refs": dataManyRefs,
  },
  storage: {
    deny: storageDeny,
    denied: failing("storage.denied", storage.listBrowsableBuckets, Code.PermissionDenied, "permission denied: storage:read"),
    empty: emptying("storage.empty", storage.listBrowsableBuckets),
    "buckets-down": failing("storage.buckets-down", storage.listBrowsableBuckets, Code.Unavailable, "storage unavailable"),
    shape: failing("storage.shape", storage.listBrowsableBuckets, Code.FailedPrecondition, "no recorded release for this environment", reason("shape_unknown")),
    rate: failing("storage.rate", storage.listObjects, Code.ResourceExhausted, "listing rate limited", reason("rate_limited")),
    loading: stalling("storage.loading", storage.listBrowsableBuckets),
  },
} satisfies Record<string, Record<string, FixturePack | readonly FixturePack[]>>;
