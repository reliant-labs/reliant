// Copyright (c) 2025 Reliant Labs

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import Button from "@/components/forge-ui/button";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import { toast } from "@/lib/toast-manager";
import { leaveOrganization, memberErrorMessage } from "@/services/controlPlane/members";
import { orgKeys, switchToJoinedOrg, type MyOrg } from "@/services/controlPlane/orgs";

/**
 * Leave the org. The last owner cannot (the server says `last_owner`, and the
 * copy says what to do instead). Afterwards the app acts in the person's
 * default org, which the server returns; a person left in no org gets a fresh
 * one at their next sign-in.
 */
export function LeaveOrganization({ org }: { org: MyOrg }) {
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const leave = async () => {
    setBusy(true);
    setError(null);
    try {
      const { defaultOrgId } = await leaveOrganization(org.id);
      setConfirming(false);
      toast.success(`You left ${org.name}.`);
      if (defaultOrgId) await switchToJoinedOrg(defaultOrgId);
      else await qc.invalidateQueries({ queryKey: orgKeys.mine });
    } catch (err) {
      setConfirming(false);
      setError(memberErrorMessage(err, "leave", `Could not leave ${org.name}.`));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="space-y-3 rounded-lg border border-border bg-card p-4" aria-labelledby="members-leave-heading">
      <div>
        <h3 id="members-leave-heading" className="text-sm font-semibold text-foreground">
          Leave {org.name}
        </h3>
        <p className="mt-1 text-sm text-muted-foreground">
          You lose access to its apps, environments and secrets. Your own chats, projects and machines are yours and stay with you.
        </p>
      </div>
      {error && (
        <p role="alert" className="text-sm text-destructive-ink" data-testid="members-leave-error">
          {error}
        </p>
      )}
      <Button variant="danger" size="sm" onClick={() => setConfirming(true)} data-testid="members-leave">
        Leave organization
      </Button>
      <ConfirmationDialog
        open={confirming}
        title={`Leave ${org.name}?`}
        description="Access tokens you made for this organization are revoked. To come back, someone has to invite you again."
        confirmLabel="Leave"
        loading={busy}
        onConfirm={() => void leave()}
        onCancel={() => setConfirming(false)}
      />
    </section>
  );
}
