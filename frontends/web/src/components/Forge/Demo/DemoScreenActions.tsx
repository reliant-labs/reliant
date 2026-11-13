// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's one row of chrome under the console's header: a quiet
 * "Sample app · read-only data" line and what the demo records (the same on
 * every entry, the tour's route included), then Take the tour, Install Forge, Sign up free and the primary Deploy yours free.
 *
 * On a phone the row is one line, the status and Deploy yours free: the tour, Install
 * and the telemetry notice sit in the nav drawer (DemoDrawerActions, through
 * DemoNavExtras). "How it was built" is a nav row of its own, not an action.
 *
 * It takes no z-index: dialogs, the tour's overlay and its modal cards all
 * paint over it.
 */
import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useLocation, useNavigate } from "@tanstack/react-router";
import { Moon, Sun } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { applyThemeMode, type ThemeMode } from "@/lib/themeMode";
import { parseTourParam } from "@/components/Onboarding/tourParam";
import { trackEvent } from "@/lib/analytics";
import { PRIVACY_POLICY_URL } from "@/lib/privacyLinks";
import { browserOptedOut, demoTelemetry, demoTelemetryNotice } from "@/lib/demoTelemetry";
import { DeployYoursButton, SignUpLink, InstallForgeLink } from "./DemoCta";
import { useConsoleDrawerOpen } from "./DemoCorner";
import { useDemoDataStatus } from "./demoData/demoDataStatus";
import { consolePath } from "../Console/paths";
import { FIXTURE_PROJECT } from "../Observability/__fixtures__/fixtureApi";

/** The tour's first stop: the sample app's Overview, on the console it walks. */
export const TOUR_START = { to: consolePath(FIXTURE_PROJECT, { kind: "overview" }), search: { tour: "forge:fg-intro" } } as const;

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

/** Light or dark, for this browser (the same `appearance.theme` key demo.html reads before paint). */
function ThemeToggle() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains("dark"));
  return (
    <button
      type="button"
      aria-label={dark ? "Use the light theme" : "Use the dark theme"}
      title={dark ? "Light theme" : "Dark theme"}
      onClick={() => {
        const next: ThemeMode = dark ? "light" : "dark";
        try {
          localStorage.setItem("appearance.theme", next);
        } catch {
          // Private mode: the theme lasts for the visit.
        }
        applyThemeMode(next);
        setDark(!dark);
      }}
      className="inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-ink-muted hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      data-testid="demo-theme-toggle"
    >
      {dark ? <Sun className="h-5 w-5" aria-hidden="true" /> : <Moon className="h-5 w-5" aria-hidden="true" />}
    </button>
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
      {/* The notice is a disclosure: it is never truncated away. From a tablet up it is in the row (wrapping if it must); on a phone, in the drawer. */}
      <span className="min-w-0 flex-1 text-xs text-ink-muted">
        <DemoStatusLine className="whitespace-nowrap" />
        <DemoTelemetryNotice className="max-md:hidden before:mx-1.5 before:content-['·']" />
      </span>
      <div className="flex shrink-0 items-center gap-3">
        {!tourOpen && <TakeTheTourButton className="max-md:hidden" />}
        <span className="max-md:hidden"><InstallForgeLink placement="banner">Install Forge</InstallForgeLink></span>
        <ThemeToggle />
        <SignUpLink placement="banner" className="max-md:hidden" />
        <DeployYoursButton placement="banner" size="md" />
      </div>
    </nav>,host,
  );
}
