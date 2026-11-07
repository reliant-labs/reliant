// Copyright (c) 2025 Reliant Labs

/** One compact, non-modal explanation of the current page. Exploration never starts a tour. */
import { useEffect, useState } from "react";
import { useLocation, useNavigate } from "@tanstack/react-router";
import { CircleHelp } from "lucide-react";

import { OnboardingSpotlight } from "@/components/Onboarding/OnboardingSpotlight";
import { TourControls } from "@/components/Onboarding/TourControls";
import { parseTourParam } from "@/components/Onboarding/tourParam";
import { trackEvent } from "@/lib/analytics";

import { SignUpButton, InstallForgeLink } from "../DemoCta";
import { DemoCorner, useConsoleDrawerOpen } from "../DemoCorner";
import { demoSession } from "../demoSession";
import { DEMO_TIPS } from "./tipCopy";
import { demoTipKey, type DemoTipKey } from "./tipKeys";

/** Secondary information is disclosed by click, tap or keyboard, never hover only. */
export function PageAgentHelp({ tipKey }: { tipKey: DemoTipKey }) {
  const tip = DEMO_TIPS[tipKey];
  return (
    <details className="relative mt-2 text-xs" data-testid="demo-agent-help">
      <summary className="touch-target inline-flex cursor-pointer items-center rounded px-1 font-medium text-primary-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring">
        How an agent helps
      </summary>
      <div className="absolute bottom-full left-0 z-10 mb-1 max-h-48 w-full overflow-y-auto rounded-lg border border-border bg-popover p-3 text-muted-foreground shadow-lg" role="note">
        <p>{tip.help}</p>
        <code className="mt-2 block [overflow-wrap:anywhere] font-mono">{tip.command}</code>
      </div>
    </details>
  );
}

export function DemoTips() {
  const location = useLocation();
  const navigate = useNavigate();
  const search = (location.search ?? {}) as Record<string, unknown>;
  const key = demoTipKey(location.pathname, search);
  const tourOpen = !!parseTourParam(search.tour);
  const [open, setOpen] = useState(false);

  useEffect(() => {
    // The old global offer no longer competes with the page's own explanation.
    demoSession.dismissToast();
    if (!key || tourOpen) { setOpen(false); return; }
    setOpen(!demoSession.hasSeenTip(key));
    demoSession.markTipSeen(key);
  }, [key, tourOpen]);

  if (tourOpen) return null;
  const tip = key ? DEMO_TIPS[key] : null;
  const dismiss = () => {
    setOpen(false);
    trackEvent("demo_tip_dismissed", { tip: key ?? "none" });
  };
  const start = () => {
    if (!key) return;
    trackEvent("demo_page_tour_started", { tip: key });
    void navigate({ search: ((prev: Record<string, unknown>) => ({ ...prev, tour: "forge:fg-intro" })) as never });
  };
  return (
    <DemoCorner order={1}>
      {open && tip && key ? (
        <section aria-label={`About this page: ${tip.title}`} className="max-h-[50dvh] overflow-y-auto rounded-xl border border-border bg-popover p-3 text-popover-foreground shadow-xl" data-testid="demo-tip" data-tip={key}>
          <h3 className="text-sm font-semibold">{tip.title}</h3>
          <p className="mt-1 text-sm leading-relaxed text-muted-foreground">{tip.body}</p>
          <div className="mt-2 flex items-center justify-end gap-2">
            <button type="button" onClick={dismiss} className="touch-target rounded-md border border-border px-3 py-1.5 text-xs font-medium" data-testid="demo-tip-dismiss">Dismiss</button>
            <button type="button" onClick={start} className="touch-target rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground" data-testid="demo-page-tour-start">Take the tour</button>
          </div>
          <PageAgentHelp tipKey={key} />
        </section>
      ) : null}
      <div className="mt-2 flex items-center justify-end gap-2" data-testid="demo-corner-bar">
        <SignUpButton placement="corner" size="md" />
        <InstallForgeLink placement="corner" />
        {!open && key && (
          <button type="button" onClick={() => setOpen(true)} aria-label={`About ${tip?.title}`} className="touch-target inline-flex h-9 w-9 items-center justify-center rounded-full border border-border bg-popover text-foreground shadow-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring" data-testid="demo-page-info">
            <CircleHelp className="h-5 w-5" aria-hidden="true" />
          </button>
        )}
      </div>
    </DemoCorner>
  );
}

/** The public demo's optional tour: explain only the page actually visible.
 * Explicit Next/Back navigate real console pages. Old step IDs remain valid entry links,
 * but never force a visitor back to a prerequisite or open an auth-gated surface.
 */
const TOUR_PAGES = [
  { key: "overview", path: "/forge/p/shop", step: "fg-intro", label: "Overview" },
  { key: "traces", path: "/forge/p/shop/observability/traces", step: "fg-trace", label: "Traces" },
  { key: "errors", path: "/forge/p/shop/observability/errors", step: "fg-issue", label: "Errors" },
  { key: "alerts", path: "/forge/p/shop/observability/alerts", step: "fg-oncall", label: "Alerts" },
] as const;

export function DemoPageTour() {
  const location = useLocation();
  const navigate = useNavigate();
  const drawerOpen = useConsoleDrawerOpen();
  const key = demoTipKey(location.pathname, (location.search ?? {}) as Record<string, unknown>) ?? "overview";
  const tip = DEMO_TIPS[key];
  const index = TOUR_PAGES.findIndex((p) => p.key === (key === "trace" ? "traces" : key));
  const at = Math.max(0, index);
  const next = TOUR_PAGES[at + 1];
  const back = index > 0 ? TOUR_PAGES[at - 1] : undefined;
  const go = (page: typeof TOUR_PAGES[number]) => void navigate({ to: page.path, search: { env: "prod", tour: `forge:${page.step}` } } as never);
  const exit = () => void navigate({ search: ((prev: Record<string, unknown>) => ({ ...prev, tour: undefined })) as never });
  useEffect(() => {
    const listener = (e: KeyboardEvent) => { if (e.key === "Escape") exit(); };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
    // Exit only changes the current URL's tour param.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [location.pathname, location.search]);
  if (drawerOpen) return null;
  const footer = <TourControls stepNumber={at + 1} totalSteps={TOUR_PAGES.length} onBack={back ? () => go(back) : undefined} onNext={next ? () => go(next) : exit} nextLabel={next ? `Next: ${next.label}` : "Done"} onExit={exit} />;
  const description = <>{tip.body}<PageAgentHelp tipKey={key} /></>;
  // No missing-target retry or forced action: a page without an anchor gets a compact card.
  const target = tip.target && document.querySelector(tip.target) ? tip.target : undefined;
  if (target) return <OnboardingSpotlight key={`${location.pathname}-${key}`} targetSelector={target} title={tip.title} description={description} stepNumber={at + 1} totalSteps={TOUR_PAGES.length} onNext={next ? () => go(next) : exit} onBack={back ? () => go(back) : undefined} onSkipAll={exit} callout={{ footer }} />;
  return (
    <DemoCorner order={1}>
      <section className="max-h-[60dvh] overflow-y-auto rounded-xl border border-border bg-popover p-4 text-popover-foreground shadow-xl" data-testid="tour-callout">
        <h3 className="text-sm font-semibold">{tip.title}</h3>
        <div className="mt-1 text-sm leading-relaxed text-muted-foreground">{description}</div>
        {footer}
      </section>
    </DemoCorner>
  );
}
