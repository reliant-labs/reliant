// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's calls to action — Install forge (opens InstallForgeSheet)
 * and Sign up — in its banner and on the tour's end card. Each click records
 * `demo_cta_clicked{target, placement}`.
 *
 * Sign up goes to forge's sign-in once forge.reliantlabs.io is live, Reliant's
 * until then (FORGE_APP_LIVE, flipped in the launch PR). The link carries
 * `utm_source=demo` and, through the web runtime's withAttribution, this
 * visitor's id as `rid`, so the sign-up joins the demo funnel. withAttribution
 * adds nothing when telemetry is not running (a browser that opted out).
 */

import { useState } from "react";
import { withAttribution } from "@reliantlabs/forge-web-runtime";
import { Download, UserPlus } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { trackEvent } from "@/lib/analytics";
import { forgeAppOrigin, reliantAppOrigin } from "@/lib/shell";

import { InstallForgeSheet } from "./InstallForgeSheet";

/**
 * Whether forge.reliantlabs.io serves yet (it was `pending_dns` on 2026-11-05).
 * Until it does, Sign up lands on Reliant's sign-in, which is the same account.
 */
export const FORGE_APP_LIVE = false;

export type DemoCtaPlacement = "banner" | "end_card";

/** Where Sign up goes from `placement`. */
export function signupHref(placement: DemoCtaPlacement, live: boolean = FORGE_APP_LIVE): string {
  const origin = live ? forgeAppOrigin() : reliantAppOrigin();
  const url = `${origin}/auth?utm_source=demo&utm_medium=${placement}`;
  try {
    return withAttribution(url);
  } catch {
    return url;
  }
}

/** "Install forge" as a text link (the tour's intro, the toast), opening the same sheet. */
export function InstallForgeLink({ placement, children = "Install forge" }: { placement: string; children?: React.ReactNode }) {
  const [installing, setInstalling] = useState(false);
  return (
    <>
      <button
        type="button"
        onClick={() => {
          trackEvent("demo_cta_clicked", { target: "install", placement });
          setInstalling(true);
        }}
        className="font-medium text-primary-ink underline-offset-2 hover:underline"
        data-testid={`install-forge-link-${placement}`}
      >
        {children}
      </button>
      <InstallForgeSheet open={installing} onClose={() => setInstalling(false)} />
    </>
  );
}

export function DemoCta({ placement, size = "sm" }: { placement: DemoCtaPlacement; size?: "sm" | "md" }) {
  const [installing, setInstalling] = useState(false);
  return (
    <span className="inline-flex flex-wrap items-center gap-2" data-testid={`demo-cta-${placement}`}>
      <Button
        size={size}
        variant="primary"
        leftIcon={<Download className="h-3.5 w-3.5" />}
        onClick={() => {
          trackEvent("demo_cta_clicked", { target: "install", placement });
          setInstalling(true);
        }}
        data-testid="demo-cta-install"
      >
        Install forge
      </Button>
      <a
        href={signupHref(placement)}
        target="_blank"
        rel="noopener"
        onClick={() => trackEvent("demo_cta_clicked", { target: "signup", placement })}
        className="no-color inline-flex items-center gap-1.5 text-sm font-medium text-ink underline-offset-2 hover:underline"
        data-testid="demo-cta-signup"
      >
        <UserPlus className="h-3.5 w-3.5" aria-hidden="true" />
        Sign up
      </a>
      <InstallForgeSheet open={installing} onClose={() => setInstalling(false)} />
    </span>
  );
}
