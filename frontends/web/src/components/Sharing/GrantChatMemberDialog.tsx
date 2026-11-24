// Copyright (c) 2025 Reliant Labs

/**
 * Give a person access to a chat, or change the level they hold
 * (control-plane docs/design/chat-collaboration.md §2.1, §3.2). View is the
 * default. Choosing Reply or Manage shows, before anything is saved, what
 * that means: their messages go to the owner's agent, on the owner's machine,
 * with the owner's credentials, paid for by the org. Until the reply path
 * ships the dialog also says what the level does today, so it never promises
 * what does not exist yet.
 *
 * The person picker offers live members of the chat's org only (the roster
 * lists no one who left), minus the owner and anyone who already has a grant.
 */

import { useEffect, useMemo, useState } from "react";

import Button from "@/components/forge-ui/button";
import Combobox, { type ComboboxOption } from "@/components/forge-ui/combobox";
import { FormField } from "@/components/forge-ui/form";
import Label from "@/components/forge-ui/label";
import Modal from "@/components/forge-ui/modal";
import { ChatMemberRole } from "@/gen/reliant/v1/sharing_pb";
import { useGrantChatMember, useOrgRoster } from "@/hooks/chat-member-queries";
import { toast } from "@/lib/toast-manager";
import { cn } from "@/lib/utils";

import {
  DEFAULT_GRANT_ROLE,
  GRANTABLE_ROLES,
  ROLE_LABEL,
  ROLE_SUMMARY,
  ROLE_TODAY,
  isAtMemberCap,
  memberErrorMessage,
  MAX_CHAT_MEMBERS,
  type GrantableRole,
} from "./chatMembers";

/** The person whose level is being changed, rather than a new one picked. */
export interface GrantTarget {
  userId: string;
  name: string;
  role: GrantableRole;
}

export interface GrantChatMemberDialogProps {
  chatId: string;
  orgId: string;
  open: boolean;
  onClose: () => void;
  /** The chat's owner: never offered, and named in the disclosure. */
  ownerUserId: string;
  ownerName: string;
  /** Whether the person granting is the owner ("your agent") or a manager. */
  callerIsOwner: boolean;
  /** Everyone who already holds a grant: not offered again. */
  memberUserIds: readonly string[];
  memberCount: number;
  /** Set to change this person's level; unset to add someone. */
  target?: GrantTarget | null;
}

