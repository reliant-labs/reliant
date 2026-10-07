import { lazy, Suspense, useState } from "react";
import { cn } from "../../lib/utils";
import { SettingsPageHeader } from "./SettingsPageHeader";
import { CombinedGeneralSettings } from "./CombinedGeneralSettings";
import { ToolPreferences } from "./ToolPreferences";
import { ActionApprovalSettings } from "./ActionApprovalSettings";
import { LocalModelsSection } from "./LocalModelsSection";
import { reliantAIAvailable } from "../../services/controlPlane/reliantAI";

// ReliantAISection is code-split out of the main settings chunk — it's only
// fetched when the user opens the "Reliant AI" tab. The named export is adapted
// to the default export React.lazy expects.
const ReliantAISection = lazy(() =>
  import("./cloud/reliantAI").then((m) => ({ default: m.ReliantAISection }))
);

interface AISettingsProps {
  providers: Array<{
    provider: string;
    displayName: string;
    hasApiKey: boolean;
    maskedKey?: string;
    configured: boolean;
  }>;
  onProvidersUpdate?: () => void;
}

type AITab = "providers" | "local" | "tools" | "reliant";

/**
 * Single "AI" settings section with internal tabs:
 *   - "Your providers" → bring-your-own provider keys ({@link CombinedGeneralSettings}).
 *   - "Tools"          → global bindings for tools that resolve a model on the
 *                        user's behalf ({@link ToolPreferences}), and the
 *                        integration actions you always allow
 *                        ({@link ActionApprovalSettings}).
 *   - "Reliant AI"     → Reliant-managed keys/credits/spend (ReliantAISection).
 *
 * The Reliant AI tab only renders when the managed-AI surface is wired up
 * (`reliantAIAvailable`); self-host / non-cloud users see the other two.
 */
export function AISettings({ providers, onProvidersUpdate }: AISettingsProps) {
  const [tab, setTab] = useState<AITab>("providers");

  // The providers tab preserves the old settings-card look: a narrow card with
  // injected heading styles so CombinedGeneralSettings' h2/h3 render as they did
  // when it lived inside SettingsContent's generic card.
  const providersContent = (
    <div className="mx-auto max-w-[700px] rounded-xl border border-border/50 bg-card p-6 shadow-sm [&_h3]:text-sm [&_h3]:font-semibold [&_h3]:text-foreground">
      <CombinedGeneralSettings
        providers={providers}
        onProvidersUpdate={onProvidersUpdate}
        onOpenReliantAI={
          reliantAIAvailable ? () => setTab("reliant") : undefined
        }
      />
    </div>
  );

  // Global tool preferences. Deliberately NOT part of Model Preferences: that
  // panel picks the model the CONVERSATION runs on and its rows are quality
  // tiers, whereas these settings configure tools that resolve a model on the
  // user's behalf and are never chosen by the agent. Same card treatment as
  // the providers tab so the two read as siblings.
  const toolsContent = (
    <div className="mx-auto max-w-[700px] space-y-8 rounded-xl border border-border/50 bg-card p-6 shadow-sm">
      <ToolPreferences providers={providers} />
      <div className="border-t border-border/60 pt-6">
        <ActionApprovalSettings />
      </div>
    </div>
  );

  const tabs: Array<{ id: AITab; label: string }> = [
    { id: "providers", label: "Your providers" },
    { id: "local", label: "Custom & local models" },
    { id: "tools", label: "Tools" },
    ...(reliantAIAvailable
      ? [{ id: "reliant" as const, label: "Reliant AI" }]
      : []),
  ];

  return (
    <>
      {tab !== "reliant" && (
      <SettingsPageHeader
        title="AI"
        description="Choose which AI models your agents can use. Add your own provider keys, set the models that tools use behind the scenes, or manage Reliant's managed AI credits and spend."
        className="mb-4 border-b-0 pb-0"
      />
      )}
      <div className="mb-6 flex gap-1 border-b border-border">
        {tabs.map((t) => (
          <button
            key={t.id}
            type="button"
            onClick={() => setTab(t.id)}
            className={cn(
              "-mb-px border-b-2 px-3 py-2 text-sm font-medium transition-colors",
              tab === t.id
                ? "border-primary text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground"
            )}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === "providers" ? (
        providersContent
      ) : tab === "local" ? (
        <div className="mx-auto max-w-[700px]">
          <LocalModelsSection />
        </div>
      ) : tab === "tools" ? (
        toolsContent
      ) : (
        // ReliantAISection is styled to expect the `.cloud-settings` scope
        // (Inter + admin-like density), mirroring how the cloud sections render
        // in SettingsContent.
        <div className="cloud-settings">
          <Suspense
            fallback={
              <div className="flex h-full items-center justify-center">
                <div className="rounded-lg border border-border/50 bg-card px-4 py-3 text-sm text-muted-foreground">
                  Loading…
                </div>
              </div>
            }
          >
            <ReliantAISection />
          </Suspense>
        </div>
      )}
    </>
  );
}
