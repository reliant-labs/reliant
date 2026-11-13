import { useMemo, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Plus } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { resolveAISettingsTab, type AISettingsTab, type SettingsTab } from "../../routeSchemas";
import { useProviderStatuses } from "../../hooks/settings-queries";
import { SettingsPageHeader } from "./SettingsPageHeader";
import { ModelPreferences } from "./ModelPreferences";
import { ToolPreferences } from "./ToolPreferences";
import { LocalModelsSection } from "./LocalModelsSection";
import { ProvidersSection } from "./providers/ProvidersSection";
import { OrgFundingSection } from "./providers/OrgFundingSection";
import { SettingsTabPanel, SettingsTabs, type SettingsTabItem } from "./SettingsTabs";

const TABS: ReadonlyArray<SettingsTabItem<AISettingsTab>> = [
  { id: "providers", label: "Providers" },
  { id: "models", label: "Models" },
  { id: "local", label: "Local & custom" },
];

/** The provider statuses ModelPreferences / ToolPreferences filter their choices by. */
function useProviderRows() {
  const { data } = useProviderStatuses();
  return useMemo(
    () =>
      (data?.providers ?? []).map((p) => ({
        provider: p.provider,
        displayName: p.displayName,
        configured: p.configured,
        hasApiKey: p.accountCount > 0,
      })),
    [data],
  );
}

/**
 * Settings → AI, in three tabs:
 *   - Providers       the accounts your agents run on ({@link ProvidersSection}),
 *                     with the page's one "Add provider" action in the header;
 *   - Models          which model each tier uses, and which provider a tool
 *                     that picks a model for you (image generation) uses —
 *                     both answer "which model does X use";
 *   - Local & custom  models on your machines, and custom endpoints.
 *
 * The tab is the URL's `?tab=` (providers | models | local), so "Default
 * models" can be linked to and survives a refresh.
 *
 * Reliant's managed keys and spend live in Billing ("Reliant AI"), linked from
 * the Reliant provider card. Response streaming is a chat preference and lives
 * in Chat & editor.
 */
export function AISettings() {
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { tab?: SettingsTab };
  const tab = resolveAISettingsTab(search.tab);
  const setTab = (next: AISettingsTab) => {
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({ ...prev, tab: next === "providers" ? undefined : next }),
      replace: true,
    });
  };
  const [pickerOpen, setPickerOpen] = useState(false);
  const providers = useProviderRows();

  return (
    <>
      <SettingsPageHeader
        title="AI providers"
        description="Connect the AI accounts your agents run on, and choose which models they use."
        actions={
          <Button
            variant="primary"
            size="lg"
            leftIcon={<Plus />}
            aria-expanded={tab === "providers" && pickerOpen}
            onClick={() => {
              setTab("providers");
              setPickerOpen(true);
            }}
          >
            Add provider
          </Button>
        }
        className="mb-4 border-b-0 pb-0"
      />
      <SettingsTabs tabs={TABS} value={tab} onChange={setTab} idPrefix="ai-settings" ariaLabel="AI settings" className="mb-6" />

      <SettingsTabPanel idPrefix="ai-settings" tab={tab} data-onboarding="ai-providers-settings">
        {tab === "providers" ? (
          <div className="space-y-4">
            <OrgFundingSection />
            <ProvidersSection pickerOpen={pickerOpen} onPickerOpenChange={setPickerOpen} />
          </div>
        ) : tab === "models" ? (
          <div className="space-y-4">
            <section aria-label="Default models" className="rounded-lg border border-border bg-card p-4">
              <h2 className="text-sm font-semibold text-foreground">Default models</h2>
              <div className="mt-1">
                <ModelPreferences providers={providers} />
              </div>
            </section>
            <section aria-label="Tool models" className="rounded-lg border border-border bg-card p-4">
              <ToolPreferences providers={providers} />
            </section>
          </div>
        ) : (
          <LocalModelsSection />
        )}
      </SettingsTabPanel>
    </>
  );
}
