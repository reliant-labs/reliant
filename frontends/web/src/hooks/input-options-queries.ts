// Copyright (c) 2025 Reliant Labs

/**
 * The option lists behind IntegrationValuePicker. Keyed by everything the
 * server's answer depends on — type, connection, scope, query, acting
 * identity — and cached for 60s, the server's own TTL, so reopening a picker
 * or typing back to an earlier query costs nothing.
 */

import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import { inputOptionsGrpc, type InputOption, type InputOptionsPage } from "@/api/input-options-grpc";

export const INPUT_OPTIONS_STALE_MS = 60_000;
const PAGE_SIZE = 50;

export interface InputOptionsParams {
  type: string;
  connectionId?: string;
  scope?: Record<string, unknown>;
  query?: string;
  actingIdentity?: "owner" | "app";
  enabled?: boolean;
}

/** A stable key for a scope object: same values, same key, whatever the order. */
export function scopeKey(scope: Record<string, unknown> | undefined): string {
  if (!scope) return "";
  return JSON.stringify(
    Object.keys(scope)
      .sort()
      .map((key) => [key, Array.isArray(scope[key]) ? [...(scope[key] as unknown[])].map(String).sort() : scope[key]]),
  );
}

export const inputOptionsKeys = {
  all: ["input-options"] as const,
  list: (p: InputOptionsParams) =>
    [...inputOptionsKeys.all, "list", p.type, p.connectionId ?? "", scopeKey(p.scope), p.query ?? "", p.actingIdentity ?? ""] as const,
  resolve: (type: string, connectionId: string | undefined, values: readonly string[]) =>
    [...inputOptionsKeys.all, "resolve", type, connectionId ?? "", [...values].sort().join("\u0000")] as const,
};

export function useInputOptions(params: InputOptionsParams) {
  return useInfiniteQuery<InputOptionsPage, Error>({
    queryKey: inputOptionsKeys.list(params),
    queryFn: ({ pageParam, signal }) =>
      inputOptionsGrpc.list(
        {
          type: params.type,
          connectionId: params.connectionId,
          scope: params.scope,
          query: params.query,
          actingIdentity: params.actingIdentity,
          pageToken: (pageParam as string) || undefined,
          pageSize: PAGE_SIZE,
        },
        signal,
      ),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextPageToken || undefined,
    enabled: params.enabled !== false && !!params.type,
    staleTime: INPUT_OPTIONS_STALE_MS,
    // Typing narrows the list in place: the previous query's options stay
    // (filtered client-side) until the new answer lands. Only for a new
    // query: another type, connection or scope lists different things.
    placeholderData: (previous, previousQuery) => {
      const before = previousQuery?.queryKey as readonly unknown[] | undefined;
      const now = inputOptionsKeys.list(params);
      const sameList = !!before && [2, 3, 4, 6].every((i) => before[i] === now[i]);
      return sameList ? previous : undefined;
    },
    // A listing that failed (the server cannot list this type, or the
    // provider is down) is shown with Try again; retrying silently behind
    // the user's back only delays that.
    retry: false,
  });
}

/** Labels for stored values the current options do not contain (an id → a login). */
export function useResolvedInputOptions(type: string, connectionId: string | undefined, values: readonly string[], enabled = true) {
  return useQuery<InputOption[], Error>({
    queryKey: inputOptionsKeys.resolve(type, connectionId, values),
    queryFn: async ({ signal }) => (await inputOptionsGrpc.list({ type, connectionId, resolve: values }, signal)).options,
    enabled: enabled && !!type && values.length > 0,
    staleTime: INPUT_OPTIONS_STALE_MS,
    retry: false,
  });
}

/** The reason a listing failed, in words for "Couldn't load repositories: …". */
export function inputOptionsErrorReason(error: unknown): string {
  if (error instanceof ConnectError) {
    if (error.code === Code.Unimplemented) return "this server can't list them yet";
    return error.rawMessage || error.message;
  }
  return error instanceof Error ? error.message : String(error);
}
