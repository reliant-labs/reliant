import { QueryClient } from "@tanstack/react-query";
import { shouldRetryQuery } from "./queryRetry";

/**
 * A QueryClient with the app's defaults. The app has one (`queryClient`
 * below); a transport scope (lib/transportScope.tsx) builds its own from the
 * same factory, so a scoped subtree caches and retries exactly as the rest of
 * the app does.
 */
export function createAppQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        // Data is fresh for 30 seconds — prevents unnecessary refetches on remount
        staleTime: 30_000,
        // Keep unused queries in cache for 5 minutes
        gcTime: 5 * 60_000,
        // Refetch when window regains focus (useful for long-running tasks)
        refetchOnWindowFocus: true,
        // Skip the retry ladder for errors a retry cannot fix — auth failures
        // and not-found. Branches on the ConnectError code, not on the message
        // text; see lib/queryRetry.ts for why the message never matched.
        retry: shouldRetryQuery,
      },
    },
  });
}

export const queryClient = createAppQueryClient();
