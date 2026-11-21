import { useCallback } from "react";
import { loadRunContext } from "./loadRunContext";
import { toast } from "@/lib/toast-manager";
import { navigateToChatUrl } from "@/lib/chatRoute";

/**
 * Open another run (chat) by id from anywhere in the app: select its project
 * and workspace, then go to the chat's own URL (`/c/<id>`). A failure is a
 * toast, never a throw: the caller is a click handler.
 */
export function useOpenRun(): (chatId: string) => Promise<void> {
  return useCallback(async (chatId: string) => {
    try {
      const chat = await loadRunContext(chatId);
      navigateToChatUrl(chat.id, { orgId: chat.orgId });
    } catch (error) {
      void toast.error(error);
    }
  }, []);
}
