/**
 * Wrap a tree in the app's singleton QueryClient (lib/query-client), the one
 * the imperative cache readers use (getCurrentWorktree, resolveWorktree, ...),
 * so a test that seeds that client and renders a component sees one cache.
 * Pair with `queryClient.clear()` in afterEach.
 */

import type { ReactElement, ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";

import { queryClient } from "@/lib/query-client";

export function withAppQueryClient(ui: ReactElement): ReactElement {
  return <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>;
}

export function AppQueryClientWrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}
