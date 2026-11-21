// Copyright (c) 2025 Reliant Labs

import { Code, ConnectError, createContextKey, createContextValues, type ContextValues, type Interceptor } from "@connectrpc/connect";
import { getRouteOrgScope, whenRouteOrgSettled } from "@/lib/routeOrg";
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
 * The org an implicitly scoped request names right now, by the committed
 * route's scope (lib/routeOrg.ts), the same authority query keys and explicit
 * `org_id` fields read (lib/orgScope.ts), so the three never disagree
 * (control-plane docs/design/unified-shell-and-projects.md §3.5):
 *
 *   member   the org of the page on screen;
 *   legacy   a page outside the org tree: the tab's active org (the mirror);
 *   pending  an org page not yet resolved: null (the interceptor holds the request);
 *   denied   an org page that names no org of theirs: null (refused).
 */
export function requestOrgId(): string | null {
  const scope = getRouteOrgScope();
  if (scope.kind === "member") return scope.org.id;
  if (scope.kind === "legacy") return getActiveOrgId();
  return null;
}

/** How long a request on a freshly loaded org page waits for its org before it is refused. */
export const PENDING_ORG_WAIT_MS = 30_000;

function refused(): ConnectError {
  return new ConnectError(
    "This page's organization is not one of yours, so the request was not sent (it would have acted in another org).",
    Code.FailedPrecondition,
  );
}

/** The org for an implicit request: waits out a pending org page, refuses a denied one. */
async function implicitOrg(): Promise<string | null> {
  let scope = getRouteOrgScope();
  if (scope.kind === "pending") {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const timeout = new Promise<null>((resolve) => {
      timer = setTimeout(() => resolve(null), PENDING_ORG_WAIT_MS);
    });
    const settled = await Promise.race([whenRouteOrgSettled(), timeout]);
    clearTimeout(timer);
    if (!settled) throw refused();
    scope = settled;
  }
  if (scope.kind === "denied") throw refused();
  return requestOrgId();
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
 * active org outside the org tree (still loading, a machine credential, a
 * signup mid-provisioning) names none, so the server resolves the person's
 * default.
 *
 * On an org page that names no org of the person's (pending, denied) an
 * implicit request never goes out naming none, which the server would answer
 * in their default org: it waits for a pending page's first commit, and is
 * refused (FailedPrecondition) on a denied one. A request that names its org
 * itself, or spans every org on purpose (acrossAllOrgs), is not held.
 *
 * Except on a pending page, this runs synchronously before timeout and
 * asynchronous authentication in the canonical chain, so the org is the one
 * of the page that made the call. It runs once per logical call, not per
 * retry, so a later selection cannot retarget either a named org or an
 * initially unset one.
 *
 * Streaming requests are left alone: their request is an async iterable, and
 * no org-scoped control-plane rpc the web calls streams.
 */
export const orgInterceptor: Interceptor = (next) => async (req) => {
  if (!req.stream && !req.contextValues.get(acrossAllOrgsKey)) {
    const field = req.method.input.fields.find((f) => f.name === "org_id");
    if (field) {
      const message = req.message as Record<string, unknown>;
      const key = field.localName;
      if (!message[key]) {
        const scope = getRouteOrgScope();
        const orgId = scope.kind === "member" || scope.kind === "legacy" ? requestOrgId() : await implicitOrg();
        if (orgId) message[key] = orgId;
      }
    }
  }
  return await next(req);
};
