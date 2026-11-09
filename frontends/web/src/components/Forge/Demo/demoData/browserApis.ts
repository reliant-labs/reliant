// Copyright (c) 2025 Reliant Labs

/** Every demo shell uses these isolated, auth-free synthetic clients, even when
 * the surrounding product has a real control-plane session. No network fallback. */
import { createClient } from "@connectrpc/connect";
import { AppDatabaseService } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { StorageBrowserService } from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import { createDataBrowserApi } from "@/services/controlPlane/observability/appDatabaseApi";
import { createStorageBrowserApi } from "@/services/controlPlane/storageBrowser/storageBrowserApi";
import { createDemoTransport } from "./demoTransport";

export function createDemoBrowserApis() {
  const transport = createDemoTransport({ source: "fixtures", url: "" });
  return {
    data: createDataBrowserApi(createClient(AppDatabaseService, transport)),
    storage: createStorageBrowserApi(createClient(StorageBrowserService, transport)),
  };
}
