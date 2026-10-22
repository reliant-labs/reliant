// Copyright (c) 2025 Reliant Labs

/**
 * CatalogService.ListInputOptions: the values a typed input's picker offers
 * (`github.repository`, `github.label`, …), listed as the caller through one
 * of their connections. One RPC serves every integration type; the picker
 * knows nothing per integration (research/autopilot/typed-inputs-design.md §3.5).
 */

import { create } from "@bufbuild/protobuf";

import { grpcClient } from "./grpc-client";
import { jsToProtoValue, protoValueToJs } from "./proto-utils";
import {
  InputOptionsConnectionState,
  ListInputOptionsRequestSchema,
} from "../gen/reliant/v1/catalog_pb";
import { ActingIdentity } from "../gen/reliant/v1/trigger_pb";

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

export interface InputOptionsPage {
  options: InputOption[];
  nextPageToken: string;
  connectionState: InputOptionsConnectionKey;
}

export interface InputOptionsQuery {
  type: string;
  /** Empty: the caller's default connection to the type's integration. */
  connectionId?: string;
  /** The values of the type's scope keys, resolved from sibling inputs. */
  scope?: Record<string, unknown>;
  query?: string;
  pageToken?: string;
  pageSize?: number;
  actingIdentity?: "owner" | "app";
  /** Label these stored values instead of listing. */
  resolve?: readonly string[];
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

function actingIdentityToProto(identity: InputOptionsQuery["actingIdentity"]): ActingIdentity {
  if (identity === "app") return ActingIdentity.APP;
  if (identity === "owner") return ActingIdentity.OWNER;
  return ActingIdentity.UNSPECIFIED;
}

export const inputOptionsGrpc = {
  async list(q: InputOptionsQuery, signal?: AbortSignal): Promise<InputOptionsPage> {
    const response = await grpcClient.catalog().listInputOptions(
      create(ListInputOptionsRequestSchema, {
        type: q.type,
        connectionId: q.connectionId || undefined,
        scope: Object.fromEntries(Object.entries(q.scope ?? {}).map(([key, value]) => [key, jsToProtoValue(value)])),
        query: q.query || undefined,
        pageToken: q.pageToken ?? "",
        pageSize: q.pageSize ?? 0,
        actingIdentity: q.actingIdentity ? actingIdentityToProto(q.actingIdentity) : undefined,
        resolve: [...(q.resolve ?? [])],
      }),
      { signal },
    );
    return {
      options: response.options.map((option) => ({
        value: option.value,
        label: option.label || option.value,
        description: option.description,
        group: option.group,
        annotations: Object.fromEntries(Object.entries(option.annotations).map(([key, value]) => [key, protoValueToJs(value)])),
        warnings: [...option.warnings],
      })),
      nextPageToken: response.nextPageToken,
      connectionState: connectionStateKey(response.connectionState),
    };
  },
};
