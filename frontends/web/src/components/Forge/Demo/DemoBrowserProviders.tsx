// Copyright (c) 2025 Reliant Labs

/**
 * What the Data and Storage browsers (and the Users page) need from context
 * in a demo, used by both the embedded demo panel (DemoConsole) and the demo
 * host's deep-linked pages (demoBundle/DemoConsoleProviders.tsx).
 *
 * THE READS. On the public demo host the shell transport IS the shop fixture
 * pack's (demoShellBoot.ts), so DataBrowser, StorageBrowser and Identity run
 * their real default clients (`connectClient` over it) and nothing is
 * provided here: the pack answers the reads (src/fixtures/shop/handlers:
 * inventory.ts, storage.ts, organization.ts) and the transport's read-only
 * guard refuses every write. Reliant's own /forge/demo (and the forge
 * shell's) are the exception: there the shell transport is the real backend,
 * so the two browsers DemoConsole draws are handed clients over a transport
 * of the same pack, until a transport can be scoped to a subtree (design P4,
 * TransportScope). That panel mounts no ForgeConsoleContext, so nothing in it
 * reads Identity.
 *
 * THE READER. The browsers key their caches by, and abort reads across, the
 * reader session (AuditedAccess/readerSession.ts). That is the browser's own
 * auth epoch, not an RPC: nobody is signed in to a demo, and a signed-out
 * session is no reader at all (requireCurrentReader refuses it). So the demo
 * keeps a synthetic reader of its own (FixtureReaderProvider), which also
 * keeps a real session's principal and epoch out of the demo's cache keys.
 */

import { useState, type ReactNode } from "react";
import { createClient } from "@connectrpc/connect";

import { createShopTransport } from "@/fixtures/shop";
import { AppDatabaseService } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { StorageBrowserService } from "@/gen/controlplane/services/storage_browser/v1/storage_browser_pb";
import { isDemoShell } from "@/lib/shell";
import { createDataBrowserApi } from "@/services/controlPlane/observability/appDatabaseApi";
import { DataBrowserApiProvider } from "@/services/controlPlane/observability/DataBrowserApiContext";
import { createStorageBrowserApi } from "@/services/controlPlane/storageBrowser/storageBrowserApi";
import { StorageBrowserApiProvider } from "@/services/controlPlane/storageBrowser/StorageBrowserApiContext";

import { createFixtureReader, FixtureReaderProvider } from "../AuditedAccess/readerSession";

export function DemoBrowserProviders({ children }: { children: ReactNode }) {
  const [reader] = useState(createFixtureReader);
  return <FixtureReaderProvider reader={reader}>{isDemoShell() ? children : <PackBrowsers>{children}</PackBrowsers>}</FixtureReaderProvider>;
}

/** The two browsers' clients over the shop pack, for a shell whose own transport is the real backend. */
function PackBrowsers({ children }: { children: ReactNode }) {
  const [apis] = useState(() => {
    const transport = createShopTransport();
    return {
      data: createDataBrowserApi(createClient(AppDatabaseService, transport)),
      storage: createStorageBrowserApi(createClient(StorageBrowserService, transport)),
    };
  });
  return <DataBrowserApiProvider api={apis.data}><StorageBrowserApiProvider api={apis.storage}>{children}</StorageBrowserApiProvider></DataBrowserApiProvider>;
}
