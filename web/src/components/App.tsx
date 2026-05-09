import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { router } from "../routes";
import { queryClient } from "../lib/query-client";
import { SentryErrorBoundary } from "./ErrorBoundary";
import { AuthInitializer } from "./AuthInitializer";
import { useThemeInitialization } from "../hooks/useThemeInitialization";
import { useSettingsHydration } from "../hooks/useSettingsHydration";
import { AuthContextProvider } from "../lib/auth/context";
import { createSupabaseAuthProvider } from "../lib/auth/supabase-provider";
import { EventBusProvider } from "../lib/event-context";

const authProvider = createSupabaseAuthProvider();

export function App() {
  // Initialize theme from database
  useThemeInitialization();
  
  // Hydrate Zustand stores from database
  useSettingsHydration();

  return (
    <EventBusProvider devMode={import.meta.env.DEV}>
      <QueryClientProvider client={queryClient}>
        <SentryErrorBoundary>
          <AuthContextProvider provider={authProvider}>
            <AuthInitializer>
              <RouterProvider router={router} />
            </AuthInitializer>
          </AuthContextProvider>
        </SentryErrorBoundary>
      </QueryClientProvider>
    </EventBusProvider>
  );
}