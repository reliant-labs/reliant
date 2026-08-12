/**
 * `/m/settings` → "Appearance" section.
 *
 * Renders `MobileAppearancePanel`, a touch-native rebuild of the desktop
 * `AppearanceSettings` panel — same `settingsSync` keys, same
 * `ColorSchemeSelector`, same `useUIStore` hidden-files flag. It never
 * imports Monaco or `MonacoEditorSettings`/`LanguageServerSettingsCompact`
 * (the desktop panel's collapsed-by-default "Editor (advanced)" section),
 * matching the `/m/*` bundle's exclusion of the eager Monaco preload (see
 * `main.tsx`'s `shouldPreloadMonaco`).
 */

import { MobileAppearancePanel } from "./MobileAppearancePanel";
import { MobileSettingsSectionHeader } from "./MobileSettingsSectionHeader";

export function MobileAppearanceScreen({ onBack }: { onBack: () => void }) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileSettingsSectionHeader title="Appearance" onBack={onBack} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <MobileAppearancePanel />
      </div>
    </div>
  );
}
