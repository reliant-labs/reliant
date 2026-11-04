/**
 * `/m/settings` → "AI providers" section: a header over `MobileAIProvidersPanel`,
 * which renders the same providers surface desktop does in its mobile variant.
 */

import { MobileAIProvidersPanel } from "./MobileAIProvidersPanel";
import { MobileSettingsSectionHeader } from "./MobileSettingsSectionHeader";

export function MobileAISettingsScreen({ onBack }: { onBack: () => void }) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileSettingsSectionHeader title="AI providers" onBack={onBack} />
      <MobileAIProvidersPanel />
    </div>
  );
}
