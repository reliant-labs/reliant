// Copyright (c) 2025 Reliant Labs

/**
 * control-plane `controlplane.v1.ActivityService`: the typed activity stream
 * ("sean promoted v1.7.44 to prod via CLI"), mapped into the console's model.
 * The only file that knows its wire shape.
 *
 * The server stores the verb as a token ("promote", "query_database"); the
 * console phrases it (Activity/activityText.ts). Scope is the delivery scope's:
 * org (membership checked), optionally a project (its events plus the
 * org-wide ones), optionally environments.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import type { Transport } from "@connectrpc/connect";
import {
  ActivityActorKind,
  ActivityService,
  type ActivityEvent as WireEvent,
  type ListActivityRequestSchema,
  type ListActivityResponse,
} from "@/gen/controlplane/services/activity/v1/activity_pb";

import { getControlPlaneClient } from "../client";
import { msOf } from "./api";
import type { DeliveryScope } from "./deliveryTypes";

export type ActivityActorKindName = "user" | "machine" | "system" | "unknown";

export interface ActivityEvent {
  id: string;
  atMs: number;
  actor: { id: string; name: string; kind: ActivityActorKindName };
  /** A lower_snake token: "promote", "resolve", "query_database". */
  verb: string;
  target: { kind: string; id: string; label: string };
  /** Null for a project- or org-wide event. */
  env: { id: string; name: string } | null;
  project: string;
  /** "console", "cli", "ci", "api", or "reliant" for the platform's own observations. */
  via: string;
  /** Small declared extras: {"reason": …, "row_count": …} on a query, {"from": "unresolved"} on a triage. */
  detail: Record<string, string>;
}

export interface ActivityList {
  /** Newest first. */
  events: ActivityEvent[];
  nextPageToken: string;
}

export interface ListActivityRequest extends DeliveryScope {
  targetKind?: string;
  targetId?: string;
  verb?: string;
  pageSize?: number;
  pageToken?: string;
}

const KIND_OF: Record<ActivityActorKind, ActivityActorKindName> = {
  [ActivityActorKind.UNSPECIFIED]: "unknown",
  [ActivityActorKind.USER]: "user",
  [ActivityActorKind.MACHINE]: "machine",
  [ActivityActorKind.SYSTEM]: "system",
};

export function mapActivityEvent(e: WireEvent): ActivityEvent {
  return {
    id: e.id,
    atMs: msOf(e.at),
    actor: { id: e.actor?.id ?? "", name: e.actor?.name || e.actor?.id || "", kind: KIND_OF[e.actor?.kind ?? ActivityActorKind.UNSPECIFIED] ?? "unknown" },
    verb: e.verb,
    target: { kind: e.target?.kind ?? "", id: e.target?.id ?? "", label: e.target?.label || e.target?.id || "" },
    env: e.env ? { id: e.env.id, name: e.env.name } : null,
    project: e.project,
    via: e.via,
    detail: { ...e.detail },
    // procedure is not shown: it names the rpc, which the verb already says.
  };
}

/** ListActivity for one page of the scope's activity. No time range: the feed is the newest first, a page at a time. */
export function activityRequest(req: ListActivityRequest): MessageInitShape<typeof ListActivityRequestSchema> {
  return {
    scope: { orgId: req.orgId, projectId: req.projectId || undefined, envIds: req.envIds },
    targetKind: req.targetKind || undefined,
    targetId: req.targetId || undefined,
    verb: req.verb || undefined,
    pageSize: req.pageSize ?? 0,
    pageToken: req.pageToken ?? "",
  };
}

/** A page of ListActivity or ListMembershipActivity (one response type) in the console's model. */
export function toActivityList(res: ListActivityResponse): ActivityList {
  return { events: res.events.map(mapActivityEvent), nextPageToken: res.nextPageToken };
}

/** One page of the scope's activity, newest first. */
export async function listActivity(req: ListActivityRequest, transport?: Transport): Promise<ActivityList> {
  return toActivityList(await getControlPlaneClient(ActivityService, transport).listActivity(activityRequest(req)));
}
