// Copyright (c) 2025 Reliant Labs

/** One compact, non-modal explanation of the current page. Exploration never starts a tour. */
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
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
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const panel = useRef<HTMLDivElement>(null);
  const id = useId();
  useEffect(() => {
    if (!open) return;
    const close = (e: PointerEvent) => { if (!trigger.current?.contains(e.target as Node) && !panel.current?.contains(e.target as Node)) setOpen(false); };
    const escape = (e: KeyboardEvent) => { if (e.key === "Escape") { e.stopImmediatePropagation(); setOpen(false); trigger.current?.focus(); } };
    document.addEventListener("pointerdown", close);
    window.addEventListener("keydown", escape, true);
    return () => { document.removeEventListener("pointerdown", close); window.removeEventListener("keydown", escape, true); };
  }, [open]);
  const rect = trigger.current?.getBoundingClientRect();
  const width = Math.min(320, window.innerWidth - 24);
  return (
    <div className="mt-2 text-xs" data-testid="demo-agent-help">
      <button ref={trigger} type="button" aria-expanded={open} aria-controls={open ? id : undefined} onClick={() => setOpen(true)} onFocus={() => setOpen(true)} className="touch-target inline-flex cursor-pointer items-center rounded px-1 font-medium text-primary-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring">How an agent helps</button>
      {open && createPortal(
        <div ref={panel} id={id} role="note" className="fixed z-[200] max-h-48 overflow-y-auto rounded-lg border border-border bg-popover p-3 text-xs text-ink-muted shadow-xl" style={{ width, left: Math.max(12, Math.min(rect?.left ?? 12, window.innerWidth - width - 12)), bottom: Math.max(12, window.innerHeight - (rect?.top ?? window.innerHeight - 12) + 4) }}>
          <p>{tip.help}</p><code className="mt-2 block [overflow-wrap:anywhere] font-mono">{tip.command}</code>
        </div>, document.body,
      )}
    </div>
  );
}

/** Keep the explanation in the page flow, never over a table, target or drawer. */
function PageInfoFlow({ children, onVisible }: { children: React.ReactNode; onVisible: () => void }) {
  const [host, setHost] = useState<HTMLElement | null>(null);
  useEffect(() => {
    const slot = document.createElement("div");
    slot.className = "mb-4 w-full max-w-xl";
    let revealFrame: number | undefined;
    const attach = () => {
      const main = document.querySelector("main");
      if (main && slot.parentElement !== main) {
        main.prepend(slot);
        setHost(slot);
        // Console navigation retains this overflow scroller. Without resetting it,
        // a newly offered card can be above the viewport and still be marked seen.
        // Only an open offer mounts this component; dismissed pages keep their scroll.
        main.scrollTop = 0;
        revealFrame = requestAnimationFrame(() => { if (slot.isConnected) main.scrollTop = 0; });
      }
    };
    attach();
    const observer = new MutationObserver(attach);
    observer.observe(document.body, { childList: true, subtree: true });
    return () => { observer.disconnect(); if (revealFrame !== undefined) cancelAnimationFrame(revealFrame); slot.remove(); };
  }, []);
  useEffect(() => {
    if (!host) return;
    // Intersections are measured against the retained page scroller. Never consume
    // a session offer before its card has actually been presented to the visitor.
    const card = host.querySelector('[data-testid="demo-tip"]');
    if (!card || typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver(([entry]) => {
      if (!entry?.isIntersecting || entry.intersectionRatio < 0.75) return;
      if (document.querySelector('#sidebar-layout-nav[aria-modal="true"]')) return;
      onVisible();
      observer.disconnect();
    }, { root: host.parentElement, threshold: 0.75 });
    observer.observe(card);
    return () => observer.disconnect();
  }, [host, onVisible]);
  return host ? createPortal(children, host) : null;
}

export function DemoTips() {
  const location = useLocation();
  const navigate = useNavigate();
  const search = (location.search ?? {}) as Record<string, unknown>;
  const key = demoTipKey(location.pathname, search);
  const tourOpen = !!parseTourParam(search.tour);
  const [open, setOpen] = useState(false);
  const [offeredKey, setOfferedKey] = useState<DemoTipKey | null>(null);
  const drawerOpen = useConsoleDrawerOpen();

  useEffect(() => {
    // The old global offer no longer competes with the page's own explanation.
    demoSession.dismissToast();
    if (!key || tourOpen) { setOpen(false); setOfferedKey(null); return; }
    setOfferedKey(key);
    setOpen(!demoSession.hasSeenTip(key));
  }, [key, tourOpen]);
  const markVisible = useCallback(() => { if (offeredKey) demoSession.markTipSeen(offeredKey); }, [offeredKey]);

  if (tourOpen) return null;
  const tip = key ? DEMO_TIPS[key] : null;
  const dismiss = () => {
    if (key) demoSession.markTipSeen(key);
    setOpen(false);
    trackEvent("demo_tip_dismissed", { tip: key ?? "none" });
  };
  const start = () => {
    if (!key) return;
    demoSession.markTipSeen(key);
    trackEvent("demo_page_tour_started", { tip: key });
    void navigate({ search: ((prev: Record<string, unknown>) => ({ ...prev, tour: "forge:fg-intro" })) as never });
  };
  return (
    <>
      {open && offeredKey === key && tip && key && !drawerOpen ? (
        <PageInfoFlow key={key} onVisible={markVisible}><section aria-label={`About this page: ${tip.title}`} className="rounded-xl border border-border bg-popover p-3 text-popover-foreground" data-testid="demo-tip" data-tip={key}>
          <h3 className="text-sm font-semibold">{tip.title}</h3>
          <p className="mt-1 text-sm leading-relaxed text-ink-muted">{tip.body}</p>
          <div className="mt-2 flex items-center justify-end gap-2">
            <button type="button" onClick={dismiss} className="touch-target rounded-md border border-border px-3 py-1.5 text-xs font-medium" data-testid="demo-tip-dismiss">Dismiss</button>
            <button type="button" onClick={start} className="touch-target rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground" data-testid="demo-page-tour-start">Take the tour</button>
          </div>
          <PageAgentHelp tipKey={key} />
        </section></PageInfoFlow>
      ) : null}
      <DemoCorner order={1}><div className="mt-2 flex items-center justify-end gap-2" data-testid="demo-corner-bar">
        <SignUpButton placement="corner" size="md" />
        <InstallForgeLink placement="corner" />
        {!open && key && (
          <button type="button" onClick={() => { setOpen(true); requestAnimationFrame(() => document.querySelector('[data-testid="demo-tip"]')?.scrollIntoView?.({ block: "nearest" })); }} aria-label={`About ${tip?.title}`} className="touch-target inline-flex h-9 w-9 items-center justify-center rounded-full border border-border bg-popover text-ink shadow-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring" data-testid="demo-page-info">
            <CircleHelp className="h-5 w-5" aria-hidden="true" />
          </button>
        )}
      </div></DemoCorner>
    </>
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
        <div className="mt-1 text-sm leading-relaxed text-ink-muted">{description}</div>
        {footer}
      </section>
    </DemoCorner>
  );
}
