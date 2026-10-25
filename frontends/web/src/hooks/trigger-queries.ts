// Copyright (c) 2025 Reliant Labs

/**
 * Automations (TriggerService) on forge's generated hooks and keys.
 *
 * Every read is a generated query under `triggerServiceKeys`, mapped to the
 * frontend model (`trigger-model.ts`) by a hoisted `select`, so the cache
 * holds the proto responses and every generated TriggerService write — which
 * invalidates `triggerServiceKeys.all` — refetches whatever is mounted. The
 * update stream invalidates the same prefix when a chat is created
 * (`triggersInvalidatingUpdate`): a fire that launched a run changes the
 * trigger's last event, health and next fire.
 *
 * The hooks here exist where a call site needs more than the generated hook:
 * the frontend model in and out of a write, an optimistic toggle, or a
 * refetch after a refused write.
 */

import { create, isMessage } from "@bufbuild/protobuf";
import { useQueryClient, type QueryClient, type QueryKey } from "@tanstack/react-query";

import { UserUpdateType } from "../gen/reliant/v1/streaming_pb";
import {
  GetTriggerResponseSchema,
  ListTriggersResponseSchema,
  TriggerService,
  type GetTriggerResponse,
  type ListTriggerEventsResponse,
  type ListTriggersResponse,
  type Trigger as ProtoTrigger,
} from "../gen/reliant/v1/trigger_pb";
import { connectClient } from "../lib/connect";
import {
  definitionToProto,
  enableWorkflowRequest,
  enableWorkflowResultFromProto,
  eventFromProto,
  previewFromProto,
  previewReplayRequest,
  replayCandidatesFromProto,
  resolvedSendersFromProto,
  triggerFromProto,
  triggerWithCredentialFromProto,
  type EnableWorkflowInput,
  type ResolvedSender,
  type Trigger,
  type TriggerDefinitionInput,
  type TriggerSender,
} from "./trigger-model";
import type { ReplayEventInput } from "./chat-model";
import {
  triggerServiceKeys,
  useCreateTrigger as useCreateTriggerRpc,
  useDeleteTrigger as useDeleteTriggerRpc,
  useDisableWorkflow as useDisableWorkflowRpc,
  useEnableWorkflow as useEnableWorkflowRpc,
  useGetTrigger,
  useListReplayEvents,
  useListTriggerEvents,
  useListTriggers,
  useRotateWebhookToken as useRotateWebhookTokenRpc,
  useSetTriggerEnabled as useSetTriggerEnabledRpc,
  useUpdateTrigger as useUpdateTriggerRpc,
  triggerServiceQueries,
} from "./trigger-service-hooks_gen";

const client = connectClient(TriggerService);
// The project_daemons rows a trigger's daemon is checked against are ProjectService's.
export { useProjectDaemonInstalls } from "./project-queries";


// ── Stream invalidation ─────────────────────────────────────────────────────

/**
 * Whether a user update should mark every trigger read stale. A fire that
 * launched a run arrives as `chat_created` (the update names no trigger, so
 * every mounted trigger read refetches, coalesced by the global updates
 * store). Nothing else on the stream concerns triggers: `next_fire_at` and a
 * skipped firing move on their own, which the 60s poll below covers.
 */
export function triggersInvalidatingUpdate(type: UserUpdateType): boolean {
  return type === UserUpdateType.CHAT_CREATED;
}

// ── Reads ───────────────────────────────────────────────────────────────────

/** next_fire_at and last_event move on their own as schedules fire. */
const TRIGGER_POLL_MS = 60_000;

/** How many firings the history and "Only from" read. */
const EVENTS_LIMIT = 50;

/** How long a resolved sender name is reused. */
export const SENDER_NAMES_STALE_MS = 10 * 60_000;

function triggersOf(response: ListTriggersResponse): Trigger[] {
  return response.triggers.map(triggerFromProto);
}

function triggerOf(response: GetTriggerResponse): Trigger {
  if (!response.trigger) throw new Error("No trigger in response");
  return triggerFromProto(response.trigger);
}

function eventsOf(response: ListTriggerEventsResponse) {
  return response.events.map(eventFromProto);
}

const eventsRequest = (triggerId: string) => ({ triggerId, limit: EVENTS_LIMIT });

/** The caller's automations, optionally narrowed to one project. */
export function useTriggers(projectId?: string, options: { enabled?: boolean } = {}) {
  return useListTriggers(
    { projectId },
    {
      enabled: options.enabled ?? true,
      // A page left open should not show a stale "next run" indefinitely.
      refetchInterval: TRIGGER_POLL_MS,
      select: triggersOf,
    },
  );
}

