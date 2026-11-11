// Copyright (c) 2025 Reliant Labs

/**
 * Settings › Members: who belongs to the active org, and the means to change it.
 *
 *   Members      the roster (role, change role, remove), the seat count,
 *                inviting, the pending invitations, and leaving
 *   Permissions  the per-permission grant editor (MemberPermissions): roles
 *                are presets of grants, and a grant can be edited one at a time
 *   Activity     who created, invited, joined, left, was removed or changed
 *                role (admins and owners)
 *
 * Membership administration is gated on the caller's ROLE in the org (admin or
 * owner), never on a grant; only an owner touches owners. The controls a
 * member could not use are not offered, but the server is the control: every
 * refusal still arrives as a reason with its own words (members.ts).
 *
 * Keyed by org: a switch remounts the page, so nothing from the previous
 * org's roster can be shown or acted on in the next one.
 */

import { useState } from "react";
import { UserPlus } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Tabs from "@/components/forge-ui/tabs";
import { useActiveOrg } from "@/hooks/useOrgs";
import type { MyOrg } from "@/services/controlPlane/orgs";
import { canAdministerMembers } from "@/services/controlPlane/members";

import { SettingsPageHeader } from "../../SettingsPageHeader";
import { MemberPermissions } from "../MemberPermissions";
import { InviteDialog } from "./InviteDialog";
import { MemberRoster } from "./MemberRoster";
import { MembershipActivity } from "./MembershipActivity";
import { PendingInvitations } from "./PendingInvitations";
import { LeaveOrganization } from "./LeaveOrganization";
import { useMembers } from "./useMembers";
import { SeatCount } from "./SeatCount";

export function MembersPage() {
  const { active, isLoading } = useActiveOrg();
  if (!active) {
    return (
      <div className="space-y-6">
        <SettingsPageHeader title="Members" description="The people in your organization, and what each of them may do." />
        <p className="text-sm text-muted-foreground" role="status">
          {isLoading ? "Loading your organization…" : "You're not in an organization yet."}
        </p>
      </div>
    );
  }
  return <OrgMembersPage key={active.id} org={active} />;
}

type TabId = "members" | "permissions" | "activity";

function OrgMembersPage({ org }: { org: MyOrg }) {
  const isAdmin = canAdministerMembers(org.role);
  const [tab, setTab] = useState<TabId>("members");
  const [inviteOpen, setInviteOpen] = useState(false);
  const members = useMembers(org.id);
  const seats = members.data?.pages[0]?.seats ?? null;

  const tabs = [
    { id: "members", label: "Members" },
    { id: "permissions", label: "Permissions" },
    ...(isAdmin ? [{ id: "activity", label: "Activity" }] : []),
  ];

  return (
    <div className="forge-ui space-y-6" data-testid="members-page">
      <SettingsPageHeader
        title="Members"
        description={`The people in ${org.name}, and what each of them may do here.`}
        meta={seats && <SeatCount seats={seats} />}
        actions={
          isAdmin && (
            <Button variant="primary" leftIcon={<UserPlus className="h-4 w-4" aria-hidden="true" />} onClick={() => setInviteOpen(true)} data-testid="members-invite">
              Invite member
            </Button>
          )
        }
      />

      <Tabs tabs={tabs} activeTab={tab} onChange={(id) => setTab(id as TabId)} ariaLabel="Members sections" />

      {tab === "members" && (
        <div className="space-y-6">
          <MemberRoster org={org} query={members} />
          {isAdmin && <PendingInvitations org={org} />}
          <LeaveOrganization org={org} />
        </div>
      )}
      {tab === "permissions" && <MemberPermissions embedded />}
      {tab === "activity" && isAdmin && <MembershipActivity org={org} members={members.data?.pages.flatMap((p) => p.members) ?? []} />}

      {isAdmin && <InviteDialog open={inviteOpen} onClose={() => setInviteOpen(false)} org={org} seats={seats} />}
    </div>
  );
}
