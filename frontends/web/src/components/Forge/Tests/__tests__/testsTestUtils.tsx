// Copyright (c) 2026 Reliant Labs

/**
 * Renders a Tests view against the fixture api (fixtures.ts), in a fresh
 * query client with no retries, and a Tests page in a real router so a test
 * can read what the URL holds after a click.
 */

import type { ReactNode } from "react";
import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider, type AnyRoute } from "@tanstack/react-router";

import { TestApiProvider, type TestApi } from "@/services/controlPlane/tests/TestApiContext";

export function renderWithApi(api: TestApi, children: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <TestApiProvider api={api}>{children}</TestApiProvider>
    </QueryClientProvider>
  );
}

/**
 * One console page at `path` (a project route such as
 * "/forge/p/$project/tests/runs"), entered at `url`, with the route's own
 * search schema: what the console's route tree gives the page.
 */
export function renderRoutedPage(
  api: TestApi,
  { path, url, validateSearch, component }: { path: string; url: string; validateSearch: (search: Record<string, unknown>) => unknown; component: () => ReactNode }
) {
  const root = createRootRoute({ component: () => <Outlet /> });
  const route = createRoute({ getParentRoute: () => root, path, validateSearch: validateSearch as never, component }) as unknown as AnyRoute;
  const router = createRouter({ routeTree: root.addChildren([route]), history: createMemoryHistory({ initialEntries: [url] }) });
  const utils = renderWithApi(api, <RouterProvider router={router} />);
  return { ...utils, router, search: () => router.state.location.search as Record<string, unknown> };
}
