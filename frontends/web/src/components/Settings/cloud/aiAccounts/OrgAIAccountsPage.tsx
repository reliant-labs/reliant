// Copyright (c) 2025 Reliant Labs

/**
 * Settings › Organization › AI accounts: what the active org pays for that its
 * members' chats run on (control-plane docs/design/org-provider-accounts.md §7).
 *
 *   API keys          the org's own keys: add, rotate, pause, rename, who may
 *                     use each, their order, delete (owners and admins); the
 *                     provider, label and status of each (members)
 *   Team workspaces   the Claude Team / ChatGPT Business workspaces the org
 *                     pays for, and the members' seats they matched (owners
 *                     and admins)
 *
 * Grants for "Selected people" keys are on Members › Permissions, beside the
 * environment grid. Adding a key and binding a workspace are refused until the
 * deployment enables them (`adding_enabled`); the page says so and keeps every
 * way to stop the org's spend (pause, rotate, delete, unbind, release).
 *
 * Keyed by org: a switch remounts the page, so nothing of one org can be acted
 * on in the next.
 */

import { useState } from "react";
import { Plus } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { useActiveOrg } from "@/hooks/useOrgs";
import { useOrgProviderAccounts } from "@/hooks/org-provider-account-queries";
import { canAdministerMembers, memberNamesBySubject } from "@/services/controlPlane/members";
import type { MyOrg } from "@/services/controlPlane/orgs";
import { orgAccountErrorMessage } from "@/types/orgProviderAccount";

import { SettingsPageHeader } from "../../SettingsPageHeader";
import { useMembers } from "../members/useMembers";
import { AddOrgKeyDialog } from "./OrgKeyDialogs";
import { OrgKeysSection } from "./OrgKeysSection";
import { TeamWorkspacesSection } from "./TeamWorkspacesSection";

const TITLE = "AI accounts";

export function OrgAIAccountsPage() {
  const { active, isLoading } = useActiveOrg();
  if (!active) {
    return (
      <div className="space-y-6">
        <SettingsPageHeader title={TITLE} description="The AI accounts your organization pays for." />
        <p className="text-sm text-muted-foreground" role="status">
          {isLoading ? "Loading your organization…" : "You're not in an organization yet."}
        </p>
      </div>
    );
  }
  return <OrgAIAccounts key={active.id} org={active} />;
}

function OrgAIAccounts({ org }: { org: MyOrg }) {
  const q = useOrgProviderAccounts(org.id);
  const [adding, setAdding] = useState(false);
  // The server's word on the role wins; until it answers, the membership list's.
  const isAdmin = q.data?.callerIsAdmin ?? canAdministerMembers(org.role);
  const addingEnabled = q.data?.addingEnabled ?? false;
  const members = useMembers(isAdmin ? org.id : "");
  const names = memberNamesBySubject(members.data?.pages.flatMap((p) => p.members) ?? []);

  const addButton = isAdmin && addingEnabled && (
    <Button variant="primary" leftIcon={<Plus className="h-4 w-4" aria-hidden="true" />} onClick={() => setAdding(true)} data-testid="org-key-add">
      Add API key
    </Button>
  );

  return (
    <div className="forge-ui space-y-6" data-testid="org-ai-accounts-page">
      <SettingsPageHeader
        title={TITLE}
        description={`The AI accounts ${org.name} pays for. Chats in ${org.name} use them before anyone's own accounts, so members' own credits are saved.`}
        actions={addButton}
      />

      {isAdmin && q.data && !addingEnabled && (
        <p role="note" className="rounded-md border border-border/60 bg-background px-3 py-2 text-sm text-muted-foreground" data-testid="org-accounts-disabled">
          Adding organization AI accounts isn't enabled on this deployment yet. Existing keys and workspaces can still be paused, rotated, removed
          and unbound.
        </p>
      )}

      {q.isLoading ? (
        <p className="text-sm text-muted-foreground" role="status">
          Loading {org.name}'s AI accounts…
        </p>
      ) : q.error ? (
        <div role="alert" className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border bg-card p-4 text-sm" data-testid="org-accounts-error">
          <span className="text-foreground">{orgAccountErrorMessage(q.error, `${org.name}'s AI accounts could not be loaded.`)}</span>
          <Button variant="outline" size="sm" onClick={() => void q.refetch()}>
            Try again
          </Button>
        </div>
      ) : (
        <>
          <OrgKeysSection org={org} accounts={q.data?.accounts ?? []} isAdmin={isAdmin} emptyAction={addButton} />
          {isAdmin && <TeamWorkspacesSection org={org} names={names} />}
        </>
      )}

      {isAdmin && <AddOrgKeyDialog open={adding} onClose={() => setAdding(false)} org={org} />}
    </div>
  );
}
