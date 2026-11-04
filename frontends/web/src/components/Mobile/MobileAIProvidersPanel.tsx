/**
 * Mobile "AI providers" panel.
 *
 * The providers themselves — the cards, the "Add provider" picker, the scoped
 * "Add another Claude account" panels and the empty state — are desktop's
 * `ProvidersSection` in its `mobile` variant (touch-sized controls, no drag
 * handle). It used to fork its own add sheet, key form and OAuth wiring, a
 * second copy of every add path that drifted from the first; now there is one
 * add path on every surface.
 *
 * What stays mobile-only: the notice that browser sign-ins need
 * `reliant auth serve` on a computer, the streaming preference (mobile has no
 * Appearance page), and `MobileModelPreferences`, which keeps only the model
 * choice of desktop's per-tier tuning.
 */

import { useMemo, useState } from "react";
import { Info, Plus } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { usePreferences, useProviderStatuses, useUpdatePreferences } from "../../hooks/settings-queries";
import { ProvidersSection } from "../Settings/providers/ProvidersSection";
import { MobileModelPreferences } from "./MobileModelPreferences";
import { MobileCardGroup } from "./MobileChrome";
import { MobileToggleRow } from "./MobileSettingsRow";

export function MobileAIProvidersPanel() {
  const [pickerOpen, setPickerOpen] = useState(false);
  const { data: userPreferences, isPending: loadingPreferences } = usePreferences();
  const streamingEnabled = userPreferences.streamingEnabled;
  const updatePreferences = useUpdatePreferences();
  const { data: statuses } = useProviderStatuses();
  const providers = useMemo(
    () => (statuses?.providers ?? []).map((p) => ({ provider: p.provider, hasApiKey: p.accountCount > 0 })),
    [statuses],
  );

  return (
    <div className="min-h-0 flex-1 overflow-y-auto py-4">
      {/* A static fact rather than a live probe: `useOAuthAvailability` is
          never called just to decide whether to SHOW this text. */}
      <div className="mx-4 mb-4 flex items-start gap-2 rounded-lg border border-warning/30 bg-warning/10 p-3 text-xs text-warning-ink">
        <Info className="mt-0.5 h-4 w-4 shrink-0" />
        <p>
          Claude, ChatGPT and Google sign-in needs <code>reliant auth serve</code> running on a computer — a phone
          can&apos;t start it. Connect those from desktop, or use GitHub Copilot, which signs in entirely on this
          device.
        </p>
      </div>

      <div className="px-4">
        <div className="mb-3 flex justify-end">
          <Button
            variant="primary"
            size="xl"
            leftIcon={<Plus />}
            aria-expanded={pickerOpen}
            onClick={() => setPickerOpen(true)}
          >
            Add provider
          </Button>
        </div>
        <ProvidersSection variant="mobile" pickerOpen={pickerOpen} onPickerOpenChange={setPickerOpen} />
      </div>

      <div className="mt-4 px-4">
        <MobileCardGroup label="Chat preferences">
          <MobileToggleRow
            label="Response streaming"
            description="Show replies as they are written. Off, each arrives all at once when it is done."
            checked={streamingEnabled}
            disabled={loadingPreferences}
            onChange={(checked) => void updatePreferences.mutateAsync({ streamingEnabled: checked }).catch(() => {})}
          />
        </MobileCardGroup>
      </div>

      <div className="mt-4 px-4">
        <MobileCardGroup label="Default models">
          <div className="p-4">
            <p className="mb-3 text-xs text-muted-foreground">
              Which model each tier uses by default. For thinking level, temperature, and compaction overrides, use
              desktop.
            </p>
            <MobileModelPreferences providers={providers} />
          </div>
        </MobileCardGroup>
      </div>
    </div>
  );
}