/**
 * An automation's name by id, for labels outside the Automations area (the
 * sidebar's origin glyph). Shares useTriggers' every-project cache entry but
 * never polls on its own: a name is not worth a request a minute from every
 * sidebar row. Undefined while loading, or when the automation was deleted.
 */
export function useTriggerName(triggerId: string | undefined): string | undefined {
  return useTriggerById(triggerId)?.name;
}

/**
 * An automation by id, from the same non-polling every-project entry as
 * useTriggerName, for labels that need more than its name (run detail's
 * schedule timezone). Undefined while loading, or when it was deleted.
 */
export function useTriggerById(triggerId: string | undefined): Trigger | undefined {
  const { data } = useListTriggers({}, { enabled: !!triggerId, staleTime: 5 * 60_000, select: triggersOf });
  return triggerId ? data?.find((trigger) => trigger.id === triggerId) : undefined;
}

export function useTrigger(id?: string) {
  return useGetTrigger({ id: id ?? "" }, { enabled: !!id, refetchInterval: TRIGGER_POLL_MS, select: triggerOf });
}

export function useTriggerEvents(id?: string, options: { refetchInterval?: number | false } = {}) {
  return useListTriggerEvents(eventsRequest(id ?? ""), {
    enabled: !!id,
    refetchInterval: options.refetchInterval ?? TRIGGER_POLL_MS,
    select: eventsOf,
  });
}

/**
 * The recent events a workflow's declared triggers received, newest first,
 * to run the workflow against again (Run now's "Replay an event").
 */
export function useReplayCandidates(projectId: string, workflow: string, enabled: boolean) {
  return useListReplayEvents(
    { projectId, workflow, limit: 20 },
    { enabled, staleTime: 15_000, select: replayCandidatesFromProto },
  );
}

/**
 * Looks people up for "Only from", as the caller: handles (GitHub logins) to
 * the ids an allowlist stores, and ids to the names it shows. Someone the
 * provider does not know is absent from the result. Through the generated
 * key, so a lookup is shared with `useSenderNames` while fresh.
 */
export async function resolveTriggerSenders(
  queryClient: QueryClient,
  integration: string,
  query: { handles?: readonly string[]; senderIds?: readonly string[] },
): Promise<ResolvedSender[]> {
  const request = { integration, handles: [...(query.handles ?? [])], senderIds: [...(query.senderIds ?? [])] };
  const response = await queryClient.fetchQuery(
    triggerServiceQueries.resolveTriggerSenders(request, { staleTime: SENDER_NAMES_STALE_MS }),
  );
  return resolvedSendersFromProto(response);
}

/**
 * Maps a past event through a declared trigger's inputs and prompt, as a fire
 * would. The filter is not applied: the person replaying has decided the
 * event is wanted. A one-shot call per edit of the payload, so not cached.
 */
export async function previewReplay(
  projectId: string,
  workflow: string,
  replay: ReplayEventInput,
  sender?: TriggerSender,
): Promise<{ inputs: Record<string, unknown>; prompt: string }> {
  return previewFromProto(await client.previewReplay(previewReplayRequest(projectId, workflow, replay, sender)));
}

// ── Writes ──────────────────────────────────────────────────────────────────
//
// Every generated TriggerService write invalidates `triggerServiceKeys.all`
// on success, so the lists, the detail and the history refetch with no help
// from here.

type Snapshot = Array<[QueryKey, unknown]>;

/**
 * Rewrite one trigger in every cached trigger read that holds it (the lists
 * and its detail). `patch` returning undefined drops it from the lists.
 * Returns what was there, to put back.
 */
function patchTriggerReads(
  queryClient: QueryClient,
  id: string,
  patch: (trigger: ProtoTrigger) => ProtoTrigger | undefined,
): Snapshot {
  const snapshot: Snapshot = [];
  for (const query of queryClient.getQueryCache().findAll({ queryKey: triggerServiceKeys.all })) {
    const data = query.state.data;
    let next: unknown;
    if (isMessage(data, ListTriggersResponseSchema) && data.triggers.some((t) => t.id === id)) {
      next = { ...data, triggers: data.triggers.flatMap((t) => (t.id === id ? (patch(t) ?? []) : [t])) };
    } else if (isMessage(data, GetTriggerResponseSchema) && data.trigger?.id === id) {
      const trigger = patch(data.trigger);
      if (!trigger) continue;
      next = { ...data, trigger };
    } else {
      continue;
    }
    snapshot.push([query.queryKey, data]);
    queryClient.setQueryData(query.queryKey, next);
  }
  return snapshot;
}

