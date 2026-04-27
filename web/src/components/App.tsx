import { RouterProvider } from "@tanstack/react-router";
import { router } from "../routes";
import { SentryErrorBoundary } from "./ErrorBoundary";
import { AuthInitializer } from "./AuthInitializer";
import { useThemeInitialization } from "../hooks/useThemeInitialization";
import { useSettingsHydration } from "../hooks/useSettingsHydration";

export function App() {
  // Initialize theme from database
  useThemeInitialization();
  
  // Hydrate Zustand stores from database
  useSettingsHydration();

  return (
    <SentryErrorBoundary>
      <AuthInitializer>
        <RouterProvider router={router} />
      </AuthInitializer>
    </SentryErrorBoundary>
  );
}