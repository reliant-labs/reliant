import { lazy, Suspense } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, type AnyRouter } from "@tanstack/react-router";
import { queryClient } from "../lib/query-client";
import { getShell } from "../lib/shell";
import ErrorBoundary from "./ErrorBoundary";
import { AuthInitializer } from "./AuthInitializer";
import { LoadingSpinner } from "./Layout/LoadingSpinner";
import { useThemeInitialization } from "../hooks/useThemeInitialization";
import { useSettingsHydration } from "../hooks/useSettingsHydration";
import { AuthStoreSession } from "../lib/auth/authStoreSession";
import { EventBusProvider } from "../lib/event-context";

// The route tree (lib/shell.ts): the app's one tree (routes.tsx) on both the
// app host and the forge host — the forge host is a brand of it, decided
// inside routes.tsx — or the demo's one route on the demo host. Each is its
// own chunk, so the public demo never downloads the app. The import starts
// when this module evaluates — during boot, beside the config wait — so it is
// not a waterfall behind the first render.
const routerModule: Promise<{ router: AnyRouter }> = getShell() === "demo" ? import("../demoShellRoutes") : import("../routes");

const ShellRouter = lazy(async () => {
  const { router } = await routerModule;
  return {
    default: function ShellRouterProvider() {
      return <RouterProvider router={router} />;
    },
  };
});

export function App() {
  // Initialize theme from database
  useThemeInitialization();
  
  // Hydrate Zustand stores from database
  useSettingsHydration();

  return (
    <EventBusProvider>
      <QueryClientProvider client={queryClient}>
        <ErrorBoundary>
          <AuthStoreSession>
            <AuthInitializer>
              <Suspense fallback={<LoadingSpinner />}>
                <ShellRouter />
              </Suspense>
            </AuthInitializer>
          </AuthStoreSession>
        </ErrorBoundary>
      </QueryClientProvider>
    </EventBusProvider>
  );
}
