/**
 * `/m/settings` → "AI providers" section.
 *
 * Thin host for `MobileAIProvidersPanel` — the mobile-native rebuild that
 * replaced this screen's previous wholesale import of desktop
 * `CombinedGeneralSettings` (a 24.3×24px streaming toggle, a 308×40px
 * provider `<select>`, both under the 44px floor). See that panel's module
 * comment for the OAuth/Claude/Codex rationale and what stays shared with
 * desktop vs. what's mobile-only presentation.
 */

import { useCallback, useEffect, useState } from "react";
import { api } from "../../api/client";
import { MobileAIProvidersPanel } from "./MobileAIProvidersPanel";
import { MobileSettingsSectionHeader } from "./MobileSettingsSectionHeader";

interface ProviderStatus {
  provider: string;
  configured: boolean;
  hasApiKey: boolean;
  maskedKey?: string;
  displayName: string;
}

export function MobileAISettingsScreen({ onBack }: { onBack: () => void }) {
  const [providers, setProviders] = useState<ProviderStatus[]>([]);

  const fetchProviderStatuses = useCallback(async () => {
    try {
      const data = await api.settings.getProviders();
      setProviders(data || []);
    } catch (error) {
      console.error("Failed to fetch provider statuses:", error);
      setProviders([]);
    }
  }, []);

  useEffect(() => {
    void fetchProviderStatuses();
  }, [fetchProviderStatuses]);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileSettingsSectionHeader title="AI providers" onBack={onBack} />
      <MobileAIProvidersPanel
        providers={providers}
        onProvidersUpdate={fetchProviderStatuses}
      />
    </div>
  );
}
