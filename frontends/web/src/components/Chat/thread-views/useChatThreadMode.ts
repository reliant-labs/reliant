import { useEffect, useState } from "react";
import { settingsSync, SETTINGS_KEYS } from "../../../services/settingsSync";
import { useSurface } from "../../../lib/surfaceContext";
import type { Surface } from "../../../lib/surface";
import { useMediaQuery } from "../../../hooks/useMediaQuery";

export type ChatThreadModePreference = "auto" | "simplified" | "detailed";
export type ChatThreadMode = "simplified" | "detailed";

/**
 * What a user who has never chosen gets: the simplified thread, everywhere.
 * Detailed and Auto (simplified only on phones and narrow windows) stay one
 * setting away.
 */
export const DEFAULT_CHAT_THREAD_MODE: ChatThreadModePreference = "simplified";

export const NARROW_THREAD_QUERY = "(max-width: 1023px)";

export const CHAT_THREAD_MODE_CHOICES: readonly {
  value: ChatThreadModePreference;
  label: string;
  description: string;
}[] = [
  { value: "simplified", label: "Simplified", description: "Collapse each turn's tool work into one fold" },
  { value: "auto", label: "Auto", description: "Simplified on phones and narrow windows" },
  { value: "detailed", label: "Detailed", description: "Show every message and tool call" },
];

export function parseChatThreadMode(value: unknown): ChatThreadModePreference {
  return value === "simplified" || value === "detailed" || value === "auto"
    ? value
    : DEFAULT_CHAT_THREAD_MODE;
}

export function getStoredChatThreadMode(): ChatThreadModePreference {
  return parseChatThreadMode(settingsSync.getSetting(SETTINGS_KEYS.CHAT_THREAD_MODE, DEFAULT_CHAT_THREAD_MODE));
}

export function resolveChatThreadMode(
  preference: ChatThreadModePreference,
  env: { surface: Surface; narrowViewport: boolean },
): ChatThreadMode {
  if (preference !== "auto") return preference;
  return env.surface !== "desktop" || env.narrowViewport ? "simplified" : "detailed";
}

export function setChatThreadMode(preference: ChatThreadModePreference): void {
  void settingsSync.setSettingIfChanged(SETTINGS_KEYS.CHAT_THREAD_MODE, preference).catch(console.error);
  window.dispatchEvent(new CustomEvent("appearance-updated"));
}

export function useChatThreadMode(): { mode: ChatThreadMode; preference: ChatThreadModePreference } {
  const surface = useSurface();
  const narrowViewport = useMediaQuery(NARROW_THREAD_QUERY);
  const [preference, setPreference] = useState<ChatThreadModePreference>(() => getStoredChatThreadMode());

  useEffect(() => {
    const handle = () => setPreference(getStoredChatThreadMode());
    window.addEventListener("appearance-updated", handle);
    return () => window.removeEventListener("appearance-updated", handle);
  }, []);

  return { mode: resolveChatThreadMode(preference, { surface, narrowViewport }), preference };
}
