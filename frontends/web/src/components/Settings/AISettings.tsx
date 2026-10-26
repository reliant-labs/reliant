import { useMemo, useState } from "react";
import { Plus } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { cn } from "../../lib/utils";
import { useProviderStatuses } from "../../hooks/settings-queries";
import { SettingsPageHeader } from "./SettingsPageHeader";
import { ModelPreferences } from "./ModelPreferences";
import { ToolPreferences } from "./ToolPreferences";
import { LocalModelsSection } from "./LocalModelsSection";
import { ProvidersSection } from "./providers/ProvidersSection";

type AITab = "providers" | "models" | "local";

const TABS: Array<{ id: AITab; label: string }> = [
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
 * Reliant's managed keys and spend live in Billing ("Reliant AI"), linked from
 * the Reliant provider card. Response streaming is a chat preference and lives
 * in Appearance.
 */
export function AISettings() {
  const [tab, setTab] = useState<AITab>("providers");
  const [pickerOpen, setPickerOpen] = useState(false);
  const providers = useProviderRows();

  return (
    <>
      <SettingsPageHeader
        title="AI"
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
      <div role="tablist" aria-label="AI settings" className="mb-6 flex gap-1 border-b border-border">
        {TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={tab === t.id}
            onClick={() => setTab(t.id)}
            className={cn(
              "-mb-px border-b-2 px-3 py-2 text-sm font-medium transition-colors",
              tab === t.id
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {t.label}
          </button>
        ))}
      </div>

      <div role="tabpanel" aria-label={TABS.find((t) => t.id === tab)?.label} data-onboarding="ai-providers-settings">
        {tab === "providers" ? (
          <ProvidersSection pickerOpen={pickerOpen} onPickerOpenChange={setPickerOpen} />
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
      </div>
    </>
  );
}