export function GrantChatMemberDialog({
  chatId,
  orgId,
  open,
  onClose,
  ownerUserId,
  ownerName,
  callerIsOwner,
  memberUserIds,
  memberCount,
  target,
}: GrantChatMemberDialogProps) {
  const adding = !target;
  const roster = useOrgRoster(orgId, open && adding);
  const granting = useGrantChatMember();
  const [userId, setUserId] = useState<string | null>(null);
  const [role, setRole] = useState<GrantableRole>(DEFAULT_GRANT_ROLE);
  const [error, setError] = useState("");

  // Every opening starts over: no one picked, and View unless changing a
  // person, who starts at the level they hold.
  useEffect(() => {
    if (!open) return;
    setUserId(target?.userId ?? null);
    setRole(target?.role ?? DEFAULT_GRANT_ROLE);
    setError("");
  }, [open, target]);

  const options = useMemo<ComboboxOption[]>(() => {
    const taken = new Set([ownerUserId, ...memberUserIds]);
    return roster.members
      .filter((m) => m.externalId && !taken.has(m.externalId))
      .map((m) => ({
        value: m.externalId,
        label: m.name || m.email,
        description: m.name ? m.email : undefined,
        keywords: [m.email],
      }));
  }, [roster.members, ownerUserId, memberUserIds]);

  // Null until someone is picked: the disclosure then says "them".
  const personName = target?.name ?? options.find((o) => o.value === userId)?.label ?? null;
  const full = adding && isAtMemberCap(memberCount);
  const unchanged = !!target && target.role === role;
  const canSave = !!userId && !full && !unchanged && !granting.isPending;

  const save = async () => {
    if (!userId) return;
    setError("");
    try {
      await granting.grant(chatId, userId, role);
      const who = personName ?? "them";
      toast.success(adding ? `Added ${who} with ${ROLE_LABEL[role]}` : `${who} now has ${ROLE_LABEL[role]}`);
      onClose();
    } catch (err) {
      setError(memberErrorMessage(err, "Could not change who has access to this chat"));
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={adding ? "Add a person" : `Change ${personName}'s access`}
      description={
        adding ? "They must be a member of this chat's organization. They lose access if they leave it." : undefined
      }
      size="md"
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={granting.isPending}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={!canSave} data-testid="grant-member-confirm">
            {granting.isPending ? "Saving…" : adding ? `Add with ${ROLE_LABEL[role]}` : "Save"}
          </Button>
        </div>
      }
    >
      <div className="space-y-4 px-6 py-4 text-ui">
        {full && (
          <p role="status" className="text-meta text-muted-foreground" data-testid="grant-member-full">
            This chat has {MAX_CHAT_MEMBERS} people, the most it can have. Remove someone to add another.
          </p>
        )}

        {adding && (
          <FormField className="forge-ui">
            <Label>Person</Label>
            <Combobox
              options={options}
              value={userId}
              onChange={setUserId}
              placeholder={roster.isLoading ? "Loading members…" : "Choose a member"}
              searchPlaceholder="Search by name or email"
              emptyMessage={roster.error ? "Could not load the organization's members" : "No one else to add"}
              loading={roster.isLoading}
              disabled={full}
            />
          </FormField>
        )}

        <fieldset className="space-y-1.5">
          <legend className="mb-1.5 text-ui font-medium text-foreground">Access</legend>
          {GRANTABLE_ROLES.map((r) => (
            <label
              key={r}
              className={cn(
                "flex cursor-pointer items-start gap-2.5 rounded-md border px-3 py-2",
                role === r ? "border-ring" : "border-border-subtle hover:bg-foreground/5",
              )}
            >
              <input
                type="radio"
                name="grant-member-role"
                className="mt-1"
                value={r}
                checked={role === r}
                onChange={() => setRole(r)}
                data-testid={`grant-member-role-${ROLE_LABEL[r].toLowerCase()}`}
              />
              <span className="min-w-0">
                <span className="font-medium text-foreground">{ROLE_LABEL[r]}</span>
                <span className="block text-meta text-muted-foreground">{ROLE_SUMMARY[r]}</span>
              </span>
            </label>
          ))}
        </fieldset>

        {role !== ChatMemberRole.VIEW && (
          <ReplyDisclosure role={role} personName={personName} ownerName={ownerName} callerIsOwner={callerIsOwner} />
        )}

        {error && (
          <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-meta text-destructive-ink">
            {error}
          </p>
        )}
      </div>
    </Modal>
  );
}

/**
 * The §2.1 disclosure: a reply is not a comment. Named plainly, with whose
 * agent, machine and credentials it is, and what the level does today.
 */
function ReplyDisclosure({
  role,
  personName,
  ownerName,
  callerIsOwner,
}: {
  role: GrantableRole;
  personName: string | null;
  ownerName: string;
  callerIsOwner: boolean;
}) {
  const whose = callerIsOwner ? "your" : `${ownerName}'s`;
  const owner = callerIsOwner ? "you" : ownerName;
  const today = ROLE_TODAY[role];
  return (
    <div className="space-y-2 rounded-md border border-border/60 bg-background p-3 text-meta" data-testid="grant-member-disclosure">
      <p className="font-medium text-foreground">What {ROLE_LABEL[role]} lets {personName ?? "them"} do</p>
      {today && (
        <p className="text-foreground" data-testid="grant-member-today">
          {today} The level is saved now and takes effect then, without granting it again.
        </p>
      )}
      <p className="text-foreground">
        {today ? "Once it ships, their" : "Their"} messages go to {whose} agent, not to {owner}. It runs on {whose} machine, with {whose} credentials, and the
        organization pays for it. {personName ?? "They"} can ask it to read any file {owner} can read and to run any command its
        mode allows, and can approve the tool calls it then asks for.
      </p>
      <p className="text-muted-foreground">
        Tool results stay hidden from them, but the agent&apos;s own words do not, so it can repeat what it read. Grant
        this only to someone you would trust with {whose} machine.
      </p>
    </div>
  );
}
