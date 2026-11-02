import { useLayoutEffect, useState, type ReactNode } from "react";
import { useChatParamsStore } from "../../store/chatParamsStore";
import { useProjectStore } from "../../store/projectStore";

/** The legacy temp configuration slot has one owner. Never import another
 * project's choices into a newly mounted composer. Attachments/text already
 * have project-scoped stores and are deliberately not cleared here.
 *
 * The owner is the project current at the slot's last write, whichever
 * composer host made it (a desktop ChatInput writes the same setters), so a
 * write from project B is never mistaken for project A's after a mobile A
 * composer mounted. undefined means unattributed: the next mount clears it. */
let configurationProject: string | undefined;
useChatParamsStore.subscribe((state, prev) => {
  if (
    state.tempNewChatParams !== prev.tempNewChatParams ||
    state.tempNewChatWorkflow !== prev.tempNewChatWorkflow ||
    state.tempNewChatPresets !== prev.tempNewChatPresets
  ) {
    configurationProject = useProjectStore.getState().currentProject?.id;
  }
});

export function NewChatDraftBoundary({ projectId, children }: { projectId: string; children: ReactNode }) {
  const [ready, setReady] = useState(false);
  useLayoutEffect(() => {
    if (configurationProject !== projectId) {
      useChatParamsStore.getState().clearTempNewChatParams();
      configurationProject = projectId;
    }
    setReady(true);
  }, [projectId]);
  return ready ? children : null;
}

export const newChatAttachmentDraftId = (projectId: string) => `new-chat:project:${projectId}`;
