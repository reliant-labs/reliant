// Copyright (c) 2025 Reliant Labs

/**
 * The audited environment's question, asked once per tab session
 * (dataAccess-model.ts): why are you reading this environment's data?
 *
 * It names the environment in the title and on the button, so confirming is a
 * deliberate act about THAT environment — the console's twin of
 * `forge db query --confirm-env prod --reason "…"`. Nothing is read until it
 * is answered: the page behind shows what is waiting, not data.
 */

import { useId, useState } from "react";

import Button from "@/components/forge-ui/button";
import Label from "@/components/forge-ui/label";
import Modal from "@/components/forge-ui/modal";
import Textarea from "@/components/forge-ui/textarea";

import { REASON_MAX, validReason } from "./dataAccess-model";

export function AccessReasonDialog({
  open,
  env,
  initialReason = "",
  onConfirm,
  onCancel,
}: {
  open: boolean;
  env: string;
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
      title={`Reading ${env}'s data is audited`}
      description={`Every table you open and every page you read in ${env} is recorded in the audit log and the project's Activity, with your name and this reason. It's asked once for this tab.`}
      size="md"
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onCancel} data-testid="data-access-cancel">
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} disabled={!valid} data-testid="data-access-confirm">
            Read {env}&apos;s data
          </Button>
        </div>
      }
    >
      <form
        className="space-y-2"
        data-testid="data-access-dialog"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <Label htmlFor={id}>Why are you reading {env}&apos;s data?</Label>
        <Textarea
          id={id}
          autoFocus
          value={reason}
          maxLength={REASON_MAX}
          placeholder="Investigating #4412: orders stuck in pending"
          onChange={(event) => setReason(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) submit();
          }}
          data-testid="data-access-reason"
        />
        <p className="text-xs text-ink-muted">
          Teammates see it in Activity next to each read. {reason.length > REASON_MAX - 50 ? `${REASON_MAX - reason.length} characters left.` : ""}
        </p>
      </form>
    </Modal>
  );
}
