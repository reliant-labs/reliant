"use client";
// The client a hook talks through. A provider scopes it to a subtree; an app
// that has one client for its whole life registers it as the default instead
// (setDefaultReliantClient), so the hooks work with no provider mounted.

import { createContext, createElement, useContext, type ReactNode } from "react";
import type { ReliantClient } from "../client/client.js";

const ReliantContext = createContext<ReliantClient | null>(null);
let defaultClient: ReliantClient | null = null;

/** Register the client hooks use when no `ReliantProvider` is above them. */
export function setDefaultReliantClient(client: ReliantClient | null): void {
  defaultClient = client;
}

export function ReliantProvider({ client, children }: { client: ReliantClient; children?: ReactNode }): ReactNode {
  return createElement(ReliantContext.Provider, { value: client }, children);
}

/** The client of the nearest provider, else the registered default. */
export function useReliantClient(): ReliantClient {
  const client = useContext(ReliantContext) ?? defaultClient;
  if (!client) {
    throw new Error("@reliantlabs/sdk/react: mount <ReliantProvider client={...}> or call setDefaultReliantClient()");
  }
  return client;
}
