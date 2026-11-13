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
import { CommandBlock } from "@/components/CommandBlock";
import { SetupHelp } from "@/components/SetupHelp";
import { authServeCommand } from "@/lib/cli-commands";
import { usePreferences, useProviderStatuses, useUpdatePreferences } from "../../hooks/settings-queries";
import { ProvidersSection } from "../Settings/providers/ProvidersSection";
import { OrgFundingSection } from "../Settings/providers/OrgFundingSection";
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
        <div className="min-w-0 flex-1 space-y-2">
          <p>Claude, ChatGPT or Google? Run this on your computer, then sign in there.</p>
          <CommandBlock command={authServeCommand()} label="sign-in helper command" />
          <SetupHelp>
            <p>A phone cannot run the sign-in helper. Copy the command to your computer and keep its terminal open. GitHub Copilot can sign in directly on this phone.</p>
            <p>Need the CLI? Open Reliant on your computer; the desktop installer includes it.</p>
          </SetupHelp>
        </div>
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
        <div className="space-y-4">
          <OrgFundingSection variant="mobile" />
          <ProvidersSection variant="mobile" pickerOpen={pickerOpen} onPickerOpenChange={setPickerOpen} />
        </div>
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
