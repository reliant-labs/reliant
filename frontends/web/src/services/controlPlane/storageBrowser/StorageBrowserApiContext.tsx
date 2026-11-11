// Copyright (c) 2025 Reliant Labs

/**
 * The seam between the storage browser (components/Forge/Storage) and the
 * five StorageBrowserService calls it makes.
 *
 * The same shape as the data browser's DataBrowserApiContext: component tests
 * and the dev-only preview (/forge-storage-preview) hand it the in-memory
 * fixture (Storage/__fixtures__/fixtureStorageApi.ts), and production never
 * provides anything but the real client, which calls the control plane
 * directly with the user's session (never through the daemon).
 */

import { createContext, useContext, type ReactNode } from "react";

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

const StorageBrowserApiContext = createContext<StorageBrowserApi>(controlPlaneStorageBrowserApi);

export function StorageBrowserApiProvider({ api, children }: { api: StorageBrowserApi; children: ReactNode }) {
  return <StorageBrowserApiContext.Provider value={api}>{children}</StorageBrowserApiContext.Provider>;
}

export function useStorageBrowserApi(): StorageBrowserApi {
  return useContext(StorageBrowserApiContext);
}
