import { QueryClient } from "@tanstack/react-query";

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Data is fresh for 30 seconds — prevents unnecessary refetches on remount
      staleTime: 30_000,
      // Keep unused queries in cache for 5 minutes
      gcTime: 5 * 60_000,
      // Refetch when window regains focus (useful for long-running tasks)
      refetchOnWindowFocus: true,
      // Don't retry on auth errors
      retry: (failureCount, error) => {
        // Don't retry on 401/403
        if (error instanceof Error && (error.message.includes("401") || error.message.includes("403"))) {
          return false;
        }
        return failureCount < 2;
      },
    },
  },
});
