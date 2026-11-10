// Copyright (c) 2025 Reliant Labs

/**
 * One current-page explanation; no cross-page sequence.
 *
 *  - Laptop and up: a page's tip opens by itself the FIRST time it is visited
 *    this browser session (demoSession.seenTips), and never moves the page to
 *    do it: a target off screen gets the corner card instead of a spotlight.
 *    After that, and once dismissed, the page has the corner's "?" button.
 *  - Phone: a one-line "About this page" disclosure at the top of the page,
 *    closed by default; it never pushes the data down unasked.
 */
import { useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { useLocation } from "@tanstack/react-router";
import { ChevronDown, CircleHelp } from "lucide-react";
import { isSheetViewport, OnboardingSpotlight } from "@/components/Onboarding/OnboardingSpotlight";
import { watchUserScroll } from "@/components/Onboarding/userScroll";
import { TourControls } from "@/components/Onboarding/TourControls";
import { isLegacyDemoTourLink } from "@/components/Onboarding/legacyDemoTour";
import { cn } from "@/lib/utils";
import { DemoCorner, useConsoleDrawerOpen } from "../DemoCorner";
import { demoSession } from "../demoSession";
import { PageAgentHelp } from "./DemoTips";
import { DEMO_TIPS } from "./tipCopy";
import { demoTipKey, type DemoTipKey } from "./tipKeys";

/** Path identifies real pages; legacy view/trace URLs also distinguish their visible page.
 * Range, environment, sorting, polling and the tour URL itself are not new visits.
 */
export function demoPageVisit(pathname: string, search: Record<string, unknown>): string {
  return `${pathname}:${demoTipKey(pathname, search) ?? "none"}`;
}

const PHONE = "(max-width: 1023px)";

export function PageEntryTour() {
  const location = useLocation();
  const search = (location.search ?? {}) as Record<string, unknown>;
  const key = demoTipKey(location.pathname, search);
  const visit = demoPageVisit(location.pathname, search);
  const mobile = useMediaQuery(PHONE);
  const drawerOpen = useConsoleDrawerOpen();
  // A guided tour owns the page, except an old trace-less deep link, which
  // keeps its page card (isLegacyDemoTourLink).
  if (!key || (search.tour && !isLegacyDemoTourLink(search)) || drawerOpen) return null;
  return mobile ? <MobilePageDisclosure key={visit} tipKey={key} /> : <VisitTour key={visit} tipKey={key} />;
}

function VisitTour({ tipKey }: { tipKey: DemoTipKey }) {
  // First visit this session: open by itself. A reload, or coming back, does not.
  const [open, setOpen] = useState(() => !demoSession.hasSeenTip(tipKey));
  // Opened from "?": the reader asked, so the page may scroll to the tip's anchor.
  const [requested, setRequested] = useState(false);
  const restoreFocus = useRef(false);
  const tip = DEMO_TIPS[tipKey];
  useEffect(() => { demoSession.markTipSeen(tipKey); }, [tipKey]);
  const dismiss = () => {
    // Focus goes back to "?" only when it was in the card (a keyboard reader); a click leaves it where it was.
    restoreFocus.current = !!document.activeElement?.closest("[data-page-tour], [data-testid='onboarding-callout']");
    setOpen(false);
  };
  useEffect(() => {
    if (open || !restoreFocus.current) return;
    restoreFocus.current = false;
    document.querySelector<HTMLButtonElement>('[data-testid="demo-page-info"]')?.focus({ preventScroll: true });
  }, [open]);
  useEffect(() => {
    if (!open) return;
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape" && !event.defaultPrevented) dismiss(); };
    window.addEventListener("keydown", escape);
    return () => window.removeEventListener("keydown", escape);
    // Dismissal changes only visit-local state.
  }, [open]);
  return (
    <>
      {/* Register with the shell's capture-phase Escape protocol. Keep our
          handler at window bubble so a nested dialog handles Escape first. */}
      {open ? <div data-escape-layer="" className="contents"><PageCallout tipKey={tipKey} requested={requested} onDismiss={dismiss} /></div> : (
        <DemoCorner order={1}>
          <div className="flex items-center justify-end gap-2" data-testid="demo-corner-bar">
            <button type="button" aria-label={`About ${tip.title}`} title={`About ${tip.title}`} onClick={() => { setRequested(true); setOpen(true); }} className="touch-target inline-flex h-9 w-9 items-center justify-center rounded-full border border-border bg-popover text-ink shadow-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring" data-testid="demo-page-info"><CircleHelp className="h-5 w-5" aria-hidden="true" /></button>
          </div>
        </DemoCorner>
      )}
    </>
  );
}

/** Phone: one line at the top of the page, opened only by a tap. */
function MobilePageDisclosure({ tipKey }: { tipKey: DemoTipKey }) {
  const [open, setOpen] = useState(false);
  const tip = DEMO_TIPS[tipKey];
  useEffect(() => { demoSession.markTipSeen(tipKey); }, [tipKey]);
  return (
    <MobilePageSlot>
      <div className="text-sm text-ink" data-page-tour={tipKey}>
        <button type="button" aria-expanded={open} onClick={() => setOpen((was) => !was)} className="inline-flex min-h-[44px] items-center gap-1.5 text-sm font-medium text-ink-muted hover:text-ink" data-testid="demo-page-info">
          <CircleHelp className="h-4 w-4" aria-hidden="true" />
          About this page
          <ChevronDown className={cn("h-4 w-4 transition-transform", open && "rotate-180")} aria-hidden="true" />
        </button>
        {open && (
          <section className="rounded-lg border border-border bg-surface p-3" data-testid="tour-callout">
            <h3 className="font-semibold">{tip.title}</h3>
            <div className="mt-1 leading-relaxed text-ink-muted">{tip.body}<PageAgentHelp tipKey={tipKey} /></div>
          </section>
        )}
      </div>
    </MobilePageSlot>
  );
}

/** Mobile explanations occupy scrollable page space, never a fixed cover over data. */
function MobilePageSlot({ children }: { children: ReactNode }) {
  const [host, setHost] = useState<HTMLElement | null>(null);
  useEffect(() => {
    const slot = document.createElement("div");
    slot.className = "shrink-0 pt-1";
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

/** Whether `el` is inside the page's visible box now (the main pane, under the chrome). */
function inView(el: Element): boolean {
  const box = el.getBoundingClientRect();
  const page = document.querySelector("main")?.getBoundingClientRect();
  const top = page?.top ?? 0;
  const bottom = page?.bottom ?? window.innerHeight;
  return box.height > 0 && box.top >= top && box.top < bottom;
}

function PageCallout({ tipKey, requested, onDismiss }: { tipKey: DemoTipKey; requested: boolean; onDismiss: () => void }) {
  const tip = DEMO_TIPS[tipKey];
  const [target, setTarget] = useState<string | null>(null);
  // From the visit's start: a reader who scrolled before the anchor mounted keeps their place.
  const [readerScrolled, setReaderScrolled] = useState(false);
  useEffect(() => {
    // Lazy page chunks may mount after the visit. Upgrade to their anchor once,
    // without restarting a dismissed tour or polling a fixed-duration timer.
    let scrolled = false;
    const stopWatching = watchUserScroll(() => { scrolled = true; setReaderScrolled(true); });
    const find = () => {
      if (!tip.target) return false;
      const element = document.querySelector(tip.target);
      if (!element) return false;
      if (requested) {
        // Asked for: bring the anchor into view (a phone's sheet places it itself).
        if (!scrolled && !isSheetViewport()) element.scrollIntoView?.({ block: "start", behavior: "auto" });
      } else if (!inView(element)) {
        // Opened by itself: never move the page. An anchor below the fold keeps the corner card.
        return true;
      }
      setTarget(tip.target);
      return true;
    };
    if (find()) return stopWatching;
    const observer = new MutationObserver(() => { if (find()) observer.disconnect(); });
    observer.observe(document.body, { childList: true, subtree: true });
    return () => { observer.disconnect(); stopWatching(); };
  }, [tip.target, requested]);
  const footer = <TourControls stepNumber={1} totalSteps={1} onNext={onDismiss} nextLabel="Done" />;
  const description = <>{tip.body}<PageAgentHelp tipKey={tipKey} /></>;
  // Place the card inside the page's own box: right of the desktop sidebar and below the screen-action row.
  const page = typeof document === "undefined" ? undefined : document.querySelector("main")?.getBoundingClientRect();
  if (target) return <OnboardingSpotlight targetSelector={target} title={tip.title} description={description} stepNumber={1} totalSteps={1} onNext={onDismiss} onSkipAll={onDismiss} keyboardNav={false} callout={{ footer, viewportLeft: Math.max(0, page?.left ?? 0), viewportTop: Math.max(0, page?.top ?? 0), autoScroll: requested && !readerScrolled }} />;
  return <DemoCorner order={1}><section className="max-h-[70dvh] overflow-y-auto rounded-xl border border-border bg-popover p-4 text-popover-foreground shadow-xl" data-testid="tour-callout" data-page-tour={tipKey}><h3 className="text-sm font-semibold">{tip.title}</h3><div className="mt-1 text-sm leading-relaxed text-ink-muted">{description}</div>{footer}</section></DemoCorner>;
}
