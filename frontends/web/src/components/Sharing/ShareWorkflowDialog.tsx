// Copyright (c) 2025 Reliant Labs

/**
 * Who can read a saved workflow (control-plane docs/design/org-sharing.md
 * §8.5), the workflow twin of ShareChatDialog. Sharing publishes the
 * workflow's definition for teammates to read and fork
 * (SharingService.SetWorkflowDraftSharing); it never lets them run it as its
 * owner, change it, or see the owner's runs of it. The dialog says so before
 * the owner shares.
 *
 * The dialog is a frame of sections, each one way of granting access, so a
 * later one joins without reshaping it: today the org-wide toggle
 * (WorkflowOrgSharingSection); per-person grants (control-plane#3580, P4)
 * are planned as a "People with access" section beside it.
 */

import { useEffect, useState } from "react";

import Button from "@/components/forge-ui/button";
import Modal from "@/components/forge-ui/modal";
import { useSetWorkflowDraftShared } from "@/hooks/sharing-queries";
import { useActiveOrg } from "@/hooks/useOrgs";
import { ResourceVisibility } from "@/gen/reliant/v1/common_pb";
import { toast } from "@/lib/toast-manager";

import { DisclosureList } from "./ShareChatDialog";
import { shareErrorMessage } from "./shareErrors";
import type { ShareableWorkflow } from "./shareTargets";

/** What a teammate gets of a shared workflow, and what they do not. */
export const SHARED_WORKFLOW_GETS = [
  "Its definition: steps, prompts, inputs and triggers",
  "A copy of their own to fork, change and run",
] as const;

export const SHARED_WORKFLOW_DOES_NOT_GET = [
  "Running it as you: a fork runs as them, on their own machine and account",
  "Changing your workflow",
  "Your runs of it, or anything they produced",
] as const;

interface ShareWorkflowDialogProps {
  /** The saved workflow whose access to manage; null closes the dialog. */
  workflow: ShareableWorkflow | null;
  onClose: () => void;
}

export function ShareWorkflowDialog({ workflow, onClose }: ShareWorkflowDialogProps) {
  return (
    <Modal
      open={workflow !== null}
      onClose={onClose}
      title={workflow ? `Share “${workflow.name}”` : "Share workflow"}
      description="Teammates get its definition, to read and fork. They cannot run it as you."
      size="md"
      footer={
        <div className="flex justify-end">
          <Button variant="ghost" onClick={onClose}>
            Done
          </Button>
        </div>
      }
    >
      {workflow && (
        <div className="divide-y divide-border-subtle px-6 text-ui">
          {/* keyed by the workflow, so its state is this workflow's alone */}
          <WorkflowOrgSharingSection key={workflow.draftId} workflow={workflow} />
        </div>
      )}
    </Modal>
  );
}

/**
 * The org-wide grant: every live member of the workflow's org reads it and
 * may fork it, or only its owner does. It shows the state the server last
 * answered, so the dialog can stay open after a change.
 */
export function WorkflowOrgSharingSection({ workflow }: { workflow: ShareableWorkflow }) {
  const { orgs } = useActiveOrg();
  const orgName = orgs.find((o) => o.id === workflow.orgId)?.name ?? "your organization";
  const [shared, setShared] = useState(workflow.shared);
  const sharing = useSetWorkflowDraftShared();
  const [error, setError] = useState("");

  useEffect(() => setShared(workflow.shared), [workflow.shared]);

  const toggle = async () => {
    setError("");
    try {
      const res = shared ? await sharing.unshare(workflow.draftId) : await sharing.share(workflow.draftId);
      // The answer is the authority; without one (an admin's view), the ask.
      const next = res.workflow ? res.workflow.visibility === ResourceVisibility.ORG : !shared;
      setShared(next);
      toast.success(next ? `Shared with ${orgName}` : "Stopped sharing");
    } catch (err) {
      setError(shareErrorMessage(err, "Could not change who can read this workflow"));
    }
  };

  return (
    <section aria-label={`Everyone in ${orgName}`} className="space-y-4 py-4" data-testid="share-workflow-org">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <h3 className="text-ui font-medium text-foreground">Everyone in {orgName}</h3>
          <p className="text-meta text-muted-foreground" data-testid="share-workflow-org-state">
            {shared ? "Shared: every member can read and fork it." : "Private: only you can see it."}
          </p>
        </div>
        <Button
          variant={shared ? "outline" : "primary"}
          size="sm"
          onClick={() => void toggle()}
          disabled={sharing.isPending}
          data-testid="share-workflow-confirm"
        >
          {sharing.isPending ? "Saving…" : shared ? "Stop sharing" : `Share with ${orgName}`}
        </Button>
      </div>
      {!shared && (
        <div className="grid gap-4 sm:grid-cols-2">
          <DisclosureList title="Members get" items={SHARED_WORKFLOW_GETS} kind="sees" />
          <DisclosureList title="Members do not get" items={SHARED_WORKFLOW_DOES_NOT_GET} kind="hidden" />
        </div>
      )}
      <p className="text-meta text-muted-foreground">
        {shared
          ? `Copies members already forked stay theirs. Anyone who leaves ${orgName} loses access.`
          : `The definition is shared as written, so a secret typed into a prompt or command is visible. An admin of ${orgName} can make it private again.`}
      </p>
      {error && (
        <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-meta text-destructive-ink">
          {error}
        </p>
      )}
    </section>
  );
}
