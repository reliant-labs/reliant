/**
 * What an org feed batch does to the cache: every shared list is re-read, and
 * a chat made private or deleted has its detail re-read, so a viewer who has
 * it open sees it go. A change to a chat that stays shared re-reads only the
 * lists.
 */

import { create } from "@bufbuild/protobuf";
import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

import { OrgUpdateSchema, OrgUpdateType } from "@/gen/reliant/v1/streaming_pb";
import { chatKeys } from "@/hooks/chat-queries";
import { sharingServiceKeys } from "@/hooks/sharing-service-hooks_gen";
import { applyOrgUpdates } from "@/hooks/sharing-queries";

function invalidated(updates: Parameters<typeof applyOrgUpdates>[1]) {
  const client = new QueryClient();
  const spy = vi.spyOn(client, "invalidateQueries");
  applyOrgUpdates(client, updates);
  return spy.mock.calls.map(([filters]) => filters);
}

const row = (type: OrgUpdateType, resourceKind = "chat") =>
  create(OrgUpdateSchema, { type, resourceKind, resourceId: "c1" });

describe("applyOrgUpdates", () => {
  it("re-reads the shared lists for every batch", () => {
    expect(invalidated([row(OrgUpdateType.CHANGED)])).toEqual([{ queryKey: sharingServiceKeys.all }]);
  });

  it("re-reads a chat that was made private or deleted", () => {
    for (const type of [OrgUpdateType.UNSHARED, OrgUpdateType.DELETED]) {
      expect(invalidated([row(type)])).toEqual([
        { queryKey: sharingServiceKeys.all },
        { queryKey: chatKeys.detail("c1"), exact: true },
      ]);
    }
  });

  it("leaves a project's id out of the chat details", () => {
    expect(invalidated([row(OrgUpdateType.UNSHARED, "project")])).toEqual([{ queryKey: sharingServiceKeys.all }]);
  });
});
