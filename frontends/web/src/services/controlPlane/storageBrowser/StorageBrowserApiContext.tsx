// Copyright (c) 2025 Reliant Labs

/**
 * The seam between the storage browser (components/Forge/Storage) and the
 * five StorageBrowserService calls it makes.
 *
 * The same shape as the data browser's DataBrowserApiContext: component tests
 * hand it the in-memory fixture (Storage/__fixtures__/fixtureStorageApi.ts),
 * and production (and the dev-only preview, /forge-storage-preview, over the
 * shop fixture pack's transport) never provides anything but the real
 * client, which calls the control plane
 * directly with the user's session (never through the daemon). The default
 * is that client on the nearest TransportScope's transport, else on the app's.
 */

import { createContext, useContext, type ReactNode } from "react";
import type { Transport } from "@connectrpc/connect";

import { transport as appTransport } from "@/lib/connect";
import { useTransport } from "@/lib/transportScope";

import * as storageBrowser from "./storageBrowserApi";

export interface StorageBrowserApi {
  listBrowsableBuckets: typeof storageBrowser.listBrowsableBuckets;
  listObjects: typeof storageBrowser.listObjects;
  getObjectMetadata: typeof storageBrowser.getObjectMetadata;
  presignObjectDownload: typeof storageBrowser.presignObjectDownload;
  previewObject: typeof storageBrowser.previewObject;
}

export const controlPlaneStorageBrowserApi: StorageBrowserApi = {
  listBrowsableBuckets: storageBrowser.listBrowsableBuckets,
  listObjects: storageBrowser.listObjects,
  getObjectMetadata: storageBrowser.getObjectMetadata,
  presignObjectDownload: storageBrowser.presignObjectDownload,
  previewObject: storageBrowser.previewObject,
};

const scopedApis = new WeakMap<Transport, StorageBrowserApi>();

/**
 * The real client on `transport` (a TransportScope's, lib/transportScope.tsx),
 * memoized per transport. The app's transport is controlPlaneStorageBrowserApi.
 */
export function storageBrowserApiOf(transport: Transport): StorageBrowserApi {
  if (transport === appTransport) return controlPlaneStorageBrowserApi;
  let api = scopedApis.get(transport);
  if (!api) {
    api = storageBrowser.storageBrowserCallsOn(transport);
    scopedApis.set(transport, api);
  }
  return api;
}

const StorageBrowserApiContext = createContext<StorageBrowserApi | null>(null);

export function StorageBrowserApiProvider({ api, children }: { api: StorageBrowserApi; children: ReactNode }) {
  return <StorageBrowserApiContext.Provider value={api}>{children}</StorageBrowserApiContext.Provider>;
}

/** The provided api, else the real client on the nearest TransportScope's transport, else on the app's. */
export function useStorageBrowserApi(): StorageBrowserApi {
  const provided = useContext(StorageBrowserApiContext);
  const transport = useTransport(appTransport);
  return provided ?? storageBrowserApiOf(transport);
}
