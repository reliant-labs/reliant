import { RouterProvider } from "@tanstack/react-router";
import { router } from "../routes";
import { SentryErrorBoundary } from "./ErrorBoundary";
import { AuthInitializer } from "./AuthInitializer";
import { useThemeInitialization } from "../hooks/useThemeInitialization";
import { useSettingsHydration } from "../hooks/useSettingsHydration";
import { FeedbackModal } from "./Feedback/FeedbackModal";

export function App() {
  // Initialize theme from database
  useThemeInitialization();
  
  // Hydrate Zustand stores from database
  useSettingsHydration();

  return (
    <SentryErrorBoundary>
      {/* Mounted at the top-level so it works even when the router shows an error boundary */}
      <FeedbackModal />
      <AuthInitializer>
        <RouterProvider router={router} />
      </AuthInitializer>
    </SentryErrorBoundary>
  );
}
