// Copyright (c) 2025 Reliant Labs

/**
 * The audited environment's question, asked once per tab session
 * (accessSession-model.ts): why are you reading this environment's data, or
 * its files?
 *
 * It names the environment in the title and on the button, so confirming is a
 * deliberate act about THAT environment — the console's twin of
 * `forge db query --confirm-env prod --reason "…"`. Nothing is read until it
 * is answered: the page behind shows what is waiting, not contents.
 *
 * Shared by every audited surface; each says what it reads (`subject`: "data",
 * "files") and what its reads record (`description`), and keeps its own test
 * ids (`testIdPrefix`: "data" gives data-access-dialog, data-access-reason, …).
 */

import { useId, useState } from "react";

import Button from "@/components/forge-ui/button";
import Label from "@/components/forge-ui/label";
import Modal from "@/components/forge-ui/modal";
import Textarea from "@/components/forge-ui/textarea";

import { REASON_MAX, validReason } from "./accessSession-model";

export function AccessReasonDialog({
  open,
  env,
  subject,
  description,
  placeholder = "Investigating #4412: orders stuck in pending",
  testIdPrefix,
  initialReason = "",
  onConfirm,
  onCancel,
}: {
  open: boolean;
  env: string;
  /** What is read, as a noun: "data", "files". */
  subject: string;
  /** What every read records, and that it is asked once for the tab. */
  description: string;
  placeholder?: string;
  testIdPrefix: string;
  initialReason?: string;
  onConfirm: (reason: string) => void;
  onCancel: () => void;
}) {
  const [reason, setReason] = useState(initialReason);
  const id = useId();
  const valid = validReason(reason);
  const submit = () => {
    if (valid) onConfirm(valid);
  };
  return (
    <Modal
      open={open}
      onClose={onCancel}
      title={`Reading ${env}'s ${subject} is audited`}
      description={description}
      size="md"
      footer={
        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="ghost" className="min-h-[44px] min-w-[44px] sm:min-h-0 sm:min-w-0" onClick={onCancel} data-testid={`${testIdPrefix}-access-cancel`}>
            Cancel
          </Button>
          <Button variant="primary" className="min-h-[44px] min-w-[44px] sm:min-h-0 sm:min-w-0" onClick={submit} disabled={!valid} data-testid={`${testIdPrefix}-access-confirm`}>
            Read {env}&apos;s {subject}
          </Button>
        </div>
      }
    >
      <form
        className="space-y-2"
        data-testid={`${testIdPrefix}-access-dialog`}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <Label htmlFor={id}>
          Why are you reading {env}&apos;s {subject}?
        </Label>
        <Textarea
          id={id}
          autoFocus
          value={reason}
          maxLength={REASON_MAX}
          placeholder={placeholder}
          onChange={(event) => setReason(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) submit();
          }}
          data-testid={`${testIdPrefix}-access-reason`}
        />
        <p className="text-xs text-ink-muted">
          Teammates see it in Activity and the audit log. {reason.length > REASON_MAX - 50 ? `${REASON_MAX - reason.length} characters left.` : ""}
        </p>
      </form>
    </Modal>
  );
}
