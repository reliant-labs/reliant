// Copyright (c) 2026 Reliant Labs

/**
 * Renders a Tests view against the fixture TestsService (fixtures.ts), served
 * on a mock transport in a TransportScope with a fresh query client and no
 * retries, so the pages' generated reads reach the fixture and never the
 * app's transport; and a Tests page in a real router so a test can read what
 * the URL holds after a click.
 */

import { useState, type ReactNode } from "react";
import { render } from "@testing-library/react";
import { QueryClient } from "@tanstack/react-query";
import { createMemoryHistory, createRootRoute, createRoute, createRouter, Outlet, RouterProvider, type AnyRoute } from "@tanstack/react-router";

import { TestsService } from "@/gen/controlplane/services/tests/v1/tests_pb";
import { mockTransport, type MockHandlers } from "@/lib/test-utils";
import { createTransportScope, TransportScopeProvider, type TransportScope } from "@/lib/transportScope";

import type { FixtureTests } from "../fixtures";

/** A scope whose transport answers TestsService from `fixture` (and refuses every other rpc, Unimplemented). */
export function fixtureScope(fixture: FixtureTests, queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })): TransportScope {
  const handlers: MockHandlers = {};
  for (const [method, handler] of Object.entries(fixture.handlers)) {
    handlers[`${TestsService.typeName}/${TestsService.method[method as keyof typeof TestsService.method].name}`] = handler as never;
  }
  return createTransportScope({
    id: "tests-fixture",
    transport: mockTransport(handlers),
    queryClient,
  });
}

/** `children` in a scope over `fixture`: one scope, and so one cache, for the provider's life. */
export function TestsFixtureProvider({ fixture, queryClient, children }: { fixture: FixtureTests; queryClient?: QueryClient; children: ReactNode }) {
  const [scope] = useState(() => fixtureScope(fixture, queryClient));
  return <TransportScopeProvider scope={scope}>{children}</TransportScopeProvider>;
}

export function renderWithTests(fixture: FixtureTests, children: ReactNode) {
  return render(<TestsFixtureProvider fixture={fixture}>{children}</TestsFixtureProvider>);
}

/**
 * One console page at `path` (a project route such as
 * "/forge/p/$project/tests/runs"), entered at `url`, with the route's own
 * search schema: what the console's route tree gives the page.
 */
export function renderRoutedPage(
  fixture: FixtureTests,
  { path, url, validateSearch, component }: { path: string; url: string; validateSearch: (search: Record<string, unknown>) => unknown; component: () => ReactNode }
) {
  const root = createRootRoute({ component: () => <Outlet /> });
  const route = createRoute({ getParentRoute: () => root, path, validateSearch: validateSearch as never, component }) as unknown as AnyRoute;
  const router = createRouter({ routeTree: root.addChildren([route]), history: createMemoryHistory({ initialEntries: [url] }) });
  const utils = renderWithTests(fixture, <RouterProvider router={router} />);
  return { ...utils, router, search: () => router.state.location.search as Record<string, unknown> };
}
