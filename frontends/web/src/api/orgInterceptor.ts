// Copyright (c) 2025 Reliant Labs

import { createContextKey, createContextValues, type ContextValues, type Interceptor } from "@connectrpc/connect";
import { currentRouteOrg } from "@/lib/routeOrg";
import { getActiveOrgId } from "@/store/activeOrgStore";

/**
 * Set on a call that deliberately spans every org the caller is in, so the
 * active org is NOT filled in and the server answers for all of them (an unset
 * org_id's documented meaning, e.g. ListDaemonsRequest). Only for reads whose
 * answer is labelled per org on the client (the machine → org map), never for
 * a write: a write that names no org acts in the person's default.
 */
export const acrossAllOrgsKey = createContextKey(false, {
  description: "the call names no org on purpose: it spans every org",
});

/** Context values for a call that spans every org (see acrossAllOrgsKey). */
export function acrossAllOrgs(values: ContextValues = createContextValues()): ContextValues {
  return values.set(acrossAllOrgsKey, true);
}

/**
 * The org a request names: the org of the route on screen when it is an org
 * page (`/o/<org>/…`, lib/routeOrg.ts), else the tab's active org — the mirror
 * the router writes when it enters an org, and the switcher on a page outside
 * the org tree (control-plane docs/design/unified-shell-and-projects.md §3.5).
 */
export function requestOrgId(): string | null {
  return currentRouteOrg()?.id ?? getActiveOrgId();
}

/**
 * Names the request's org (requestOrgId) on every request whose message has an `org_id` field
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
  if (!req.stream && !req.contextValues.get(acrossAllOrgsKey)) {
    const field = req.method.input.fields.find((f) => f.name === "org_id");
    if (field) {
      const orgId = requestOrgId();
      const message = req.message as Record<string, unknown>;
      const key = field.localName;
      if (orgId && !message[key]) message[key] = orgId;
    }
  }
  return await next(req);
};
