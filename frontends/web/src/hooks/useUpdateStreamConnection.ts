/**
 * The live update stream belongs to the app shell, not to any one screen.
 *
 * One connection carries everything live: chat list changes, activity, daemon
 * heartbeats, and — for the one chat a screen subscribes to
 * (reconcileChatSubscription) — that chat's messages and node events. Screens
 * only say WHICH chat; whether the stream is up at all is decided here, once
 * per shell (ModernApp on desktop, MobileShell on `/m/*`), so every screen
 * under it is live, including one opened directly by URL.
 *
 * `ready` is the shell's word that the chat list has loaded: that is what
 * sets the sequence the stream resumes from, and globalUpdatesStore's
 * connect() defers until it has. A screen that subscribed before then rides
 * this connection — connect() forwards the subscribed chat into the stream's
 * first request. The stream spans every project, so switching projects never
 * reconnects it; it disconnects when the shell unmounts.
 */

import { useEffect } from "react";
import { useGlobalUpdatesStore } from "../store/globalUpdatesStore";

export function useUpdateStreamConnection(ready: boolean): void {
  useEffect(() => {
    if (!ready) return;
    useGlobalUpdatesStore.getState().connect();
    return () => {
      useGlobalUpdatesStore.getState().disconnect();
    };
  }, [ready]);
}
