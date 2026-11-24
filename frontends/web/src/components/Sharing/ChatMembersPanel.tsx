// Copyright (c) 2025 Reliant Labs

/**
 * Who has access to a chat (control-plane docs/design/chat-collaboration.md
 * §2.2, §3.2, §14): the owner, and the people granted View, Reply or Manage.
 *
 * What each caller sees is what ListChatMembers answers them: the owner and
 * managers see every member and level, and may add people (when the
 * deployment allows grants: chat_collaboration.enabled), change a level or
 * remove anyone; reply members see the owner and the reply and manage
 * members; viewers see the owner and a count. Any member may remove their own
 * grant. Removing is never gated, so a grant made while granting was on can
 * always be taken back.
 */

import { useState, type ReactNode } from "react";
import { Users } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Modal from "@/components/forge-ui/modal";
import { ChatMemberRole, type ChatMember } from "@/gen/reliant/v1/sharing_pb";
import { useChatMembers, useRevokeChatMember } from "@/hooks/chat-member-queries";
import { useOrgMemberNames } from "@/hooks/sharing-queries";
import { toast } from "@/lib/toast-manager";
import { useAuthStore } from "@/store/authStore";
import { leaveSharedChat } from "@/store/openChat";

import {
  canAdministerMembers,
  isAtMemberCap,
  isGrantableRole,
  MAX_CHAT_MEMBERS,
  memberErrorMessage,
  ROLE_LABEL,
} from "./chatMembers";
import { GrantChatMemberDialog, type GrantTarget } from "./GrantChatMemberDialog";

export interface ChatMembersPanelProps {
  chatId: string;
  orgId: string;
  open: boolean;
  onClose: () => void;
  /** Shared with the whole org: everyone there can view it besides its members. */
  sharedWithOrg: boolean;
}

