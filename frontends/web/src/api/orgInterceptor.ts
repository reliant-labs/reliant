// Copyright (c) 2025 Reliant Labs

import type { Interceptor } from "@connectrpc/connect";
import { getActiveOrgId } from "@/store/activeOrgStore";

/**
 * Names the active org on every request whose message has an `org_id` field
 * (control-plane's org-scoped rpcs, docs/orgs.md, and ForgeService, which hands
 * the org to forge's credential helper).
 *
 * One interceptor instead of a field at ~100 call sites is the point: a call
 * site that forgot it would silently act in the default org, which is the
 * "first org of the user" bug in a new place. A caller that names an org
 * itself keeps it (the field is only filled when empty), and a request with no
 * active org (still loading, a machine credential, a signup mid-provisioning)
 * names none, so the server resolves the person's default.
 *
 * This runs synchronously before timeout and asynchronous authentication in
 * the canonical chain. It runs once per logical call, not per retry, so a later
 * selection cannot retarget either a named org or an initially unset one.
 *
 * Streaming requests are left alone: their request is an async iterable, and
 * no org-scoped control-plane rpc the web calls streams.
 */
export const orgInterceptor: Interceptor = (next) => async (req) => {
  if (!req.stream) {
    const field = req.method.input.fields.find((f) => f.name === "org_id");
    if (field) {
      const orgId = getActiveOrgId();
      const message = req.message as Record<string, unknown>;
      const key = field.localName;
      if (orgId && !message[key]) message[key] = orgId;
    }
  }
  return await next(req);
};
