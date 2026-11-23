// Copyright (c) 2025 Reliant Labs

/**
 * "Switch project": the project selector over the page (⌘K's Switch
 * project…, the Switch Project shortcut, the header). Picking a project
 * makes it the one the chats show — the sidebar's project filter — and opens
 * its chats; an app with no code here opens its console.
 */

import { useNavigate } from "@tanstack/react-router";

import { useWorkspaceStateStore } from "@/store/workspaceStateStore";
import { leaveChatPageFor, useProjectStore } from "@/store/projectStore";
import { appPath, projectChatsPath } from "@/lib/paths";

import { ProjectSelectorDialog, type ProjectSelection } from "./ProjectSelector";

export function SwitchProjectDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const navigate = useNavigate();
  const filterId = useWorkspaceStateStore((s) => s.projectFilterId);
  const currentId = useProjectStore((s) => s.currentProject?.id ?? null);
  const onSelect = (selection: ProjectSelection) => {
    if (selection.kind === "app") {
      void navigate({ to: appPath(selection.app.name) });
      return;
    }
    const id = selection.row.key;
    // From a chat's page, leave it first: the URL's chat wins over a selection.
    leaveChatPageFor(id);
    void useProjectStore
      .getState()
      .setProjectFilter(id)
      .then(() => navigate({ to: projectChatsPath(id) }));
  };
  return <ProjectSelectorDialog open={open} onClose={onClose} mode="navigate" selectedKey={filterId ?? currentId} onSelect={onSelect} label="Switch project" />;
}
