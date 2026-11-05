import { lazy, Suspense } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, type AnyRouter } from "@tanstack/react-router";
import { queryClient } from "../lib/query-client";
import { isForgeShell } from "../lib/shell";
import ErrorBoundary from "./ErrorBoundary";
import { AuthInitializer } from "./AuthInitializer";
import { LoadingSpinner } from "./Layout/LoadingSpinner";
import { useThemeInitialization } from "../hooks/useThemeInitialization";
import { useSettingsHydration } from "../hooks/useSettingsHydration";
import { AuthContextProvider } from "../lib/auth/context";
import { createSupabaseAuthProvider } from "../lib/auth/supabase-provider";
import { EventBusProvider } from "../lib/event-context";

const authProvider = createSupabaseAuthProvider();

// The route tree is the shell's (lib/shell.ts): Reliant's, or the forge
// shell's on the forge host. Each is its own chunk, so the forge shell never
// downloads Reliant's chats and overlays, nor Reliant the forge shell's
// pages. The import starts when this module evaluates — during boot, beside
// the config wait — so it is not a waterfall behind the first render.
const routerModule: Promise<{ router: AnyRouter }> = isForgeShell()
  ? import("../forgeShellRoutes")
  : import("../routes");

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
          <AuthContextProvider provider={authProvider}>
            <AuthInitializer>
              <Suspense fallback={<LoadingSpinner />}>
                <ShellRouter />
              </Suspense>
            </AuthInitializer>
          </AuthContextProvider>
        </ErrorBoundary>
      </QueryClientProvider>
    </EventBusProvider>
  );
}
