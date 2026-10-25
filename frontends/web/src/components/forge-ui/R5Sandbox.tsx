/**
 * The R5 overlay primitives in ForgeTokenSandbox (/forge-token-sandbox):
 * Tooltip, rendered the way its call sites use it, OUTSIDE .forge-ui as they
 * sit in the app. e2e/color-schemes.spec.ts opens each one and screenshots
 * the region with its overlay; the baselines were captured on the pre-R5
 * components, so each R5 commit's image diff is exactly what it changed.
 *
 *   r5-tooltips  ?view=r5-tooltips — a toolbar of icon buttons, a disabled
 *                button that explains itself, a help icon in running text;
 *                the spec hovers the disabled button, then focuses the
 *                toolbar's first button from the keyboard
 */
import { Copy, Download, HelpCircle, RefreshCw } from "lucide-react";

import Tooltip from "./tooltip";

function SectionTitle({ children }: { children: React.ReactNode }) {
  return <h2 className="mb-3 text-sm font-medium text-foreground">{children}</h2>;
}

const iconButton =
  "inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground";

export function R5Tooltips() {
  return (
    <div className="space-y-6 p-16" data-testid="r5-tooltips">
      <SectionTitle>Tooltip — a toolbar, a disabled action, a help icon</SectionTitle>
      <div className="flex max-w-3xl items-start gap-16">
        <div className="flex items-center gap-1 rounded-lg border border-border bg-card p-1">
          <Tooltip content="Copy path" side="bottom" triggerClassName="inline-flex">
            <button type="button" className={iconButton} aria-label="Copy path">
              <Copy className="h-4 w-4" />
            </button>
          </Tooltip>
          <Tooltip content="Download" side="bottom" triggerClassName="inline-flex">
            <button type="button" className={iconButton} aria-label="Download">
              <Download className="h-4 w-4" />
            </button>
          </Tooltip>
          <Tooltip content="Refresh" side="bottom" triggerClassName="inline-flex">
            <button type="button" className={iconButton} aria-label="Refresh">
              <RefreshCw className="h-4 w-4" />
            </button>
          </Tooltip>
        </div>

        <Tooltip content="Save the workflow first: steps attach to a saved revision, not a draft." side="top">
          <button
            type="button"
            disabled
            className="rounded-md bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground opacity-50"
          >
            Add step
          </button>
        </Tooltip>

        <p className="flex items-center gap-1 text-sm text-muted-foreground">
          Entry steps run in parallel
          <Tooltip content="Every entry step starts when the workflow does." side="right" triggerClassName="inline-flex">
            <HelpCircle className="h-3.5 w-3.5" aria-label="About entry steps" />
          </Tooltip>
        </p>
      </div>
    </div>
  );
}
