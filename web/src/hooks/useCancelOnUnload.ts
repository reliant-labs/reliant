import { useEffect } from "react";
import { useChatStore } from "../store/chatStore";
import { useIsChatRunning } from "../store/activityStore";

// Best-effort cancel when the browser tab/window is closed or navigated away
export function useCancelOnUnload() {
  const activeChatId = useChatStore((state) => state.activeChatId);
  const chatId = activeChatId;
  const isChatBusy = useIsChatRunning(chatId || "");

  useEffect(() => {
    const handler = () => {
      // Only act if there's active work
      if (!isChatBusy || !chatId) return;

      try {
        // Determine base URL - use grpcUrl
        let base = "";
        if (window.RELIANT_CONFIG?.grpcUrl) {
          base = window.RELIANT_CONFIG.grpcUrl;
        } else {
          base = `${window.location.protocol}//${window.location.host}`;
        }

        const url = `${base}/reliant.v1.ChatService/CancelChat`;

        const body = JSON.stringify({ chatId: chatId });
        const payload = new Blob([body], { type: "application/json" });

        if (navigator.sendBeacon) {
          navigator.sendBeacon(url, payload);
        } else {
          fetch(url, {
            method: "POST",
            body,
            headers: {
              "Content-Type": "application/json",
              "Connect-Protocol-Version": "1",
            },
            keepalive: true,
          }).catch(() => {});
        }
      } catch {
        // Ignore errors - unload context is fragile
      }
    };

    window.addEventListener("beforeunload", handler);
    window.addEventListener("pagehide", handler as EventListener);
    return () => {
      window.removeEventListener("beforeunload", handler);
      window.removeEventListener("pagehide", handler as EventListener);
    };
  }, [chatId, isChatBusy]);
}