export function ChatMembersPanel({ chatId, orgId, open, onClose, sharedWithOrg }: ChatMembersPanelProps) {
  const query = useChatMembers(open ? chatId : null, orgId);
  const names = useOrgMemberNames(open ? orgId : null);
  const selfId = useAuthStore((s) => s.user?.id ?? null);
  const revoking = useRevokeChatMember();
  const [grantOpen, setGrantOpen] = useState(false);
  const [target, setTarget] = useState<GrantTarget | null>(null);
  const [confirmLeave, setConfirmLeave] = useState(false);
  const [error, setError] = useState("");

  const data = query.data;
  const callerRole = data?.callerRole ?? ChatMemberRole.VIEW;
  const isOwner = !!data && callerRole === ChatMemberRole.UNSPECIFIED;
  const admin = !!data && canAdministerMembers(callerRole);
  const members = data?.members ?? [];
  const memberCount = data?.memberCount ?? 0;
  const ownerName = (data && names.get(data.ownerUserId)) || "The owner";
  const nameOf = (userId: string) => names.get(userId) || "A member";
  // A viewer of a private chat can read it only through a grant of their own.
  const viewerHoldsGrant = callerRole === ChatMemberRole.VIEW && !sharedWithOrg;
  const canAdd = admin && !!data?.grantsEnabled;
  const full = isAtMemberCap(memberCount);

  const close = () => {
    setConfirmLeave(false);
    setError("");
    onClose();
  };

  const openGrant = (next: GrantTarget | null) => {
    setTarget(next);
    setGrantOpen(true);
  };

  const revoke = (userId: string) => revoking.revoke(chatId, userId);

  const remove = async (member: ChatMember) => {
    setError("");
    try {
      await revoke(member.userId);
      toast.success(`Removed ${nameOf(member.userId)}`);
    } catch (err) {
      setError(memberErrorMessage(err, "Could not remove them"));
    }
  };

  const leave = async () => {
    if (!selfId) return;
    setError("");
    try {
      await revoke(selfId);
      toast.success("You no longer have your own access to this chat");
      close();
      // A private chat is now unreadable: close it here rather than waiting
      // for the stream's re-check to notice.
      if (!sharedWithOrg) leaveSharedChat(chatId);
    } catch (err) {
      setError(memberErrorMessage(err, "Could not remove your access"));
    }
  };

  const hiddenCount = Math.max(0, memberCount - members.length);

  return (
    <>
      <Modal
        open={open}
        onClose={close}
        title="People with access"
        description={
          sharedWithOrg
            ? "Shared with the organization: every member can view it. The people below have their own access."
            : "Only the owner and the people with their own access can open this chat."
        }
        size="md"
        footer={
          <div className="flex items-center justify-end gap-2">
            {admin && (
              <span className="mr-auto text-meta text-muted-foreground" data-testid="chat-members-count">
                {memberCount} of {MAX_CHAT_MEMBERS} people
              </span>
            )}
            <Button variant={canAdd ? "ghost" : "outline"} onClick={close}>
              Done
            </Button>
            {canAdd && (
              <Button variant="primary" onClick={() => openGrant(null)} disabled={full} data-testid="chat-members-add">
                Add a person
              </Button>
            )}
          </div>
        }
      >
        <div className="space-y-3 px-6 py-4 text-ui">
          {query.isLoading && <p className="text-muted-foreground">Loading…</p>}
          {query.error && (
            <p role="alert" className="text-meta text-destructive-ink">
              {memberErrorMessage(query.error, "Could not load who has access")}
            </p>
          )}

          {data && (
            <ul className="divide-y divide-border-subtle" data-testid="chat-members-list">
              <MemberRow name={ownerName} you={isOwner} level="Owner" />
              {members.map((m) => {
                const you = !!selfId && m.userId === selfId;
                const role = isGrantableRole(m.role) ? m.role : null;
                return (
                  <MemberRow key={m.userId} name={nameOf(m.userId)} you={you} level={role ? ROLE_LABEL[role] : ""} testId={`chat-member-${m.userId}`}>
                    {admin && !you && role && (
                      <>
                        {data.grantsEnabled && (
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => openGrant({ userId: m.userId, name: nameOf(m.userId), role })}
                            data-testid={`chat-member-change-${m.userId}`}
                          >
                            Change
                          </Button>
                        )}
                        <Button
                          variant="ghost"
                          size="sm"
                          onClick={() => void remove(m)}
                          disabled={revoking.isPending}
                          data-testid={`chat-member-remove-${m.userId}`}
                        >
                          Remove
                        </Button>
                      </>
                    )}
                    {you && !confirmLeave && (
                      <Button variant="ghost" size="sm" onClick={() => setConfirmLeave(true)} data-testid="chat-member-leave">
                        Leave
                      </Button>
                    )}
                  </MemberRow>
                );
              })}
            </ul>
          )}

          {data && hiddenCount > 0 && (
            <p className="text-meta text-muted-foreground" data-testid="chat-members-hidden">
              {members.length === 0
                ? `${hiddenCount} ${hiddenCount === 1 ? "person has" : "people have"} their own access.`
                : `${hiddenCount} more can view.`}
            </p>
          )}

          {data && admin && memberCount === 0 && (
            <div className="flex flex-col items-center px-4 py-6 text-center" data-testid="chat-members-empty">
              <Users className="size-5 text-muted-foreground" aria-hidden />
              <p className="mt-2 font-medium text-foreground">No one else has their own access yet</p>
              <p className="mt-1 max-w-sm text-meta text-muted-foreground">
                {data.grantsEnabled
                  ? "Add a person from your organization. They start with View, and you choose whether they can do more."
                  : "Adding people to a chat is not turned on for this deployment yet."}
              </p>
            </div>
          )}

          {data && admin && !data.grantsEnabled && memberCount > 0 && (
            <p className="text-meta text-muted-foreground" data-testid="chat-members-grants-off">
              Adding people is turned off on this deployment. You can still remove anyone&apos;s access.
            </p>
          )}

          {data && canAdd && full && (
            <p className="text-meta text-muted-foreground" data-testid="chat-members-full">
              This chat has {MAX_CHAT_MEMBERS} people, the most it can have. Remove someone to add another.
            </p>
          )}

          {data && viewerHoldsGrant && !confirmLeave && (
            <Button variant="outline" size="sm" onClick={() => setConfirmLeave(true)} data-testid="chat-member-leave">
              Remove my access
            </Button>
          )}

          {confirmLeave && (
            <div className="flex flex-wrap items-center gap-2 rounded-md border border-border/60 bg-background p-3" data-testid="chat-member-leave-confirm">
              <p className="mr-auto text-meta text-foreground">
                {sharedWithOrg
                  ? "Remove your own access? You can still view it while it is shared with the organization."
                  : "Remove your access? You will not be able to open this chat until someone adds you again."}
              </p>
              <Button variant="ghost" size="sm" onClick={() => setConfirmLeave(false)}>
                Cancel
              </Button>
              <Button variant="danger" size="sm" onClick={() => void leave()} disabled={revoking.isPending} data-testid="chat-member-leave-confirm-button">
                Remove my access
              </Button>
            </div>
          )}

          {error && (
            <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-meta text-destructive-ink">
              {error}
            </p>
          )}
        </div>
      </Modal>

      {data && admin && (
        <GrantChatMemberDialog
          chatId={chatId}
          orgId={orgId}
          open={grantOpen}
          onClose={() => setGrantOpen(false)}
          ownerUserId={data.ownerUserId}
          ownerName={ownerName}
          callerIsOwner={isOwner}
          memberUserIds={members.map((m) => m.userId)}
          memberCount={memberCount}
          target={target}
        />
      )}
    </>
  );
}

function MemberRow({
  name,
  you,
  level,
  testId,
  children,
}: {
  name: string;
  you: boolean;
  level: string;
  testId?: string;
  children?: ReactNode;
}) {
  return (
    <li className="flex min-h-[var(--row-h)] items-center gap-2 py-1.5" data-testid={testId}>
      <span className="flex size-7 flex-shrink-0 items-center justify-center rounded-full border border-border text-meta text-muted-foreground" aria-hidden>
        {name.charAt(0).toUpperCase()}
      </span>
      <span className="min-w-0 flex-1 truncate text-foreground">
        {name}
        {you && <span className="text-muted-foreground"> (you)</span>}
      </span>
      <span className="text-meta text-muted-foreground">{level}</span>
      {children}
    </li>
  );
}
