// Copyright (c) 2025 Reliant Labs

/**
 * Thin client over `CatalogService.SearchCatalog` / `GetCatalogEntry`: the
 * integration catalog, searched server-side so the builder never ships the
 * whole catalog to the browser (research/INTEGRATIONS_V1_BRIEF.md §3a).
 *
 * Proto messages become plain frontend types here, so components never touch
 * `$typeName` or proto enums.
 */

import { create } from "@bufbuild/protobuf";

import { grpcClient } from "./grpc-client";
import {
  CatalogEntryKind,
  GetCatalogEntryRequestSchema,
  ListCatalogIntegrationsRequestSchema,
  SearchCatalogRequestSchema,
  type CatalogEntry as ProtoCatalogEntry,
  type CatalogEntrySummary as ProtoCatalogEntrySummary,
  type CatalogIntegration as ProtoCatalogIntegration,
} from "../gen/reliant/v1/catalog_pb";
import { ConnectionAuthKind } from "../gen/reliant/v1/connection_pb";
import { asJsonSchema, type JsonSchema } from "../lib/jsonSchema";
import { authKindFromProto, type AuthKind, type AuthMethod, type ConnectionParam, authMethodFromProto, connectionParamFromProto } from "./connection-grpc";

export type CatalogKind = "action" | "trigger";

export interface CatalogIntegration {
  id: string;
  version: number;
  displayName: string;
  /** Icon hint, e.g. "github" or "globe". */
  icon: string;
  category: string;
}

/** One search result: enough to render a picker row. */
export interface CatalogEntrySummary {
  /** "github/issue.create@1" — what an action node's `uses` names. */
  ref: string;
  kind: CatalogKind;
  id: string;
  displayName: string;
  summary: string;
  integration: CatalogIntegration;
  authKinds: AuthKind[];
  connectionRequired: boolean;
  /** The caller can use it now: no connection needed, one exists, or a delegated authority serves it. */
  connected: boolean;
  /** The action changes external state. */
  mutates: boolean;
}

export interface CatalogEntry {
  summary: CatalogEntrySummary;
  description: string;
  paramsSchema?: JsonSchema;
  outputSchema?: JsonSchema;
  payloadSchema?: JsonSchema;
  connection: {
    required: boolean;
    methods: AuthMethod[];
    params: ConnectionParam[];
  };
  toolName: string;
}

export interface CatalogFacet {
  value: string;
  count: number;
}

export interface CatalogSearchQuery {
  query: string;
  kinds?: CatalogKind[];
  category?: string;
  integration?: string;
  connectedOnly?: boolean;
  pageSize?: number;
  pageToken?: string;
}

export interface CatalogSearchPage {
  entries: CatalogEntrySummary[];
  nextPageToken: string;
  totalSize: number;
  categoryFacets: CatalogFacet[];
}

/** One integration in a browse: enough to draw its row before it is expanded. */
export interface CatalogIntegrationListing {
  integration: CatalogIntegration;
  /** How many of its entries are of the browsed kinds. */
  entryCount: number;
  /** The caller can use it now (see CatalogEntrySummary.connected). */
  connected: boolean;
}

export interface CatalogIntegrationsQuery {
  kinds?: CatalogKind[];
  category?: string;
  pageSize?: number;
  pageToken?: string;
}

export interface CatalogIntegrationsPage {
  /** Connected first, then by display name. */
  integrations: CatalogIntegrationListing[];
  nextPageToken: string;
  totalSize: number;
  categoryFacets: CatalogFacet[];
}

function kindFromProto(kind: CatalogEntryKind): CatalogKind {
  return kind === CatalogEntryKind.TRIGGER ? "trigger" : "action";
}

function kindToProto(kind: CatalogKind): CatalogEntryKind {
  return kind === "trigger" ? CatalogEntryKind.TRIGGER : CatalogEntryKind.ACTION;
}

