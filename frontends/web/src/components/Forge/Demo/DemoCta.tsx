// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's calls to action: **Sign up** (the primary, large) and
 * **Install Forge** (secondary, opens InstallForgeSheet). They sit in the
 * banner, on the tour's end card, in every page card's footer and, once a
 * page's card is closed, in the corner beside "Show me around". Each click
 * records `demo_cta_clicked{cta, placement}`.
 *
 * Sign up goes to forge's sign-up (forge.reliantlabs.io, `forgeAppOrigin`)
 * with the landing page's UTMs (`utm_source=demo&utm_campaign=forge_demo&
 * utm_content=<placement>`) and `journey=forge`, so after sign-up the forge
 * journey starts. Through the web runtime's withAttribution it also carries
 * this visitor's id as `rid`, so the sign-up joins the demo funnel (nothing
 * is added when telemetry is not running: a browser that opted out).
 */

import { useState } from "react";
import { withAttribution } from "@reliantlabs/forge-web-runtime";
import { Download, UserPlus } from "lucide-react";

import { trackEvent } from "@/lib/analytics";
import { forgeAppOrigin, isDemoShell } from "@/lib/shell";
import { cn } from "@/lib/utils";

import { InstallForgeSheet } from "./InstallForgeSheet";

/** Where a CTA sits: the landing UTM's `utm_content`, and analytics' `placement`. */
export type DemoCtaPlacement = "banner" | "corner" | "end_card" | "page_card" | "toast" | "tour_intro" | "agent_sheet";

/** Where Sign up goes from `placement`. */
export function signupHref(placement: DemoCtaPlacement): string {
  const url = `${forgeAppOrigin()}/auth?mode=signup&journey=forge&utm_source=demo&utm_campaign=forge_demo&utm_content=${placement}`;
  try {
    return withAttribution(url);
  } catch {
    return url;
  }
}

/**
 * Where the public demo shows no CTA: its action row carries them, so a page
 * explanation or a mid-tour card never repeats them. The tour's end card does:
 * it is where the tour hands off.
 */
const OFF_ON_DEMO: readonly DemoCtaPlacement[] = ["page_card", "toast", "tour_intro", "corner"];

const tracked = (cta: "signup" | "install", placement: DemoCtaPlacement) => trackEvent("demo_cta_clicked", { cta, placement });

/** "Install Forge" as a text link (the tour's intro, the toast), opening the same sheet. */
export function InstallForgeLink({ placement, children = "Install Forge" }: { placement: DemoCtaPlacement; children?: React.ReactNode }) {
  const [installing, setInstalling] = useState(false);
  if (isDemoShell() && OFF_ON_DEMO.includes(placement)) return null;
  return (
    <>
      <button
        type="button"
        onClick={() => {
          tracked("install", placement);
          setInstalling(true);
        }}
        className="touch-target font-medium text-primary-ink underline-offset-2 hover:underline"
        data-testid={`install-forge-link-${placement}`}
      >
        {children}
      </button>
      <InstallForgeSheet open={installing} onClose={() => setInstalling(false)} />
    </>
  );
}

/** Sign up, as the page's primary button. */
export function SignUpButton({ placement, size = "lg", className }: { placement: DemoCtaPlacement; size?: "md" | "lg"; className?: string }) {
  if (isDemoShell() && OFF_ON_DEMO.includes(placement)) return null;
  return (
    <a
      href={signupHref(placement)}
      target="_blank"
      rel="noopener"
      onClick={() => tracked("signup", placement)}
      className={cn(
        "no-color inline-flex touch-target items-center justify-center gap-2 rounded-lg bg-primary font-semibold text-primary-foreground shadow-sm transition-colors hover:bg-primary/90 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        size === "lg" ? "min-h-11 px-5 text-base" : "min-h-9 px-4 text-sm",
        className,
      )}
      data-testid="demo-cta-signup"
    >
      <UserPlus className="h-4 w-4" aria-hidden="true" />
      Sign up free
    </a>
  );
}

/** Sign up (primary) and Install Forge (secondary). */
export function DemoCta({ placement, size = "lg" }: { placement: DemoCtaPlacement; size?: "md" | "lg" }) {
  const [installing, setInstalling] = useState(false);
  // The public demo keeps sales actions in its persistent screen action row,
  // never inside a page explanation or journey callout.
  if (isDemoShell() && OFF_ON_DEMO.includes(placement)) return null;
  return (
    <span className="inline-flex flex-wrap items-center gap-2" data-testid={`demo-cta-${placement}`}>
      <SignUpButton placement={placement} size={size} />
      <button
        type="button"
        onClick={() => {
          tracked("install", placement);
          setInstalling(true);
        }}
        className={cn(
          "inline-flex touch-target items-center gap-1.5 rounded-lg border border-border bg-surface px-3 text-sm font-medium text-ink hover:bg-ink/5",
          size === "lg" ? "min-h-11" : "min-h-9",
        )}
        data-testid="demo-cta-install"
      >
        <Download className="h-3.5 w-3.5" aria-hidden="true" />
        Install Forge
      </button>
      <InstallForgeSheet open={installing} onClose={() => setInstalling(false)} />
    </span>
  );
}
