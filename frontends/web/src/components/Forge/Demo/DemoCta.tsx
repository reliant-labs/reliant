// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's calls to action: **Deploy yours free** (the primary,
 * large, and the only button on a phone), **Install Forge** (opens
 * InstallForgeSheet) and **Sign up free** (a quiet secondary). They sit in the
 * banner and on the tour's end card. Each click records
 * `demo_cta_clicked{cta, placement}`.
 *
 * Deploy yours free goes to the landing page's start checklist
 * (reliantlabs.io/products/forge#start: one command, no account) with
 * `utm_source=demo&utm_campaign=forge_demo&utm_content=<placement>`. Sign up
 * goes to forge's sign-up (forge.reliantlabs.io, `forgeAppOrigin`) with the
 * same UTMs and `journey=forge`. Through the web runtime's withAttribution both
 * carry this visitor's id as `rid`, so the click joins the demo funnel
 * (nothing is added when telemetry is not running: a browser that opted out).
 */

import { useState } from "react";
import { withAttribution } from "@reliantlabs/forge-web-runtime";
import { Download, Rocket } from "lucide-react";

import { trackEvent } from "@/lib/analytics";
import { forgeAppOrigin, isDemoShell } from "@/lib/shell";
import { cn } from "@/lib/utils";

import { InstallForgeSheet } from "./InstallForgeSheet";

/** Where a CTA sits: the landing UTM's `utm_content`, and analytics' `placement`. */
export type DemoCtaPlacement = "banner" | "corner" | "end_card" | "page_card" | "toast" | "tour_intro" | "agent_sheet" | "landing";

/** The landing page's start checklist: deploy an app with one command, no account. */
export const DEPLOY_START_URL = "https://reliantlabs.io/products/forge";

function attributed(url: string): string {
  try {
    return withAttribution(url);
  } catch {
    return url;
  }
}

/** Where Deploy yours free goes from `placement`. The UTMs sit before the #start anchor. */
export function deployHref(placement: DemoCtaPlacement): string {
  return attributed(`${DEPLOY_START_URL}?utm_source=demo&utm_campaign=forge_demo&utm_content=${placement}#start`);
}

/** Where Sign up goes from `placement`. */
export function signupHref(placement: DemoCtaPlacement): string {
  return attributed(`${forgeAppOrigin()}/auth?mode=signup&journey=forge&utm_source=demo&utm_campaign=forge_demo&utm_content=${placement}`);
}

/**
 * Where the public demo shows no CTA: its action row carries them, so a page
 * explanation or a mid-tour card never repeats them. The tour's end card does:
 * it is where the tour hands off.
 */
const OFF_ON_DEMO: readonly DemoCtaPlacement[] = ["page_card", "toast", "tour_intro", "corner"];

const tracked = (cta: "deploy" | "signup" | "install", placement: DemoCtaPlacement) => trackEvent("demo_cta_clicked", { cta, placement });

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

/** Deploy yours free, as the page's primary button. */
export function DeployYoursButton({ placement, size = "lg", className }: { placement: DemoCtaPlacement; size?: "md" | "lg"; className?: string }) {
  if (isDemoShell() && OFF_ON_DEMO.includes(placement)) return null;
  return (
    <a
      href={deployHref(placement)}
      target="_blank"
      rel="noopener"
      onClick={() => tracked("deploy", placement)}
      className={cn(
        "no-color inline-flex touch-target items-center justify-center gap-2 rounded-lg bg-primary font-semibold text-primary-foreground shadow-sm transition-colors hover:bg-primary/90 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        size === "lg" ? "min-h-11 px-5 text-base" : "min-h-9 px-4 text-sm",
        className,
      )}
      data-testid="demo-cta-deploy"
    >
      <Rocket className="h-4 w-4" aria-hidden="true" />
      Deploy yours free
    </a>
  );
}

/** Sign up free, as a quiet secondary link. */
export function SignUpLink({ placement, className }: { placement: DemoCtaPlacement; className?: string }) {
  if (isDemoShell() && OFF_ON_DEMO.includes(placement)) return null;
  return (
    <a
      href={signupHref(placement)}
      target="_blank"
      rel="noopener"
      onClick={() => tracked("signup", placement)}
      className={cn("touch-target inline-flex items-center font-medium text-primary-ink underline-offset-2 hover:underline", className)}
      data-testid="demo-cta-signup"
    >
      Sign up free
    </a>
  );
}

/** Deploy yours free (primary), Install Forge and Sign up free (secondary). */
export function DemoCta({ placement, size = "lg" }: { placement: DemoCtaPlacement; size?: "md" | "lg" }) {
  const [installing, setInstalling] = useState(false);
  // The public demo keeps sales actions in its persistent screen action row,
  // never inside a page explanation or journey callout.
  if (isDemoShell() && OFF_ON_DEMO.includes(placement)) return null;
  return (
    <span className="inline-flex flex-wrap items-center gap-2" data-testid={`demo-cta-${placement}`}>
      <DeployYoursButton placement={placement} size={size} />
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
      <SignUpLink placement={placement} className="px-1 text-sm" />
      <InstallForgeSheet open={installing} onClose={() => setInstalling(false)} />
    </span>
  );
}
