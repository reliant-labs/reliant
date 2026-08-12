/**
 * `/m/settings` → "Notifications" section.
 *
 * Renders `MobileNotificationsPanel`, a touch-native rebuild of the desktop
 * `NotificationSettings` panel. Both read and write the same
 * `useNotificationStore` — only the row/toggle presentation differs.
 */

import { MobileNotificationsPanel } from "./MobileNotificationsPanel";
import { MobileSettingsSectionHeader } from "./MobileSettingsSectionHeader";

export function MobileNotificationsScreen({ onBack }: { onBack: () => void }) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileSettingsSectionHeader title="Notifications" onBack={onBack} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <MobileNotificationsPanel />
      </div>
    </div>
  );
}
