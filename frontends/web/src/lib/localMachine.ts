// Copyright (c) 2025 Reliant Labs

/**
 * Which of the user's machines is THIS computer — the one whose disk the
 * desktop app's native folder dialog shows.
 *
 * The registry does not say: a self-hosted daemon on this laptop and one on
 * another laptop look alike in ListDaemons. The only reliable source is the
 * desktop app itself, which runs this computer's daemon and can read its id
 * (`electronAPI.getLocalDaemonId`). Without that answer — the browser, or a
 * desktop build that does not expose it — no machine is "this computer", so
 * callers browse the chosen machine in the app instead of opening a native
 * dialog that would hand back a path from the wrong disk.
 */

import { useEffect, useState } from "react";

/** Whether the native folder dialog exists at all (the desktop app). */
export function hasNativeFolderDialog(): boolean {
  return typeof window !== "undefined" && !!window.electronAPI?.selectDirectory;
}

/** The daemon id of this computer's machine, or null when it cannot be known. */
export async function fetchLocalDaemonId(): Promise<string | null> {
  const api = typeof window !== "undefined" ? window.electronAPI : undefined;
  if (!api?.getLocalDaemonId) return null;
  try {
    return (await api.getLocalDaemonId()) || null;
  } catch {
    return null;
  }
}

/**
 * The daemon id of this computer's machine, once the desktop app has said;
 * null in the browser, while asking, and when it cannot say.
 */
export function useLocalDaemonId(): string | null {
  const [id, setId] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    void fetchLocalDaemonId().then((next) => {
      if (live) setId(next);
    });
    return () => {
      live = false;
    };
  }, []);
  return id;
}

/**
 * Whether a folder on `daemonId` can be chosen with the native dialog: only
 * when that machine is known to be this computer.
 */
export function canUseNativeFolderDialog(daemonId: string | null | undefined, localDaemonId: string | null): boolean {
  return hasNativeFolderDialog() && !!daemonId && !!localDaemonId && daemonId === localDaemonId;
}
