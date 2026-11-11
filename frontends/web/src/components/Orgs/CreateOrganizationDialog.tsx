// Copyright (c) 2025 Reliant Labs

import { useEffect, useId, useState } from "react";

import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import Modal from "@/components/forge-ui/modal";
import { toast } from "@/lib/toast-manager";
import { createOrganization, memberErrorMessage } from "@/services/controlPlane/members";
import { switchToJoinedOrg } from "@/services/controlPlane/orgs";

const MAX_NAME = 80;

/**
 * Create an organization: the caller becomes its owner, and the app switches
 * to it at once (a view choice, like the switcher; the server-side default
 * moves only when the person had none). Inviting people is the next step, in
 * Settings › Members.
 */
export function CreateOrganizationDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const nameId = useId();

  useEffect(() => {
    if (!open) return;
    setName("");
    setError(null);
  }, [open]);

  const trimmed = name.trim();
  const valid = trimmed.length > 0 && trimmed.length <= MAX_NAME;

  const submit = async () => {
    if (!valid || busy) return;
    setBusy(true);
    setError(null);
    try {
      const org = await createOrganization(trimmed);
      await switchToJoinedOrg(org.id);
      toast.success(`Created ${org.name || trimmed}. Invite people to it from Settings › Members.`);
      onClose();
    } catch (err) {
      setError(memberErrorMessage(err, "create", "Could not create the organization. Try again."));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Create an organization"
      description="A place to deploy apps and work with other people. You'll be its owner, and can invite people to it."
      size="md"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={busy} disabled={!valid} onClick={() => void submit()} data-testid="create-org-submit">
            Create organization
          </Button>
        </>
      }
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <div className="space-y-1.5">
          <label htmlFor={nameId} className="block text-sm font-medium text-foreground">
            Name
          </label>
          <Input
            id={nameId}
            autoFocus
            maxLength={MAX_NAME}
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Acme Inc."
            data-testid="create-org-name"
          />
        </div>
        {error && (
          <p role="alert" className="rounded-md border border-destructive/40 bg-background px-3 py-2 text-sm text-destructive-ink" data-testid="create-org-error">
            {error}
          </p>
        )}
        <button type="submit" hidden />
      </form>
    </Modal>
  );
}
