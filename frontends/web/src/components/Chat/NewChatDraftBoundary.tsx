import { useLayoutEffect, useState, type ReactNode } from "react";
import { useChatParamsStore } from "../../store/chatParamsStore";

/** The legacy temp configuration slot has one owner. Never import another
 * project's choices into a newly mounted composer. Attachments/text already
 * have project-scoped stores and are deliberately not cleared here. */
let configurationProject: string | undefined;
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