function restore(queryClient: QueryClient, snapshot: Snapshot | undefined) {
  snapshot?.forEach(([key, data]) => queryClient.setQueryData(key, data));
}

/** Seed a returned trigger into its detail read, so a page opened next shows it at once. */
function seedTrigger(queryClient: QueryClient, trigger: ProtoTrigger | undefined) {
  if (!trigger) return;
  queryClient.setQueryData(triggerServiceKeys.getTrigger({ id: trigger.id }), create(GetTriggerResponseSchema, { trigger }));
}

/** Create a trigger; resolves to it, with the webhook token when it is a webhook (shown once). */
export function useCreateTrigger() {
  const queryClient = useQueryClient();
  const mutation = useCreateTriggerRpc({ onSuccess: (response) => seedTrigger(queryClient, response.trigger) });
  return {
    isPending: mutation.isPending,
    mutateAsync: async (input: TriggerDefinitionInput) =>
      triggerWithCredentialFromProto(await mutation.mutateAsync({ trigger: definitionToProto(input) })),
  };
}

/** Replace a trigger's definition (a FULL replacement: start from definitionFromTrigger). */
export function useUpdateTrigger() {
  const queryClient = useQueryClient();
  const mutation = useUpdateTriggerRpc({ onSuccess: (response) => seedTrigger(queryClient, response.trigger) });
  return {
    isPending: mutation.isPending,
    mutateAsync: async ({ id, input }: { id: string; input: TriggerDefinitionInput }) =>
      triggerWithCredentialFromProto(await mutation.mutateAsync({ id, trigger: definitionToProto(input) })).trigger,
  };
}

/** Replace a webhook trigger's token; the old one stops working at once. `mutateAsync(id)`. */
export function useRotateWebhookToken() {
  const mutation = useRotateWebhookTokenRpc();
  return {
    isPending: mutation.isPending,
    mutateAsync: async (id: string) => triggerWithCredentialFromProto(await mutation.mutateAsync({ id })),
  };
}

/**
 * Turn on every trigger a workflow declares, all or none. A refusal is a
 * normal result (`enabled: false`) naming the trigger that failed and why.
 */
export function useEnableWorkflow() {
  const mutation = useEnableWorkflowRpc();
  return {
    isPending: mutation.isPending,
    mutateAsync: async (input: EnableWorkflowInput) =>
      enableWorkflowResultFromProto(await mutation.mutateAsync(enableWorkflowRequest(input))),
  };
}

/**
 * Enable or pause one trigger: `mutate({ id, enabled })`. The switch flips at
 * once — a toggle that lags a round-trip reads as having ignored the click —
 * and flips back if the server refuses.
 */
export function useSetTriggerEnabled() {
  const queryClient = useQueryClient();
  return useSetTriggerEnabledRpc({
    onMutate: async ({ id, enabled }) => {
      await queryClient.cancelQueries({ queryKey: triggerServiceKeys.all });
      return patchTriggerReads(queryClient, id ?? "", (t) => ({ ...t, enabled: enabled ?? t.enabled }));
    },
    onError: (_error, _request, snapshot) => restore(queryClient, snapshot),
    onSuccess: (response) => seedTrigger(queryClient, response.trigger),
  });
}

/**
 * Pause every activation of a workflow: `mutate({ workflow, projectId })`.
 * Partial failure is in the response's `failed`, not thrown; a thrown error
 * still refetches, since some activations may have paused first.
 */
export function useDisableWorkflow() {
  const queryClient = useQueryClient();
  return useDisableWorkflowRpc({
    onError: () => void queryClient.invalidateQueries({ queryKey: triggerServiceKeys.all }),
  });
}

/** Delete a trigger: `mutate({ id })`. It leaves the lists at once; its detail and history are dropped. */
export function useDeleteTrigger() {
  const queryClient = useQueryClient();
  return useDeleteTriggerRpc({
    onSuccess: (_response, { id = "" }) => {
      patchTriggerReads(queryClient, id, () => undefined);
      queryClient.removeQueries({ queryKey: triggerServiceKeys.getTrigger({ id }) });
      queryClient.removeQueries({ queryKey: triggerServiceKeys.listTriggerEvents(eventsRequest(id)) });
    },
  });
}
