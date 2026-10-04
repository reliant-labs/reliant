import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  triggerGrpc,
  type Trigger,
  type TriggerDefinitionInput,
} from "../api/trigger-grpc";
import { workflowGrpc } from "../api/workflow-grpc";

export type { Trigger, TriggerEvent, TriggerDefinitionInput } from "../api/trigger-grpc";

// ── Key factory ─────────────────────────────────────────────────────────────

export const triggerKeys = {
  all: ["triggers"] as const,
  lists: () => [...triggerKeys.all, "list"] as const,
  /** `undefined` is "every project", which is what the Automations page lists. */
  list: (projectId?: string) => [...triggerKeys.lists(), projectId ?? null] as const,
  details: () => [...triggerKeys.all, "detail"] as const,
  detail: (id: string) => [...triggerKeys.details(), id] as const,
  events: (id: string) => [...triggerKeys.all, "events", id] as const,
  workflows: (projectId: string) => [...triggerKeys.all, "workflows", projectId] as const,
};

// ── Query hooks ─────────────────────────────────────────────────────────────

/** The caller's automations, optionally narrowed to one project. */
export function useTriggers(projectId?: string) {
  return useQuery({
    queryKey: triggerKeys.list(projectId),
    queryFn: () => triggerGrpc.list(projectId),
    // next_fire_at and last_event move on their own as schedules fire, so a
    // page left open should not show a stale "next run" indefinitely.
    refetchInterval: 60_000,
  });
}

export function useTrigger(id?: string) {
  return useQuery({
    queryKey: triggerKeys.detail(id ?? ""),
    queryFn: () => triggerGrpc.get(id!),
    enabled: !!id,
    refetchInterval: 60_000,
  });
}

export function useTriggerEvents(
  id?: string,
  options: { refetchInterval?: number | false } = {},
) {
  return useQuery({
    queryKey: triggerKeys.events(id ?? ""),
    queryFn: () => triggerGrpc.listEvents(id!),
    enabled: !!id,
    refetchInterval: options.refetchInterval ?? 60_000,
  });
}

/**
 * Workflows available in a given project, for the automation form.
 *
 * Not the global `useWorkflows()` store: that one follows the CURRENT project,
 * while an automation's workflow has to be chosen from the project the form
 * has selected, which may be a different one.
 */
export function useProjectWorkflowList(projectId?: string) {
  return useQuery({
    queryKey: triggerKeys.workflows(projectId ?? ""),
    queryFn: () => workflowGrpc.listWorkflows(projectId!),
    enabled: !!projectId,
    staleTime: 60_000,
  });
}

// ── Mutation hooks ──────────────────────────────────────────────────────────

/** Write a fresh trigger into every cache that shows it. */
function useStoreTrigger() {
  const queryClient = useQueryClient();
  return (trigger: Trigger) => {
    queryClient.setQueryData(triggerKeys.detail(trigger.id), trigger);
    void queryClient.invalidateQueries({ queryKey: triggerKeys.lists() });
  };
}

export function useCreateTrigger() {
  const storeTrigger = useStoreTrigger();
  return useMutation({
    mutationFn: (input: TriggerDefinitionInput) => triggerGrpc.create(input),
    onSuccess: storeTrigger,
  });
}

export function useUpdateTrigger() {
  const storeTrigger = useStoreTrigger();
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: TriggerDefinitionInput }) =>
      triggerGrpc.update(id, input),
    onSuccess: storeTrigger,
  });
}

export function useSetTriggerEnabled() {
  const queryClient = useQueryClient();
  const storeTrigger = useStoreTrigger();
  return useMutation({
    mutationFn: ({ id, enabled }: { id: string; enabled: boolean }) =>
      triggerGrpc.setEnabled(id, enabled),
    // Flip the switch immediately: a toggle that lags a round-trip reads as
    // having ignored the click.
    onMutate: async ({ id, enabled }) => {
      await queryClient.cancelQueries({ queryKey: triggerKeys.all });
      const previousLists = queryClient.getQueriesData<Trigger[]>({ queryKey: triggerKeys.lists() });
      const previousDetail = queryClient.getQueryData<Trigger>(triggerKeys.detail(id));
      queryClient.setQueriesData<Trigger[]>({ queryKey: triggerKeys.lists() }, (list) =>
        list?.map((t) => (t.id === id ? { ...t, enabled } : t)),
      );
      if (previousDetail) {
        queryClient.setQueryData(triggerKeys.detail(id), { ...previousDetail, enabled });
      }
      return { previousLists, previousDetail };
    },
    onError: (_error, { id }, context) => {
      context?.previousLists.forEach(([key, data]) => queryClient.setQueryData(key, data));
      if (context?.previousDetail) {
        queryClient.setQueryData(triggerKeys.detail(id), context.previousDetail);
      }
    },
    onSuccess: storeTrigger,
  });
}

export function useDeleteTrigger() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => triggerGrpc.delete(id),
    onSuccess: (_data, id) => {
      queryClient.removeQueries({ queryKey: triggerKeys.detail(id) });
      queryClient.removeQueries({ queryKey: triggerKeys.events(id) });
      queryClient.setQueriesData<Trigger[]>({ queryKey: triggerKeys.lists() }, (list) =>
        list?.filter((t) => t.id !== id),
      );
    },
  });
}

/**
 * Run a trigger now. The fire is asynchronous on the server — the response is
 * only the fire workflow's id — so success here means "started", and the
 * outcome arrives later as a new event.
 */
export function useFireTrigger() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => triggerGrpc.fire(id),
    onSuccess: (_fireWorkflowId, id) => {
      void queryClient.invalidateQueries({ queryKey: triggerKeys.events(id) });
    },
  });
}
