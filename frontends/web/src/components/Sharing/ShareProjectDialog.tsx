// Copyright (c) 2025 Reliant Labs

/**
 * The owner's share toggle for a project (control-plane
 * docs/design/org-sharing.md §2, §4), the project twin of ShareChatDialog.
 * Sharing is a disclosure, so the dialog says exactly what the org's members
 * will and will not see before the owner confirms it.
 *
 * What a member sees is the viewer's shape of the project
 * (internal/projectproto ForViewer, and ListProjectsWithApps for a viewer):
 * its card, without the path on the owner's machine or its checkouts, and
 * repositories without their remotes. Sharing a project lets members list
 * its shared chats; it never shares a chat that is not shared itself
 * (SharingService.SetProjectVisibility).
 */

import { useEffect, useState } from "react";

import Button from "@/components/forge-ui/button";
import Modal from "@/components/forge-ui/modal";
import { useSetProjectShared } from "@/hooks/sharing-queries";
import { toast } from "@/lib/toast-manager";
import type { Project } from "@/store/projectStore";

import { DisclosureList } from "./ShareChatDialog";
import { shareErrorMessage } from "./shareErrors";

/** What a member of the org can see of a shared project, and what they cannot. */
export const SHARED_PROJECT_SEES = [
  "The project's name, description and git remote",
  "Its repositories by name, and the apps found in its code",
  "Its chats that you share, each one shared on its own",
] as const;

export const SHARED_PROJECT_DOES_NOT_SEE = [
  "Any chat in it you have not shared",
  "Where it is on your machine, or which machines have it",
  "Anything they could act with: they cannot start a chat or a run in it, or change it",
] as const;

interface ShareProjectDialogProps {
  project: Project;
  orgName: string;
  open: boolean;
  onClose: () => void;
}

export function ShareProjectDialog({ project, orgName, open, onClose }: ShareProjectDialogProps) {
  const shared = project.visibility === "org";
  const sharing = useSetProjectShared();
  const [error, setError] = useState("");

  useEffect(() => {
    if (open) setError("");
  }, [open]);

  const confirm = async () => {
    setError("");
    try {
      if (shared) {
        await sharing.unshare(project.id);
        toast.success("Stopped sharing");
      } else {
        await sharing.share(project.id);
        toast.success(`Shared with ${orgName}`);
      }
      onClose();
    } catch (err) {
      setError(shareErrorMessage(err, "Could not change who can read this project"));
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={shared ? `Stop sharing ${project.name} with ${orgName}?` : `Share ${project.name} with ${orgName}`}
      description={
        shared
          ? `Members of ${orgName} will no longer see this project. Chats in it you shared stay shared until you stop sharing them. You can share it again at any time.`
          : `Every member of ${orgName} will see this project, read-only. Anyone who leaves ${orgName} loses access.`
      }
      size="md"
      footer={
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={sharing.isPending}>
            Cancel
          </Button>
          <Button
            variant={shared ? "outline" : "primary"}
            onClick={() => void confirm()}
            disabled={sharing.isPending}
            data-testid="share-project-confirm"
          >
            {sharing.isPending ? "Saving…" : shared ? "Stop sharing" : `Share with ${orgName}`}
          </Button>
        </div>
      }
    >
      <div className="space-y-4 px-6 py-4 text-ui">
        {!shared && (
          <div className="grid gap-4 sm:grid-cols-2">
            <DisclosureList title="Members can see" items={SHARED_PROJECT_SEES} kind="sees" />
            <DisclosureList title="Members cannot see" items={SHARED_PROJECT_DOES_NOT_SEE} kind="hidden" />
          </div>
        )}
        {!shared && (
          <p className="text-meta text-muted-foreground">
            Sharing the project shares none of its chats: share each chat you want them to read from the chat itself. An
            admin of {orgName} can make the project private again.
          </p>
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
