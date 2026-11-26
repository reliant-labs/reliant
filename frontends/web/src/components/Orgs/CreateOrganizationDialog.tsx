// Copyright (c) 2025 Reliant Labs

import { useEffect, useId, useState } from "react";
import { useNavigate } from "@tanstack/react-router";

import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import Modal from "@/components/forge-ui/modal";
import { settingsPath } from "@/lib/paths";
import { toast } from "@/lib/toast-manager";
import { createOrganization, memberErrorMessage } from "@/services/controlPlane/members";
import { switchToJoinedOrg } from "@/services/controlPlane/orgs";

const MAX_NAME = 80;

/**
 * Create an organization: the caller becomes its owner, and the app switches
 * to it at once (a view choice, like the switcher; the server-side default
 * moves only when the person had none).
 *
 * Then a next step in the same dialog: free static hosting stays with the
 * person's one free hosting org, so in a new org a backend or a static site
 * waits on app hosting billing,
 * so the dialog offers it now rather than at the first queued deploy. It
 * never blocks: "Not now" closes, and the org exists either way. Inviting
 * people stays in Settings › Members (the closing toast says so).
 */
export function CreateOrganizationDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<{ id: string; name: string } | null>(null);
  const nameId = useId();

  useEffect(() => {
    if (!open) return;
    setName("");
    setError(null);
    setCreated(null);
  }, [open]);

  // Closing the next step any way but "Set up hosting billing" is "Not now".
  const notNow = () => {
    if (created) toast.success(`Created ${created.name}. Invite people to it from Settings › Members.`);
    onClose();
  };

  const setUpHostingBilling = () => {
    if (!created) return;
    // The org is the active one already (switchToJoinedOrg); `org` still
    // names it, so billing's gate asks if the person switched away meanwhile.
    void navigate({
      to: settingsPath("billing"),
      search: { tab: "hosting", org: created.id },
      hash: "app-hosting",
    });
    onClose();
  };

  const trimmed = name.trim();
  const valid = trimmed.length > 0 && trimmed.length <= MAX_NAME;

  const submit = async () => {
    if (!valid || busy) return;
    setBusy(true);
    setError(null);
    try {
      const org = await createOrganization(trimmed);
      await switchToJoinedOrg(org.id);
      setCreated({ id: org.id, name: org.name || trimmed });
    } catch (err) {
      setError(memberErrorMessage(err, "create", "Could not create the organization. Try again."));
    } finally {
      setBusy(false);
    }
  };

  if (created) {
    return (
      <Modal
        open={open}
        onClose={notNow}
        title={`Created ${created.name}`}
        description="Next: set up app hosting billing, so this organization can host backends and static sites. Free static hosting stays with your one free hosting organization."
        size="md"
        footer={
          <>
            <Button variant="ghost" onClick={notNow} data-testid="create-org-not-now">
              Not now
            </Button>
            <Button variant="primary" onClick={setUpHostingBilling} data-testid="create-org-hosting-billing">
              Set up hosting billing
            </Button>
          </>
        }
      >
        <p className="text-sm text-muted-foreground" data-testid="create-org-next-step">
          It isn't required to use the organization: you can set it up any time from Settings › Billing › App hosting.
        </p>
      </Modal>
    );
  }

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
