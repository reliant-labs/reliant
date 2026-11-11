// Copyright (c) 2025 Reliant Labs
import { useState, type ReactNode } from "react";
import { DataBrowserApiProvider } from "@/services/controlPlane/observability/DataBrowserApiContext";
import { StorageBrowserApiProvider } from "@/services/controlPlane/storageBrowser/StorageBrowserApiContext";
import { IdentityApiProvider } from "@/services/controlPlane/identity/IdentityApiContext";
import { createFixtureReader, FixtureReaderProvider } from "../AuditedAccess/readerSession";
import { createDemoBrowserApis } from "./demoData/browserApis";

/** Used by both the embedded demo panel and the demo host's deep-linked pages. */
export function DemoBrowserProviders({ children }: { children: ReactNode }) {
  const [apis] = useState(createDemoBrowserApis);
  const [reader] = useState(createFixtureReader);
  return <FixtureReaderProvider reader={reader}><DataBrowserApiProvider api={apis.data}><StorageBrowserApiProvider api={apis.storage}><IdentityApiProvider api={apis.identity}>{children}</IdentityApiProvider></StorageBrowserApiProvider></DataBrowserApiProvider></FixtureReaderProvider>;
}
