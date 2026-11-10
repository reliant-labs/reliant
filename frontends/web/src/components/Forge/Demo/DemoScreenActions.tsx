// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's one row of chrome under the console's header: a quiet
 * "Sample app · read-only data" line (and, from a laptop up, what the demo
 * records), then Take the tour, Install Forge and the primary Sign up free.
 *
 * On a phone the row is one line, the status and Sign up: the tour, Install
 * and the telemetry notice sit in the nav drawer (DemoDrawerActions, through
 * DemoNavExtras). "How it was built" is a nav row of its own, not an action.
 *
 * It takes no z-index: dialogs, the tour's overlay and its modal cards all
 * paint over it.
 */
import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useLocation, useNavigate } from "@tanstack/react-router";
import Button from "@/components/forge-ui/button";
import { parseTourParam } from "@/components/Onboarding/tourParam";
import { trackEvent } from "@/lib/analytics";
import { PRIVACY_POLICY_URL } from "@/lib/privacyLinks";
import { browserOptedOut, demoTelemetry, demoTelemetryNotice } from "@/lib/demoTelemetry";
import { SignUpButton, InstallForgeLink } from "./DemoCta";
import { useConsoleDrawerOpen } from "./DemoCorner";
import { useDemoDataStatus } from "./demoData/demoDataStatus";
import { FORGE_DEMO_PATH } from "./demoPath";

/** The tour's first stop, on the console it walks. */
export const TOUR_START = { to: FORGE_DEMO_PATH, search: { view: "overview", tour: "forge:fg-intro" } } as const;

/** One quiet line, said once: this is a sample, and nothing here changes anything. */
export function DemoStatusLine({ className }: { className?: string }) {
  const data = useDemoDataStatus();
  const text = data === "live" ? "Live sample app · read-only" : data === "fallback" ? "Sample data (live demo unavailable) · read-only" : "Sample app · read-only data";
  return <span className={className} data-testid="forge-demo-data" data-source={data}>{text}</span>;
}

/** What this demo records, and the privacy policy. A disclosure: shown on every entry path. */
export function DemoTelemetryNotice({ className }: { className?: string }) {
  const optedOut = browserOptedOut();
  const notice = demoTelemetryNotice(demoTelemetry(optedOut), optedOut);
  if (!notice) return null;
  return (
    <span className={className} data-testid="forge-demo-telemetry-notice">
      {notice}{" "}
      <a href={PRIVACY_POLICY_URL} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2">Privacy</a>
    </span>
  );
}

function useTourOpen(): boolean {
  const search = (useLocation().search ?? {}) as Record<string, unknown>;
  return parseTourParam(search.tour) !== null;
}

export function TakeTheTourButton({ className }: { className?: string }) {
  const navigate = useNavigate();
  return (
    <Button variant="ghost" size="sm" className={className} data-testid="demo-start-tour" onClick={() => {
      trackEvent("demo_tour_restarted", {});
      void navigate(TOUR_START as never);
    }}>Take the tour</Button>
  );
}

/** The phone drawer's share of the row: the tour, Install and the notice. */
export function DemoDrawerActions() {
  const tourOpen = useTourOpen();
  return (
    <div className="space-y-2 border-t border-border px-3 pt-3 text-sm" data-testid="demo-drawer-actions">
      {!tourOpen && <TakeTheTourButton className="-ml-2" />}
      <div><InstallForgeLink placement="banner">Install Forge</InstallForgeLink></div>
      <DemoTelemetryNotice className="block text-xs text-ink-muted" />
    </div>
  );
}

export function DemoScreenActions() {
  const [host, setHost] = useState<HTMLElement | null>(null);
  const drawerOpen = useConsoleDrawerOpen();
  const tourOpen = useTourOpen();
  useEffect(() => {
    const slot = document.createElement("div");
    slot.className = "w-full shrink-0";
    const attach = () => {
      const main = document.querySelector("main");
      // A dedicated row below the existing header, outside its controls and
      // above the retained scroll host. Never overlay data or the mobile drawer.
      if (main?.parentElement && slot.parentElement !== main.parentElement) {
        main.parentElement.insertBefore(slot, main);
        setHost(slot);
      }
    };
    attach(); const observer = new MutationObserver(attach);
    observer.observe(document.body, {childList:true,subtree:true});
    return () => { observer.disconnect();slot.remove(); };
  }, []);
  if (!host || drawerOpen) return null;
  return createPortal(
    <nav aria-label="Demo actions" data-testid="demo-screen-actions" className="flex min-h-11 items-center gap-3 border-b border-border bg-surface px-3 py-1 text-sm md:px-8">
      <span className="min-w-0 flex-1 truncate text-xs text-ink-muted">
        <DemoStatusLine />
        <DemoTelemetryNotice className="max-lg:hidden before:mx-1.5 before:content-['·']" />
      </span>
      <div className="flex shrink-0 items-center gap-3">
        {!tourOpen && <TakeTheTourButton className="max-md:hidden" />}
        <span className="max-md:hidden"><InstallForgeLink placement="banner">Install Forge</InstallForgeLink></span>
        <SignUpButton placement="banner" size="md" />
      </div>
    </nav>,host,
  );
}
