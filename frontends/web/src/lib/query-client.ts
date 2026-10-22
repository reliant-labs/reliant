import { QueryClient } from "@tanstack/react-query";
import { shouldRetryQuery } from "./queryRetry";

export const queryClient = new QueryClient({
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
