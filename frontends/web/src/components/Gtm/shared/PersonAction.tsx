// Copyright (c) 2026 Reliant Labs

/**
 * The person-only actions (plan B.3 rule 4): approve an item, verify a claim.
 * (Accepting or rejecting an idea is the third; it is on the idea's page.)
 * An agent can never make them: the server refuses an agent actor. Everyone
 * using this web app is a person, so the buttons are real; each opens a
 * dialog that says what the action records before it is confirmed.
 *
 * There is no publish or send action anywhere in GTM. An email goes out only
 * when the agent sends it, after a person approves the card it raises in the
 * chat (EmailSendNote).
 */

import { BadgeCheck, Mail, ShieldCheck, UserRound } from "lucide-react";
import type { ReactNode } from "react";

import Button from "@/components/forge-ui/button";
import Modal from "@/components/forge-ui/modal";
import Tag from "@/components/forge-ui/tag";

export type PersonAction = "approve" | "verify";

const ACTION: Record<PersonAction, { label: string; icon: ReactNode; title: string; confirm: string }> = {
  approve: { label: "Approve", icon: <ShieldCheck />, title: "Approve this item", confirm: "Approve" },
  verify: { label: "Verify", icon: <BadgeCheck />, title: "Verify this fact", confirm: "Mark verified" },
};

/** "Only a person", beside an action an agent cannot take. */
export function PersonOnlyTag() {
  return <Tag icon={<UserRound className="size-3" aria-hidden="true" />}>Only a person</Tag>;
}

/** The button that opens a person-only action's dialog. */
export function PersonActionButton({
  action,
  subject,
  onClick,
  size = "sm",
  disabled,
}: {
  action: PersonAction;
  /** What it acts on, for the button's accessible name. */
  subject: string;
  onClick: () => void;
  size?: "xs" | "sm" | "md";
  disabled?: boolean;
}) {
  const a = ACTION[action];
  return (
    <Button
      variant="outline"
      size={size}
      leftIcon={a.icon}
      onClick={onClick}
      disabled={disabled}
      aria-haspopup="dialog"
      aria-label={`${a.label}: ${subject}`}
      data-person-action={action}
    >
      {a.label}
    </Button>
  );
}

/** Confirms a person-only action: what it records, then the write itself (`onConfirm`). */
export function PersonActionDialog({
  open,
  onClose,
  action,
  subject,
  onConfirm,
  busy,
  confirmDisabled,
  children,
}: {
  open: boolean;
  onClose: () => void;
  action: PersonAction;
  subject: string;
  onConfirm: () => void;
  busy?: boolean;
  /** The write cannot go yet (a field below is missing). */
  confirmDisabled?: boolean;
  /** What else the dialog shows: the evidence a verification needs, the findings an approval overrides. */
  children?: ReactNode;
}) {
  const a = ACTION[action];
  return (
    <Modal
      open={open}
      onClose={onClose}
      size="md"
      sheetOnMobile
      title={a.title}
      description={subject}
      footer={
        <div className="flex w-full justify-end gap-2">
          <Button variant="secondary" size="sm" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" size="sm" onClick={onConfirm} loading={busy} disabled={confirmDisabled}>
            {a.confirm}
          </Button>
        </div>
      }
    >
      <div className="space-y-4 text-sm text-ink" data-testid={`person-action-dialog-${action}`}>
        <div className="flex items-center gap-2">
          <PersonOnlyTag />
          <span className="text-ink-muted">Agents cannot do this.</span>
        </div>
        {action === "approve" && (
          <p>
            Approving records your name against this version. It does not publish or send anything: posting stays a
            separate step, and an email goes out only through the agent, after you approve its card in the chat.
          </p>
        )}
        {action === "verify" && (
          <p>
            Verifying records you as the person who checked this fact against its source. Claim checks treat only
            verified facts as support for copy.
          </p>
        )}
        {children}
      </div>
    </Modal>
  );
}

/** An email item: how it goes out. Nothing on these pages sends it. */
export function EmailSendNote() {
  return (
    <p className="flex items-start gap-2 text-ink-muted" data-testid="gtm-email-send-note">
      <Mail className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <span>
        Sending happens through the agent: it raises an approval card in the chat with exactly these recipients, this
        subject and this body, and sends from your Gmail only after you approve it there. Nothing sends from this page.
      </span>
    </p>
  );
}
