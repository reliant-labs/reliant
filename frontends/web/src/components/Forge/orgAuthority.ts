// Copyright (c) 2025 Reliant Labs

/**
 * Who may do what to a hosted environment (control-plane
 * docs/design/unified-shell-and-projects.md §11.4, D19; today's server rules):
 *
 *   Stop / Start an env        deploy:write on that env (org-wide or bound to it)
 *   Delete an env              org owner or admin
 *   Purge a kept database      org owner or admin
 *
 * The role comes from the caller's membership in the ACTIVE org, the org every
 * deploy request acts in. It is a courtesy, never the control: the server
 * decides, and every control still shows a PermissionDenied in words.
 *
 * "unknown" (the membership list has not answered, or there is no control
 * plane to ask) leaves a control enabled, so the server's answer is what the
 * person sees. A member is not known to LACK deploy:write: an admin may have
 * bound it to one environment, and reading grants needs token:read most
 * members do not hold. So Stop is never disabled on the role alone.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { useActiveOrg } from "@/hooks/useOrgs";
import { cloudErrorDetail } from "@/services/forge/cloudEnvs";

export type OrgAuthority = "admin" | "member" | "unknown";

export function orgAuthorityOf(role: string | null | undefined): OrgAuthority {
  if (role === "owner" || role === "admin") return "admin";
  if (role === "member") return "member";
  return "unknown";
}

/** The caller's authority in the active org. */
export function useOrgAuthority(): OrgAuthority {
  const { active } = useActiveOrg();
  return orgAuthorityOf(active?.role);
}

export const DELETE_ENV_NEEDS_ADMIN = "Deleting an environment needs an org owner or admin.";
export const PURGE_NEEDS_ADMIN = "Purging a kept database needs an org owner or admin.";

/**
 * A refusal in words. A PermissionDenied leads with the rule (`needs`), then
 * the server's own detail; anything else is the server's detail alone.
 */
export function refusalText(err: unknown, needs: string): string {
  const detail = cloudErrorDetail(err);
  if (err instanceof ConnectError && err.code === Code.PermissionDenied) {
    return detail ? `${needs} (${detail})` : needs;
  }
  return detail || "The request failed. Try again.";
}
