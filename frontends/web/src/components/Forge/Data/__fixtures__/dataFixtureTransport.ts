// Copyright (c) 2025 Reliant Labs

/**
 * The in-memory data fixture (fixtureDataApi.ts) on the wire: a transport
 * whose AppDatabaseService answers ListBrowsableDatabases, DescribeDatabase
 * and BrowseTable from it, so a component test runs the browser's real reads
 * (useDataBrowser.ts: the scoped client and appDatabaseApi.ts's mappers)
 * against the fixture's behaviour.
 *
 * Each handler decodes the request into the fixture's model (what it records
 * in `calls`), asks the fixture, and encodes its answer as control-plane
 * would. The fixture's methods are looked up per call, so a test that wraps
 * one (`api.describeDatabase = …`) is answered by its wrapper. Every other
 * rpc is Unimplemented.
 *
 *   const scope = dataFixtureScope(createDataFixtureApi(), client);
 *   render(<TransportScopeProvider scope={scope}>{ui}</TransportScopeProvider>);
 */

import { fromJson } from "@bufbuild/protobuf";
import { ValueSchema } from "@bufbuild/protobuf/wkt";
import { createRouterTransport, type Transport } from "@connectrpc/connect";
import type { QueryClient } from "@tanstack/react-query";

import {
  AppDatabaseService,
  BrowsableDatabaseKind,
  BrowseCount,
  BrowseFilterOp,
  DataAccessPolicy,
  DatabaseTableKind,
  IdentitySubjectKind,
  type BrowseTableRequest,
} from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { createTransportScope, type TransportScope } from "@/lib/transportScope";
import type { BrowseCountMode, BrowseInput, BrowseOp, BrowsableDb, DataAccessConfirmation, DataAccessPolicyName, DbTableKind } from "@/services/controlPlane/observability/appDatabaseApi";

import type { DataFixtureApi } from "./fixtureDataApi";

const POLICIES: Record<DataAccessPolicyName, DataAccessPolicy> = {
  unknown: DataAccessPolicy.UNSPECIFIED,
  open: DataAccessPolicy.OPEN,
  audited: DataAccessPolicy.AUDITED,
  deny: DataAccessPolicy.DENY,
};

const DB_KINDS: Record<BrowsableDb["kind"], BrowsableDatabaseKind> = {
  unknown: BrowsableDatabaseKind.UNSPECIFIED,
  managed: BrowsableDatabaseKind.MANAGED,
  external: BrowsableDatabaseKind.EXTERNAL,
};

const TABLE_KINDS: Record<DbTableKind, DatabaseTableKind> = {
  unknown: DatabaseTableKind.UNSPECIFIED,
  table: DatabaseTableKind.TABLE,
  view: DatabaseTableKind.VIEW,
  materialized_view: DatabaseTableKind.MATERIALIZED_VIEW,
  foreign_table: DatabaseTableKind.FOREIGN_TABLE,
};

const OPS: Record<BrowseFilterOp, BrowseOp | undefined> = {
  [BrowseFilterOp.UNSPECIFIED]: undefined,
  [BrowseFilterOp.EQ]: "eq",
  [BrowseFilterOp.NEQ]: "neq",
  [BrowseFilterOp.LT]: "lt",
  [BrowseFilterOp.LTE]: "lte",
  [BrowseFilterOp.GT]: "gt",
  [BrowseFilterOp.GTE]: "gte",
  [BrowseFilterOp.LIKE]: "like",
  [BrowseFilterOp.ILIKE]: "ilike",
  [BrowseFilterOp.IS_NULL]: "is_null",
  [BrowseFilterOp.NOT_NULL]: "not_null",
  [BrowseFilterOp.IN]: "in",
};

const COUNTS: Record<BrowseCount, BrowseCountMode> = {
  [BrowseCount.UNSPECIFIED]: "none",
  [BrowseCount.NONE]: "none",
  [BrowseCount.ESTIMATED]: "estimated",
  [BrowseCount.EXACT]: "exact",
};

/** The audited confirmation, when the request carries one (appDatabaseApi.ts sends both fields or neither). */
function accessOf(req: { confirmEnvId?: string; reason?: string }): { access?: DataAccessConfirmation } {
  return req.confirmEnvId === undefined && req.reason === undefined ? {} : { access: { confirmEnvId: req.confirmEnvId ?? "", reason: req.reason ?? "" } };
}

function browseInput(req: BrowseTableRequest): BrowseInput {
  return {
    environmentId: req.environmentId,
    database: req.database,
    table: req.table,
    columns: [...req.columns],
    filters: req.filters.map((f) => ({ column: f.column, op: OPS[f.op]!, values: [...f.values] })),
    sort: req.sort.map((s) => ({ column: s.column, desc: s.desc })),
    limit: req.limit,
    offset: Number(req.offset),
    count: COUNTS[req.count],
    ...accessOf(req),
  };
}

/** The fixture's AppDatabaseService, on the wire. */
export function dataFixtureTransport(api: DataFixtureApi): Transport {
  return createRouterTransport(({ service }) => {
    service(AppDatabaseService, {
      async listBrowsableDatabases(req, { signal }) {
        const dbs = await api.listBrowsableDatabases(req.environmentId, { signal });
        return { databases: dbs.map((db) => ({ name: db.name, kind: DB_KINDS[db.kind], policy: POLICIES[db.policy], reachable: db.reachable, unreachableReason: db.unreachableReason })) };
      },
      async describeDatabase(req, { signal }) {
        const out = await api.describeDatabase({ environmentId: req.environmentId, database: req.database, ...accessOf(req) }, { signal });
        return {
          database: out.database,
          durationMs: BigInt(out.durationMs),
          tables: out.tables.map((t) => ({
            schema: t.schema,
            name: t.name,
            kind: TABLE_KINDS[t.kind],
            estimatedRows: BigInt(t.estimatedRows),
            comment: t.comment,
            columns: t.columns.map((c) => ({
              name: c.name,
              pgType: c.pgType,
              nullable: c.nullable,
              comment: c.comment,
              isPrimaryKey: c.isPrimaryKey,
              identitySubject: c.identitySubject === "id" ? IdentitySubjectKind.ID : c.identitySubject === "email" ? IdentitySubjectKind.EMAIL : IdentitySubjectKind.UNSPECIFIED,
            })),
            primaryKey: [...t.primaryKey],
            foreignKeys: t.foreignKeys.map((k) => ({ ...k })),
            referencedBy: t.referencedBy.map((k) => ({ ...k })),
          })),
        };
      },
      async browseTable(req, { signal }) {
        const page = await api.browseTable(browseInput(req), { signal });
        return {
          columns: page.columns,
          rows: page.rows.map((row, i) => ({ values: row.map((cell) => fromJson(ValueSchema, cell)), truncatedCells: page.truncatedCells[i] ?? [] })),
          hasMore: page.hasMore,
          truncated: page.truncated,
          nextOffset: BigInt(page.nextOffset),
          ...(page.total === null ? {} : { total: BigInt(page.total) }),
          totalIsEstimate: page.totalIsEstimate,
          totalIsLowerBound: page.totalIsLowerBound,
          durationMs: BigInt(page.durationMs),
          database: page.database,
          table: page.table,
          replica: page.replica,
        };
      },
    });
  });
}

/** A TransportScope over the fixture, caching on `queryClient` (a test's). */
export function dataFixtureScope(api: DataFixtureApi, queryClient: QueryClient): TransportScope {
  return createTransportScope({ id: "data-fixture", transport: dataFixtureTransport(api), queryClient });
}
