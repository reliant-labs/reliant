/**
 * `/m/settings` → "Workspace preferences" section.
 *
 * Renders `MobileWorkspacePreferencesPanel`, a touch-native rebuild of the
 * desktop `WorktreeSettings` panel. Both read and write the same
 * `usePreferences` / `useUpdatePreferences` / `useUpdateWorktreePreferences`
 * hooks — only the radio-card grid is replaced with a vertical list.
 */

import { MobileWorkspacePreferencesPanel } from "./MobileWorkspacePreferencesPanel";
import { MobileSettingsSectionHeader } from "./MobileSettingsSectionHeader";

export function MobileWorkspacePreferencesScreen({
  onBack,
}: {
  onBack: () => void;
}) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileSettingsSectionHeader title="Workspace preferences" onBack={onBack} />
      <div className="min-h-0 flex-1 overflow-y-auto">
        <MobileWorkspacePreferencesPanel />
      </div>
    </div>
  );
}
