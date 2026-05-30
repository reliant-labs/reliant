import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  listDaemons,
  createDaemon,
  resumeDaemon,
  type CreateDaemonArgs,
} from '@/services/controlPlane/daemon';
import { getReliantState, isCloudEligible } from '@/services/controlPlane/billing';
import { gitService } from '@/services/controlPlane/git';
import type { CloneRepoArgs } from '@/services/controlPlane/git/types';
import { onboardingService } from '@/services/controlPlane/onboarding';
import type { OnboardingUser } from '@/services/controlPlane/onboarding';

export function useCurrentUser() {
  return useQuery<OnboardingUser | null>({
    queryKey: ['onboarding', 'currentUser'],
    queryFn: () => onboardingService.getCurrentUser(),
    staleTime: 30_000,
    // A focus refetch flips isLoading back to true mid-click and re-disables
    // the Start cloud daemon button, eating the first click.
    refetchOnWindowFocus: false,
  });
}

export function useCloudEligibility() {
  const { data: user, isLoading: userLoading } = useCurrentUser();
  const { data: state, isLoading: stateLoading } = useQuery({
    queryKey: ['onboarding', 'reliantState'],
    queryFn: () => getReliantState(),
    staleTime: 30_000,
    refetchOnWindowFocus: false,
  });

  const isLoading = userLoading || stateLoading;
  const entitlement = state?.entitlement;
  const eligible = !isLoading && isCloudEligible(entitlement);

  const reason = !user
    ? 'Sign up required'
    : !isCloudEligible(entitlement)
      ? 'No cloud credits available'
      : null;

  return { eligible, reason, isLoading };
}

export function useDaemonList() {
  return useQuery({
    queryKey: ['onboarding', 'daemons'],
    queryFn: async () => {
      const { daemons } = await listDaemons();
      return daemons;
    },
    staleTime: 10_000,
  });
}

export function useCreateDaemon() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (args: CreateDaemonArgs) => createDaemon(args),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['onboarding', 'daemons'] });
    },
  });
}

export function useResumeDaemon() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => resumeDaemon(name),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['onboarding', 'daemons'] });
    },
  });
}

export function useGitRepos() {
  return useQuery({
    queryKey: ['onboarding', 'gitRepos'],
    queryFn: () => gitService.listRepos(1, 100, 'updated'),
    enabled: false, // manually triggered
  });
}

export function useCloneRepo() {
  return useMutation({
    mutationFn: (args: CloneRepoArgs) => gitService.cloneRepo(args),
  });
}

export function useCompleteOnboarding() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: Record<string, unknown>) =>
      onboardingService.completeOnboarding(data),
    onSuccess: () => {
      // Optimistically mark onboarding complete so ModernApp doesn't redirect
      // back to ?step=goal before the refetch completes.
      queryClient.setQueryData<OnboardingUser | null>(
        ['onboarding', 'currentUser'],
        (old) => ({
          ...(old ?? { onboardingCompleted: false }),
          onboardingCompleted: true,
        }),
      );
      queryClient.invalidateQueries({ queryKey: ['onboarding', 'currentUser'] });
    },
  });
}
