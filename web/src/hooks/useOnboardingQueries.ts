import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  listDaemons,
  createDaemon,
  resumeDaemon,
  listGitRepos,
  cloneRepo,
  hasReliantCreditEligibility,
  type ControlPlaneUser,
} from '@/components/OnboardingFlow/api';
import { onboardingService } from '@/services/controlPlane/onboarding';

export function useCurrentUser() {
  return useQuery<ControlPlaneUser | null>({
    queryKey: ['onboarding', 'currentUser'],
    queryFn: () => onboardingService.getCurrentUser(),
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
    mutationFn: (data: Record<string, unknown>) =>
      onboardingService.completeOnboarding(data),
    onSuccess: () => {
      // Optimistically mark onboarding complete so ModernApp doesn't redirect
      // back to ?step=goal before the refetch completes.
      queryClient.setQueryData<ControlPlaneUser | null>(
        ['onboarding', 'currentUser'],
        (old) => ({ ...(old ?? {}), onboardingCompleted: true }),
      );
      queryClient.invalidateQueries({ queryKey: ['onboarding', 'currentUser'] });
    },
  });
}
