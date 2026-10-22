/**
 * `render` for components that reach React Query somewhere in their subtree.
 *
 * The app always has a provider (see components/App.tsx), so a component that
 * calls `useQuery` is correct — but a test that renders it bare is not, and
 * fails with "No QueryClient set". This wraps each render in a throwaway
 * client so tests exercise the same tree the app builds.
 *
 * Retries are off and there is no cache lifetime: a test asserting an error
 * state shouldn't wait out a backoff, and no query should outlive the test
 * that created it.
 */

import type { ReactElement, ReactNode } from "react";
import { render, type RenderOptions, type RenderResult } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

export function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, staleTime: 0 },
      mutations: { retry: false },
    },
  });
}

export function renderWithQuery(
  ui: ReactElement,
  options?: Omit<RenderOptions, "wrapper">,
): RenderResult & { queryClient: QueryClient } {
  const queryClient = createTestQueryClient();
  const result = render(ui, {
    ...options,
    wrapper: ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    ),
  });
  return { ...result, queryClient };
}
