import { useCallback } from "react";
import { useRouter } from "@tanstack/react-router";
import { loadRunContext } from "./loadRunContext";
import { toast } from "@/lib/toast-manager";

/**
 * Open another run (chat) by id from anywhere in the app: select its project
 * and workspace, then go to the project's chat screen. A failure is a toast,
 * never a throw: the caller is a click handler.
 */
export function useOpenRun(): (chatId: string) => Promise<void> {
  const router = useRouter({ warn: false });
  return useCallback(
    async (chatId: string) => {
      try {
        const chat = await loadRunContext(chatId);
        await router?.navigate({ to: "/project/$projectId", params: { projectId: chat.projectId }, search: {} });
      } catch (error) {
        void toast.error(error);
      }
    },
    [router]
  );
}
