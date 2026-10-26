// Copyright (c) 2025 Reliant Labs

/** The way into the app-building chat from a page outside the chat shell (appStart.ts). */

import { useCallback } from "react";
import { useNavigate } from "@tanstack/react-router";

import { getCachedProjects } from "@/hooks/project-queries";
import { useChatStore } from "@/store/chatStore";
import { useProjectStore } from "@/store/projectStore";
import { useWorktreeStore } from "@/store/worktreeStore";

import { START_APP } from "./appStart";

/** Open a new chat in the current project (or the first one) with the app-building composer. */
export function useStartBuildingApp(): () => void {
  const navigate = useNavigate();
  return useCallback(() => {
    const project = useProjectStore.getState().currentProject ?? getCachedProjects()[0] ?? null;
    // A new chat, not whichever chat the project last had open.
    useChatStore.getState().clearCurrentChat(useWorktreeStore.getState().currentWorktreeId);
    if (project) {
      void navigate({ to: "/project/$projectId", params: { projectId: project.id }, search: { start: START_APP } });
    } else {
      void navigate({ to: "/", search: { start: START_APP } });
    }
  }, [navigate]);
}
