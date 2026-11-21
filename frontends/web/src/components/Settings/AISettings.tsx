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

/** Models & tools' tabs; Personal accounts (the providers tab) is its own page. */
const MODEL_TABS: ReadonlyArray<SettingsTabItem<Exclude<AISettingsTab, "providers">>> = [
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
 * Settings → AI: the one page behind two sidebar entries in the AI group
 * (Organization accounts, between them, is its own section):
 *   - Personal accounts  the accounts your agents run on that you connect
 *                        yourself ({@link ProvidersSection}), with the page's
 *                        one "Add provider" action, and how the active org's
 *                        accounts and yours are ordered ({@link OrgFundingSection});
 *   - Models & tools     which model each tier uses, and which provider a tool
 *                        that picks a model for you (image generation) uses,
 *                        in a Models tab; models on your machines and custom
 *                        endpoints in a Local & custom tab.
 *
 * Which is shown is the URL's `?tab=` (providers | models | local), unchanged
 * from when they were three tabs of one page, so every link to them still
 * lands where it did and survives a refresh.
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

  if (tab === "providers") {
    return (
      <>
        <SettingsPageHeader
          title="Personal accounts"
          description="The AI accounts you connect yourself. Your agents run on them, and on any your organization gives you."
          actions={
            <Button variant="primary" size="lg" leftIcon={<Plus />} aria-expanded={pickerOpen} onClick={() => setPickerOpen(true)}>
              Add provider
            </Button>
          }
        />
        <div className="space-y-4" data-onboarding="ai-providers-settings">
          <OrgFundingSection />
          <ProvidersSection pickerOpen={pickerOpen} onPickerOpenChange={setPickerOpen} />
        </div>
      </>
    );
  }

  return (
    <>
      <SettingsPageHeader
        title="Models & tools"
        description="Choose which models your agents and tools use, and add models on your machines or custom endpoints."
        className="mb-4 border-b-0 pb-0"
      />
      <SettingsTabs tabs={MODEL_TABS} value={tab} onChange={setTab} idPrefix="ai-settings" ariaLabel="Models & tools" className="mb-6" />

      <SettingsTabPanel idPrefix="ai-settings" tab={tab}>
        {tab === "models" ? (
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
