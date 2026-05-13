/**
 * useGitHubCredential
 *
 * Canonical hook for "does the user have a GitHub git credential available?"
 *
 * This is the credential-based check — i.e. whether the control-plane has a
 * usable GitHub token for the user. It is the correct gate for any "can we
 * do git things?" decision. Do NOT use Supabase identity presence
 * (`user.identities.some(i => i.provider === 'github')`) for that purpose —
 * identity linkage is not the same as having a usable git credential.
 *
 * Error handling:
 *   - In local-only mode (no control plane configured) the local git service
 *     returns `available: false` without throwing, so the normal data path
 *     yields `hasToken: false`. We additionally swallow any "service is
 *     unavailable in local-only mode" error defensively.
 *   - Auth failures (ConnectError with Code.Unauthenticated) are surfaced via
 *     React Query's `isError`. The user is logged out — not credential-less —
 *     and consumers can distinguish via `isError`.
 *   - Other errors are re-thrown so React Query can retry with backoff.
 */
import { useCallback } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ConnectError, Code } from "@connectrpc/connect";

import { gitService } from "@/services/controlPlane/git";
import { hasControlPlane } from "@/services/controlPlane/config";
import type { GitCredentialStatus } from "@/services/controlPlane/git";

export const GITHUB_CREDENTIAL_QUERY_KEY = ["gitCredential", "github"] as const;

export function useGitHubCredential(): {
  hasToken: boolean;
  scopes: string;
  isLoading: boolean;
  isError: boolean;
  refresh: () => Promise<void>;
} {
  const queryClient = useQueryClient();

  const { data, isLoading, isError } = useQuery<GitCredentialStatus>({
    queryKey: GITHUB_CREDENTIAL_QUERY_KEY,
    queryFn: async () => {
      try {
        return await gitService.getCredential("github");
      } catch (err) {
        // Local-only / no control plane → no-op. The local implementation
        // normally returns `available: false` without throwing, but if a
        // future change throws (e.g. cloud-only methods), keep the silent
        // fallback so consumers don't see a spurious error here.
        if (!hasControlPlane) {
          return {
            available: false,
            hasToken: false,
            provider: "github",
            scopes: "",
          };
        }
        // Auth failures and other real errors → let React Query surface them.
        // `Code.Unauthenticated` means the user is logged out, which is
        // distinct from "credential not set" and callers can check `isError`.
        if (err instanceof ConnectError && err.code === Code.Unauthenticated) {
          throw err;
        }
        throw err;
      }
    },
    staleTime: 30_000,
    refetchOnWindowFocus: true,
  });

  const refresh = useCallback(async () => {
    await queryClient.invalidateQueries({
      queryKey: GITHUB_CREDENTIAL_QUERY_KEY,
    });
  }, [queryClient]);

  return {
    hasToken: data?.hasToken ?? false,
    scopes: data?.scopes ?? "",
    isLoading,
    isError,
    refresh,
  };
}
