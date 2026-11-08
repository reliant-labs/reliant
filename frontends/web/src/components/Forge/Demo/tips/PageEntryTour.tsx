// Copyright (c) 2025 Reliant Labs

/** One current-page callout, automatically shown for each visit; no cross-page sequence. */
import { useEffect, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { DemoHowBuiltLink } from "../DemoHowBuiltLink";
import { useLocation } from "@tanstack/react-router";
import { CircleHelp } from "lucide-react";
import { OnboardingSpotlight } from "@/components/Onboarding/OnboardingSpotlight";
import { TourControls } from "@/components/Onboarding/TourControls";
import { isLegacyDemoTourLink } from "@/components/Onboarding/legacyDemoTour";
import { DemoCorner, useConsoleDrawerOpen } from "../DemoCorner";
import { PageAgentHelp } from "./DemoTips";
import { DEMO_TIPS } from "./tipCopy";
import { demoTipKey, type DemoTipKey } from "./tipKeys";

/** Path identifies real pages; legacy view/trace URLs also distinguish their visible page.
 * Range, environment, sorting, polling and the tour URL itself are not new visits.
 */
export function demoPageVisit(pathname: string, search: Record<string, unknown>): string {
  return `${pathname}:${demoTipKey(pathname, search) ?? "none"}`;
}

export function PageEntryTour() {
  const location = useLocation();
  const search = (location.search ?? {}) as Record<string, unknown>;
  const key = demoTipKey(location.pathname, search);
  const visit = demoPageVisit(location.pathname, search);
  // A guided tour owns the page, except an old trace-less deep link, which
  // keeps its page card (isLegacyDemoTourLink).
  if (!key || (search.tour && !isLegacyDemoTourLink(search))) return null;
  // A route change unmounts visit-local dismissal. Refresh also starts fresh;
  // deliberately no seen/session storage can suppress an arrival callout.
  return <VisitTour key={visit} tipKey={key} />;
}

function VisitTour({ tipKey }: { tipKey: DemoTipKey }) {
  const [open, setOpen] = useState(true);
  const mobile = useMediaQuery("(max-width: 1023px)");
  const drawerOpen = useConsoleDrawerOpen();
  const tip = DEMO_TIPS[tipKey];
  const dismiss = () => {
    setOpen(false);
    document.querySelector<HTMLButtonElement>('[data-testid="demo-start-tour"]')?.focus();
    // No navigation on dismissal: filter normalization cannot create a new visit.
  };
  useEffect(() => {
    if (!open || drawerOpen) return;
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape" && !event.defaultPrevented) dismiss(); };
    window.addEventListener("keydown", escape);
    return () => window.removeEventListener("keydown", escape);
    // Dismissal changes only visit-local state.
  }, [open, drawerOpen]);
  if (drawerOpen) return null;
  return (
    <>
      {/* Register with the shell's capture-phase Escape protocol. Keep our
          handler at window bubble so a nested dialog handles Escape first. */}
      {open ? <div data-escape-layer="" className="contents"><PageCallout tipKey={tipKey} onDismiss={dismiss} /></div> : null}
      {!open && (mobile ? <MobilePageCallout><button type="button" onClick={() => setOpen(true)} className="inline-flex min-h-[44px] items-center gap-2 rounded-md border border-border px-3 text-sm text-ink" data-testid="demo-page-info"><CircleHelp className="h-4 w-4" aria-hidden="true" />About {tip.title}</button></MobilePageCallout> : <DemoCorner order={1}>
        <div className="flex items-center justify-end gap-2" data-testid="demo-corner-bar">
          <button type="button" aria-label={`About ${tip.title}`} onClick={() => setOpen(true)} className="touch-target inline-flex h-9 w-9 items-center justify-center rounded-full border border-border bg-popover text-ink shadow-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring" data-testid="demo-page-info"><CircleHelp className="h-5 w-5" aria-hidden="true" /></button>
        </div>
      </DemoCorner>)}
    </>
  );
}

/** Mobile explanations occupy scrollable page space, never a fixed cover over data. */
function MobilePageCallout({ children }: { children: ReactNode }) {
  const [host, setHost] = useState<HTMLElement | null>(null);
  useEffect(() => {
    const slot = document.createElement("div");
    slot.className = "shrink-0 py-3";
    slot.dataset.testid = "demo-mobile-explanation";
    const attach = () => {
      const main = document.querySelector("main");
      if (main && slot.parentElement !== main) { main.prepend(slot); setHost(slot); }
    };
    attach();
    const observer = new MutationObserver(attach);
    observer.observe(document.body, { childList: true, subtree: true });
    return () => { observer.disconnect(); slot.remove(); };
  }, []);
  return host ? createPortal(children, host) : null;
}

function PageCallout({ tipKey, onDismiss }: { tipKey: DemoTipKey; onDismiss: () => void }) {
  const tip = DEMO_TIPS[tipKey];
  const [target, setTarget] = useState<string | null>(null);
  const mobile = useMediaQuery("(max-width: 1023px)");
  useEffect(() => {
    if (mobile) return;
    // Lazy page chunks may mount after the visit. Upgrade to their anchor once,
    // without restarting a dismissed tour or polling a fixed-duration timer.
    const find = () => {
      if (!tip.target || !document.querySelector(tip.target)) return false;
      const element = document.querySelector(tip.target)!;
      element.scrollIntoView?.({ block: "start", behavior: "auto" });
      setTarget(tip.target);
      return true;
    };
    if (find()) return;
    const observer = new MutationObserver(() => { if (find()) observer.disconnect(); });
    observer.observe(document.body, { childList: true, subtree: true });
    return () => observer.disconnect();
  }, [tip.target, mobile]);
  const footer = <><div className="mt-3"><DemoHowBuiltLink placement="page_explanation" /></div><TourControls stepNumber={1} totalSteps={1} onNext={onDismiss} nextLabel="Done" /></>;
  const description = <>{tip.body}<PageAgentHelp tipKey={tipKey} /></>;
  // Place the card inside the page's own box: right of the desktop sidebar and
  // below the screen-action row, which stacks above the tour and would cover it.
  const page = typeof document === "undefined" ? undefined : document.querySelector("main")?.getBoundingClientRect();
  if (mobile) return <MobilePageCallout><section className="rounded-lg border border-border bg-surface p-3 text-sm text-ink" data-testid="tour-callout" data-page-tour={tipKey}><h3 className="font-semibold">{tip.title}</h3><div className="mt-1 leading-relaxed text-ink-muted">{description}</div>{footer}</section></MobilePageCallout>;
  if (target) return <OnboardingSpotlight targetSelector={target} title={tip.title} description={description} stepNumber={1} totalSteps={1} onNext={onDismiss} onSkipAll={onDismiss} keyboardNav={false} callout={{ footer, viewportLeft: Math.max(0, page?.left ?? 0), viewportTop: Math.max(0, page?.top ?? 0) }} />;
  return <DemoCorner order={1}><section className="max-h-[70dvh] overflow-y-auto rounded-xl border border-border bg-popover p-4 text-popover-foreground shadow-xl" data-testid="tour-callout" data-page-tour={tipKey}><h3 className="text-sm font-semibold">{tip.title}</h3><div className="mt-1 text-sm leading-relaxed text-ink-muted">{description}</div>{footer}</section></DemoCorner>;
}
