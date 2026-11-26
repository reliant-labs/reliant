// Copyright (c) 2025 Reliant Labs

/**
 * ActivityService's ListActivity over a model-level ObservabilityApi: the
 * feed a test fake (test/observabilityFixtures.tsx) answers, encoded on the
 * wire so the console's generated hook (hooks/observability-queries.ts
 * useActivity) and its `select` mapper (activityApi.ts) decode it. The shop
 * pack's own ActivityService is the console scenario's (handlers/console.ts),
 * over the same events (model/activity.ts) and the same encoder.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import { mockService, type MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { ActivityActorKind, ActivityService, type ActivityEventSchema } from "@/gen/controlplane/services/activity/v1/activity_pb";
import type { ActivityEvent } from "@/services/controlPlane/observability/activityApi";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { ts } from "./wire";

const KIND = { user: ActivityActorKind.USER, machine: ActivityActorKind.MACHINE, system: ActivityActorKind.SYSTEM, unknown: ActivityActorKind.UNSPECIFIED } as const;

/** One model event on the wire. */
export function wireActivityEvent(e: ActivityEvent): MessageInitShape<typeof ActivityEventSchema> {
  return {
    id: e.id,
    at: ts(e.atMs),
    actor: { ...e.actor, kind: KIND[e.actor.kind] },
    verb: e.verb,
    target: e.target,
    env: e.env ?? undefined,
    via: e.via,
    detail: e.detail,
    project: e.project,
  };
}

export function activityHandlers(fixtures: Pick<ObservabilityApi, "listActivity">): MockHandlers {
  return mockService(ActivityService, {
    listActivity: async (req) => {
      const scope = req.scope;
      const res = await fixtures.listActivity({
        orgId: scope?.orgId ?? "",
        projectId: scope?.projectId,
        envIds: [...(scope?.envIds ?? [])],
        targetKind: req.targetKind,
        targetId: req.targetId,
        verb: req.verb,
        pageSize: req.pageSize,
        pageToken: req.pageToken,
      });
      return { events: res.events.map(wireActivityEvent), nextPageToken: res.nextPageToken };
    },
  });
}
