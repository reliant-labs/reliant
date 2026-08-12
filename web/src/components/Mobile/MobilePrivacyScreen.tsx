/**
 * `/m/settings` → "Privacy" section.
 *
 * Renders `MobilePrivacyPanel`, a touch-native rebuild of the desktop
 * `PrivacySettings` panel. Both read and write the same `usePrivacyStore`
 * — only the row/toggle presentation differs.
 */

import { MobilePrivacyPanel } from "./MobilePrivacyPanel";
import { MobileSettingsSectionHeader } from "./MobileSettingsSectionHeader";

export function MobilePrivacyScreen({ onBack }: { onBack: () => void }) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileSettingsSectionHeader title="Privacy" onBack={onBack} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <MobilePrivacyPanel />
      </div>
    </div>
  );
}
