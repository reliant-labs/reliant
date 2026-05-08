import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  getCurrentUser,
  listDaemons,
  createDaemon,
  resumeDaemon,
  listGitRepos,
  cloneRepo,
  completeOnboardingRPC,
  hasReliantCreditEligibility,
} from '@/components/OnboardingFlow/api';

export function useCurrentUser() {
  return useQuery({
    queryKey: ['onboarding', 'currentUser'],
    queryFn: async () => {
      const { user } = await getCurrentUser();
      return user ?? null;
    },
    staleTime: 30_000,
  });
}

export function useCloudEligibility() {
  const { data: user, isLoading } = useCurrentUser();
  const eligible = !isLoading && user ? hasReliantCreditEligibility(user) : false;
  const reason = !user ? 'Sign up required'
    : user.ipRestricted ? 'Cloud daemons not available from your network'
    : (user.globalBudgetAvailable === false || user.budgetAvailable === false) ? 'Cloud budget reached'
    : !hasReliantCreditEligibility(user) ? 'No cloud credits available'
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
    mutationFn: createDaemon,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['onboarding', 'daemons'] });
    },
  });
}

export function useResumeDaemon() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: resumeDaemon,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['onboarding', 'daemons'] });
    },
  });
}

export function useGitRepos(accountLogin?: string) {
  return useQuery({
    queryKey: ['onboarding', 'gitRepos', accountLogin],
    queryFn: () => listGitRepos(1, 100, 'updated', accountLogin),
    enabled: false, // manually triggered
  });
}

export function useCloneRepo() {
  return useMutation({ mutationFn: cloneRepo });
}

export function useCompleteOnboarding() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: completeOnboardingRPC,
    onSuccess: () => {
      // Optimistically mark onboarding complete so ModernApp doesn't redirect
      // back to ?step=goal before the refetch completes
      queryClient.setQueryData(['onboarding', 'currentUser'], (old: Record<string, unknown> | null) =>
        old ? { ...old, onboardingCompleted: true } : old,
      );
      queryClient.invalidateQueries({ queryKey: ['onboarding', 'currentUser'] });
    },
  });
}