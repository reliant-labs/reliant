// Copyright (c) 2025 Reliant Labs

/**
 * The forge demo bundle's root (demoBundle/main.tsx): the demo shell's router under
 * the app's query client and error boundary. No auth provider, no settings
 * hydration, no daemon: the demo shell has nothing to sign in to.
 */

import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";

import ErrorBoundary from "@/components/ErrorBoundary";
import { router } from "@/demoShellRoutes";
import { queryClient } from "@/lib/query-client";

export function DemoRoot() {
  return (
    <QueryClientProvider client={queryClient}>
      <ErrorBoundary>
        <RouterProvider router={router} />
      </ErrorBoundary>
    </QueryClientProvider>
  );
}
