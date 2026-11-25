// Copyright (c) 2025 Reliant Labs

/**
 * The seam between the data browser (components/Forge/Data) and the three
 * AppDatabaseService reads it makes: ListBrowsableDatabases, DescribeDatabase
 * and BrowseTable.
 *
 * The same shape as ObservabilityApiContext, kept separate because the data
 * browser is not an observability view: component tests hand it the
 * in-memory fixture (Data/__fixtures__/fixtureDataApi.ts), and production
 * (and the dev-only preview, /forge-data-preview, over the shop fixture
 * pack's transport) never provides
 * anything but the real client, which calls the control plane directly with
 * the user's session (never through the daemon: services/forge/live.ts). The
 * default is that client on the nearest TransportScope's transport, else on
 * the app's.
 */

import { createContext, useContext, type ReactNode } from "react";
import type { Transport } from "@connectrpc/connect";

import { transport as appTransport } from "@/lib/connect";
import { useTransport } from "@/lib/transportScope";

import * as appDatabase from "./appDatabaseApi";

export interface DataBrowserApi {
  listBrowsableDatabases: typeof appDatabase.listBrowsableDatabases;
  describeDatabase: typeof appDatabase.describeDatabase;
  browseTable: typeof appDatabase.browseTable;
}

export const controlPlaneDataBrowserApi: DataBrowserApi = {
  listBrowsableDatabases: appDatabase.listBrowsableDatabases,
  describeDatabase: appDatabase.describeDatabase,
  browseTable: appDatabase.browseTable,
};

const scopedApis = new WeakMap<Transport, DataBrowserApi>();

/**
 * The real client on `transport` (a TransportScope's, lib/transportScope.tsx),
 * memoized per transport. The app's transport is controlPlaneDataBrowserApi.
 */
export function dataBrowserApiOf(transport: Transport): DataBrowserApi {
  if (transport === appTransport) return controlPlaneDataBrowserApi;
  let api = scopedApis.get(transport);
  if (!api) {
    api = appDatabase.dataBrowserReadsOn(transport);
    scopedApis.set(transport, api);
  }
  return api;
}

const DataBrowserApiContext = createContext<DataBrowserApi | null>(null);

export function DataBrowserApiProvider({ api, children }: { api: DataBrowserApi; children: ReactNode }) {
  return <DataBrowserApiContext.Provider value={api}>{children}</DataBrowserApiContext.Provider>;
}

/** The provided api, else the real client on the nearest TransportScope's transport, else on the app's. */
export function useDataBrowserApi(): DataBrowserApi {
  const provided = useContext(DataBrowserApiContext);
  const transport = useTransport(appTransport);
  return provided ?? dataBrowserApiOf(transport);
}
