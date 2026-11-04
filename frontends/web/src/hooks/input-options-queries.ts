// Copyright (c) 2025 Reliant Labs

/**
 * The option lists behind IntegrationValuePicker: CatalogService.ListInputOptions,
 * the values a typed input's picker offers (`github.repository`,
 * `github.label`, …), listed as the caller through one of their connections.
 * One RPC serves every integration type; the picker knows nothing per
 * integration (research/autopilot/typed-inputs-design.md §3.5).
 *
 * Keyed under `catalogServiceKeys` by the request — type, connection, scope,
 * query, acting identity — and cached for 60s, the server's own TTL, so
 * reopening a picker or typing back to an earlier query costs nothing, and a
 * connection change (which invalidates `catalogServiceKeys.all`) relists.
 */

import { useMemo } from "react";
import type { InfiniteData } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  InputOptionsConnectionState,
  type InputOption as ProtoInputOption,
  type ListInputOptionsResponse,
} from "@/gen/reliant/v1/catalog_pb";
import { ActingIdentity } from "@/gen/reliant/v1/trigger_pb";
import { jsToProtoValue, protoValueToJs } from "@/api/proto-utils";
import { catalogServiceKeys, useListInputOptions, useListInputOptionsInfinite } from "./catalog-service-hooks_gen";

export const INPUT_OPTIONS_STALE_MS = 60_000;
const PAGE_SIZE = 50;

export interface InputOption {
  /** What is stored in the input. */
  value: string;
  label: string;
  description: string;
  /** The heading the option is listed under; empty for none. */
  group: string;
  annotations: Record<string, unknown>;
  /** Non-blocking: shown with the option and on its chip. */
  warnings: string[];
}

export type InputOptionsConnectionKey = "connected" | "needs_connection" | "unknown";

/** A listing as far as it has been paged: every loaded page's options, in order. */
export interface InputOptionsListing {
  options: InputOption[];
  /** The first page's answer: whether the listing needs a connection. */
  connectionState: InputOptionsConnectionKey;
}

export interface InputOptionsParams {
  type: string;
  connectionId?: string;
  scope?: Record<string, unknown>;
  query?: string;
  actingIdentity?: "owner" | "app";
  enabled?: boolean;
}

function connectionStateKey(state: InputOptionsConnectionState): InputOptionsConnectionKey {
  switch (state) {
    case InputOptionsConnectionState.CONNECTED:
      return "connected";
    case InputOptionsConnectionState.NEEDS_CONNECTION:
      return "needs_connection";
    default:
      return "unknown";
  }
}

function actingIdentityToProto(identity: InputOptionsParams["actingIdentity"]): ActingIdentity | undefined {
  if (identity === "app") return ActingIdentity.APP;
  if (identity === "owner") return ActingIdentity.OWNER;
  return undefined;
}

function optionFromProto(option: ProtoInputOption): InputOption {
  return {
    value: option.value,
    label: option.label || option.value,
    description: option.description,
    group: option.group,
    annotations: Object.fromEntries(Object.entries(option.annotations).map(([key, value]) => [key, protoValueToJs(value)])),
    warnings: [...option.warnings],
  };
}

/**
 * A scope with its list values in a canonical order: the same values list the
 * same options whatever order the sibling inputs hold them in, so they share
 * one request and one cache entry. (protojson sorts nothing inside a list;
 * the key hash already sorts object keys.)
 */
function canonicalScope(scope: Record<string, unknown> | undefined) {
  const byText = (a: unknown, b: unknown) => (String(a) < String(b) ? -1 : String(a) > String(b) ? 1 : 0);
  return Object.fromEntries(
    Object.entries(scope ?? {}).map(([key, value]) => [key, jsToProtoValue(Array.isArray(value) ? [...value].sort(byText) : value)]),
  );
}

/** The first page's request: everything the listing depends on, no page token. */
function listRequest(p: InputOptionsParams) {
  return {
    type: p.type,
    connectionId: p.connectionId || undefined,
    scope: canonicalScope(p.scope),
    query: p.query || undefined,
    pageSize: PAGE_SIZE,
    actingIdentity: actingIdentityToProto(p.actingIdentity),
  };
}

/** What a listing is apart from its query: typing changes the query and nothing else. */
function listingIdentity(p: InputOptionsParams): string {
  return JSON.stringify(catalogServiceKeys.listInputOptionsInfinite(listRequest({ ...p, query: undefined })));
}

// Module-level, so React Query re-maps only when a page lands.
function listingOf(data: InfiniteData<ListInputOptionsResponse, string>): InputOptionsListing {
  return {
    options: data.pages.flatMap((page) => page.options.map(optionFromProto)),
    connectionState: connectionStateKey(data.pages[0]?.connectionState ?? InputOptionsConnectionState.UNSPECIFIED),
  };
}

export function useInputOptions(params: InputOptionsParams) {
  return useListInputOptionsInfinite(listRequest(params), {
    select: listingOf,
    enabled: params.enabled !== false && !!params.type,
    staleTime: INPUT_OPTIONS_STALE_MS,
    // Typing narrows the list in place: the previous query's options stay
    // (filtered client-side) until the new answer lands. Only for a new
    // query: another type, connection or scope lists different things.
    placeholderData: (previous, previousQuery) =>
      previousQuery?.meta?.listing === listingIdentity(params) ? previous : undefined,
    meta: { listing: listingIdentity(params) },
    // A listing that failed (the server cannot list this type, or the
    // provider is down) is shown with Try again; retrying silently behind
    // the user's back only delays that.
    retry: false,
  });
}

/** Labels for stored values the current options do not contain (an id → a login). */
export function useResolvedInputOptions(type: string, connectionId: string | undefined, values: readonly string[], enabled = true) {
  const resolve = useMemo(() => [...values].sort(), [values]);
  return useListInputOptions(
    { type, connectionId: connectionId || undefined, resolve },
    { enabled: enabled && !!type && values.length > 0, staleTime: INPUT_OPTIONS_STALE_MS, retry: false, select: optionsOf },
  );
}

const optionsOf = (response: ListInputOptionsResponse) => response.options.map(optionFromProto);

/** The reason a listing failed, in words for "Couldn't load repositories: …". */
export function inputOptionsErrorReason(error: unknown): string {
  if (error instanceof ConnectError) {
    if (error.code === Code.Unimplemented) return "this server can't list them yet";
    return error.rawMessage || error.message;
  }
  return error instanceof Error ? error.message : String(error);
}
