// Copyright (c) 2025 Reliant Labs

/**
 * What the Data and Storage browsers (and the Users page) need from context
 * in the demo, for the demo's own pages (DemoConsole) and the demo host's
 * deep-linked console pages (demoBundle/DemoConsoleProviders.tsx). Both mount
 * only in the demo shell (demoShellRoutes.tsx); the app's /forge/demo leaves
 * for it (demoRoute.tsx).
 *
 * THE READS. The demo shell's transport IS the shop fixture pack's
 * (demoShellBoot.ts), so DataBrowser, StorageBrowser and Identity run their
 * real default clients (`connectClient` over it) and nothing is provided
 * here: the pack answers the reads (src/fixtures/shop/handlers: inventory.ts,
 * storage.ts, organization.ts) and the transport's read-only guard refuses
 * every write.
 *
 * THE READER. The browsers key their caches by, and abort reads across, the
 * reader session (AuditedAccess/readerSession.ts). That is the browser's own
 * auth epoch, not an RPC: nobody is signed in to a demo, and a signed-out
 * session is no reader at all (requireCurrentReader refuses it). So the demo
 * keeps a synthetic reader of its own (FixtureReaderProvider), which also
 * keeps a real session's principal and epoch out of the demo's cache keys.
 */

import { useState, type ReactNode } from "react";

import { createFixtureReader, FixtureReaderProvider } from "../AuditedAccess/readerSession";

export function DemoBrowserProviders({ children }: { children: ReactNode }) {
  const [reader] = useState(createFixtureReader);
  return <FixtureReaderProvider reader={reader}>{children}</FixtureReaderProvider>;
}
