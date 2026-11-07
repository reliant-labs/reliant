import { DEFAULT_LONG_PASTE_PREFERENCES, useLongPastePreferencesQuery, type LongPastePreferences } from "./settings-queries";

/** Account-wide composer preferences, shared with Settings → Chat & editor. */
export function useLongPastePreferences(): LongPastePreferences {
  return useLongPastePreferencesQuery().data ?? DEFAULT_LONG_PASTE_PREFERENCES;
}
