import { useEffect } from "react";
import { useChatStore } from "../store/chatStore";
import { useProjectStore } from "../store/projectStore";
import { useIsChatRunning } from "../store/activityStore";

// Best-effort cancel when the browser tab/window is closed or navigated away
export function useCancelOnUnload() {
  const activeChatId = useChatStore((state) => state.activeChatId);
  const currentProject = useProjectStore((state) => state.currentProject);

  const chatId = activeChatId;
  const currentChat = useChatStore((state) => chatId ? state.chats.get(chatId) || null : null);
  const isChatBusy = useIsChatRunning(chatId || "");

  useEffect(() => {
    const handler = () => {
      // Only act if there's active work
      if (!isChatBusy) return;

      // Try to best-effort cancel via Beacon/keepalive fetch
      try {
        const projectId = currentProject?.id;
        const chatId = currentChat?.id;
        if (!projectId || !chatId) return;

        // Determine base URL
        let base = "";
        if (
          typeof window !== "undefined" &&
          (window as unknown as { RELIANT_CONFIG?: { backendUrl?: string } }).RELIANT_CONFIG?.backendUrl
        ) {
          base = (window as unknown as { RELIANT_CONFIG: { backendUrl: string } }).RELIANT_CONFIG.backendUrl;
        } else {
          base = `${window.location.protocol}//${window.location.host}`;
        }

        const url = `${base}/api/v2/chats/${encodeURIComponent(chatId)}/cancel`;

        const body = { reason: "tab_unload" };
        const payload = new Blob([JSON.stringify(body)], {
          type: "application/json",
        });

        // Include auth token when available (for fetch fallback)
        const token = localStorage.getItem("auth_token");
        const headers: Record<string, string> = { "Content-Type": "application/json" };
        if (token) headers["Authorization"] = `Bearer ${token}`;

        if (navigator.sendBeacon) {
          navigator.sendBeacon(url, payload);
        } else {
          // Fallback with keepalive fetch (not guaranteed but better than nothing)
          fetch(url, {
            method: "POST",
            body: JSON.stringify(body),
            headers,
            keepalive: true,
            credentials: "include",
          }).catch(() => {});
        }
      } catch {
        // Ignore errors - unload context is fragile
      }

      // Optionally show native confirmation prompt to prevent accidental loss
      // e.preventDefault();
      // e.returnValue = "A session is still running. Are you sure you want to leave?";
    };

    window.addEventListener("beforeunload", handler);
    window.addEventListener("pagehide", handler as EventListener);
    return () => {
      window.removeEventListener("beforeunload", handler);
      window.removeEventListener("pagehide", handler as EventListener);
    };
  }, [chatId, currentChat?.id, currentProject?.id, isChatBusy]);
}
