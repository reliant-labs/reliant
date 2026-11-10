// Copyright (c) 2025 Reliant Labs

import Button from "@/components/forge-ui/button";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import type { ActivityEvent } from "@/services/controlPlane/observability/activityApi";
import { memberErrorMessage, type OrgMember } from "@/services/controlPlane/members";
import type { MyOrg } from "@/services/controlPlane/orgs";

import { memberLabel } from "./MemberRoster";
import { useMembershipActivity } from "./useMembers";

/** A declared detail, whichever spelling of its path the server recorded it under. */
function detailOf(e: ActivityEvent, key: string): string {
  const hit = Object.entries(e.detail).find(([k]) => k === key || k.endsWith(`.${key}`));
  return hit?.[1] ?? "";
}

/** "an admin", "an owner", "a member". */
const withArticle = (role: string) => (/^[aeiou]/.test(role) ? `an ${role}` : `a ${role}`);

/**
 * One membership event as a sentence: "Ada invited bob@example.com as an admin".
 * The target of remove/set_role is a user id; `nameOf` turns it back into a
 * person when they are still on the roster.
 */
export function describeMembershipEvent(e: ActivityEvent, nameOf: (userId: string) => string): string {
  const actor = e.actor.name || "Someone";
  const target = e.target.label && e.target.label !== e.target.id ? e.target.label : nameOf(e.target.id);
  const role = detailOf(e, "role");
  const asRole = role ? ` as ${withArticle(role)}` : "";
  switch (e.verb) {
    case "create_org":
      return `${actor} created the organization`;
    case "invite":
      return `${actor} ${detailOf(e, "resent") === "true" ? "re-sent the invitation to" : "invited"} ${target}${asRole}`;
    case "revoke_invite":
      return `${actor} revoked an invitation`;
    case "join":
      return `${actor} joined${asRole}`;
    case "remove":
      return `${actor} removed ${target}`;
    case "leave":
      return `${actor} left`;
    case "set_role":
      return role ? `${actor} made ${target} ${withArticle(role)}` : `${actor} changed ${target}'s role`;
    case "set_grants":
      return `${actor} changed ${target}'s permissions`;
    default:
      return `${actor} ${e.verb.replace(/_/g, " ")} ${target}`.trim();
  }
}

export function MembershipActivity({ org, members }: { org: MyOrg; members: OrgMember[] }) {
  const activity = useMembershipActivity(org.id);
  const byId = new Map(members.map((m) => [m.userId, memberLabel(m)]));
  const nameOf = (id: string) => byId.get(id) ?? "a former member";
  const events = activity.data?.pages.flatMap((p) => p.events) ?? [];

  return (
    <section className="rounded-lg border border-border bg-card" aria-labelledby="members-activity-heading" data-testid="members-activity">
      <h3 id="members-activity-heading" className="border-b border-border px-4 py-3 text-sm font-semibold text-foreground">
        Membership history
      </h3>
      {activity.isPending ? (
        <p className="px-4 py-3 text-sm text-muted-foreground" role="status">
          Loading history…
        </p>
      ) : activity.isError ? (
        <p className="px-4 py-3 text-sm text-muted-foreground" role="alert">
          {memberErrorMessage(activity.error, "load", "Could not load the membership history.")}
        </p>
      ) : events.length === 0 ? (
        <p className="px-4 py-3 text-sm text-muted-foreground">Nothing has changed in this organization's membership yet.</p>
      ) : (
        <ul className="divide-y divide-border">
          {events.map((e) => (
            <li key={e.id} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 px-4 py-3" data-testid={`members-activity-${e.id}`}>
              <p className="min-w-0 flex-1 text-sm text-foreground">{describeMembershipEvent(e, nameOf)}</p>
              <time className="text-xs text-muted-foreground" dateTime={new Date(e.atMs).toISOString()} title={formatAbsoluteTime(new Date(e.atMs).toISOString())}>
                {formatRelativeTime(new Date(e.atMs).toISOString())}
                {e.via && ` · ${e.via}`}
              </time>
            </li>
          ))}
        </ul>
      )}
      {activity.hasNextPage && (
        <div className="border-t border-border px-4 py-3">
          <Button variant="outline" size="sm" loading={activity.isFetchingNextPage} onClick={() => void activity.fetchNextPage()}>
            Show older
          </Button>
        </div>
      )}
    </section>
  );
}
