import { useCallback } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useProjectStore } from "@/store/projectStore";
import { useChatStore } from "@/store/chatStore";
import { useViewerStore } from "@/store/viewerStore";

/**
 * Go to the project picker — the one place where projects are added.
 *
 * Deselecting the current project IS how the picker is reached (ModernApp
 * renders it whenever `currentProject` is null), and getting there requires
 * three stores to be cleared in order, not just a route change. That sequence
 * lived only inside ModernApp, so every other surface that wanted to offer
 * "add a project" either could not, or would have had to copy it — and a
 * partial copy leaves a stale chat or a stale viewer pointing at a project
 * that is no longer selected.
 *
 * Extracted so Settings → Projects can offer the same entry point as the
 * picker itself, per the add-project design: the picker is the primary place,
 * and Settings is the second one.
 */
export function useNavigateToProjectPicker(): () => void {
  const navigate = useNavigate();

  return useCallback(() => {
    useChatStore.getState().clearCurrentChat(null);
    // Viewer state must be saved and cleared BEFORE the project is
    // deselected, or the viewers persist against a project nothing points to.
    useViewerStore.getState().clearViewersForProjectSwitch();
    // selectProject drives the URL on the way in; deselecting sets state and
    // navigates by hand.
    useProjectStore.setState({ currentProject: null });
    navigate({ to: "/", search: {} });
  }, [navigate]);
}