function integrationFromProto(proto: ProtoCatalogIntegration | undefined): CatalogIntegration {
  return {
    id: proto?.id ?? "",
    version: proto?.version ?? 0,
    displayName: proto?.displayName || proto?.id || "",
    icon: proto?.icon ?? "",
    category: proto?.category ?? "",
  };
}

export function catalogSummaryFromProto(proto: ProtoCatalogEntrySummary): CatalogEntrySummary {
  return {
    ref: proto.ref,
    kind: kindFromProto(proto.kind),
    id: proto.id,
    displayName: proto.displayName || proto.id,
    summary: proto.summary,
    integration: integrationFromProto(proto.integration),
    authKinds: proto.authKinds
      .filter((kind) => kind !== ConnectionAuthKind.UNSPECIFIED)
      .map(authKindFromProto),
    connectionRequired: proto.connectionRequired,
    connected: proto.connected,
    mutates: proto.mutates,
  };
}

export function catalogEntryFromProto(proto: ProtoCatalogEntry): CatalogEntry {
  if (!proto.summary) throw new Error("Catalog entry has no summary");
  return {
    summary: catalogSummaryFromProto(proto.summary),
    description: proto.description,
    paramsSchema: asJsonSchema(proto.paramsSchema),
    outputSchema: asJsonSchema(proto.outputSchema),
    payloadSchema: asJsonSchema(proto.payloadSchema),
    connection: {
      required: proto.connection?.required ?? false,
      methods: (proto.connection?.methods ?? []).map(authMethodFromProto),
      params: (proto.connection?.connectionParams ?? []).map(connectionParamFromProto),
    },
    toolName: proto.toolName,
  };
}

/** The integration id a ref names: "github/issue.create@1" → "github". */
export function refIntegration(ref: string): string {
  const slash = ref.indexOf("/");
  return slash > 0 ? ref.slice(0, slash) : "";
}

export const catalogSearchGrpc = {
  async search(q: CatalogSearchQuery, signal?: AbortSignal): Promise<CatalogSearchPage> {
    const response = await grpcClient.catalog().searchCatalog(
      create(SearchCatalogRequestSchema, {
        query: q.query.slice(0, 256),
        kinds: (q.kinds ?? []).map(kindToProto),
        category: q.category ?? "",
        integration: q.integration ?? "",
        connectedOnly: q.connectedOnly ?? false,
        pageSize: q.pageSize ?? 20,
        pageToken: q.pageToken ?? "",
      }),
      { signal },
    );
    return {
      entries: response.entries.map(catalogSummaryFromProto),
      nextPageToken: response.nextPageToken,
      totalSize: response.totalSize,
      categoryFacets: response.categoryFacets.map((facet) => ({ value: facet.value, count: facet.count })),
    };
  },

  /** Browse by integration: one row per integration, connected first. */
  async listIntegrations(q: CatalogIntegrationsQuery, signal?: AbortSignal): Promise<CatalogIntegrationsPage> {
    const response = await grpcClient.catalog().listCatalogIntegrations(
      create(ListCatalogIntegrationsRequestSchema, {
        kinds: (q.kinds ?? []).map(kindToProto),
        category: q.category ?? "",
        pageSize: q.pageSize ?? 20,
        pageToken: q.pageToken ?? "",
      }),
      { signal },
    );
    return {
      integrations: response.integrations.map((listing) => ({
        integration: integrationFromProto(listing.integration),
        entryCount: listing.entryCount,
        connected: listing.connected,
      })),
      nextPageToken: response.nextPageToken,
      totalSize: response.totalSize,
      categoryFacets: response.categoryFacets.map((facet) => ({ value: facet.value, count: facet.count })),
    };
  },

  async get(ref: string): Promise<CatalogEntry> {
    const response = await grpcClient.catalog().getCatalogEntry(create(GetCatalogEntryRequestSchema, { ref }));
    if (!response.entry) throw new Error(`No catalog entry for ${ref}`);
    return catalogEntryFromProto(response.entry);
  },
};
