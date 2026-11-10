// Copyright (c) 2025 Reliant Labs

/**
 * The seam between the data browser (components/Forge/Data) and the three
 * AppDatabaseService reads it makes: ListBrowsableDatabases, DescribeDatabase
 * and BrowseTable.
 *
 * The same shape as ObservabilityApiContext, kept separate because the data
 * browser is not an observability view: component tests and the dev-only
 * preview (/forge-data-preview) hand it the in-memory fixture
 * (Data/__fixtures__/fixtureDataApi.ts), and production never provides
 * anything but the real client, which calls the control plane directly with
 * the user's session (never through the daemon: services/forge/live.ts).
 */

import { createContext, useContext, type ReactNode } from "react";

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

const DataBrowserApiContext = createContext<DataBrowserApi>(controlPlaneDataBrowserApi);

export function DataBrowserApiProvider({ api, children }: { api: DataBrowserApi; children: ReactNode }) {
  return <DataBrowserApiContext.Provider value={api}>{children}</DataBrowserApiContext.Provider>;
}

export function useDataBrowserApi(): DataBrowserApi {
  return useContext(DataBrowserApiContext);
}
