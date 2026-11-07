// Copyright (c) 2025 Reliant Labs

/** One current-page callout, automatically shown for each visit; no cross-page sequence. */
import { useEffect, useState } from "react";
import { useLocation, useNavigate } from "@tanstack/react-router";
import { CircleHelp } from "lucide-react";
import { OnboardingSpotlight } from "@/components/Onboarding/OnboardingSpotlight";
import { TourControls } from "@/components/Onboarding/TourControls";
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
  if (!key) return null;
  // A route change unmounts visit-local dismissal. Refresh also starts fresh;
  // deliberately no seen/session storage can suppress an arrival callout.
  return <VisitTour key={visit} tipKey={key} />;
}

function VisitTour({ tipKey }: { tipKey: DemoTipKey }) {
  const [open, setOpen] = useState(true);
  const drawerOpen = useConsoleDrawerOpen();
  const navigate = useNavigate();
  const tip = DEMO_TIPS[tipKey];
  const dismiss = () => {
    setOpen(false);
    // Old deep links still explain their visible page, then dismiss locally.
    void navigate({ replace: true, search: ((prev: Record<string, unknown>) => ({ ...prev, tour: undefined })) as never });
  };
  useEffect(() => {
    if (!open || drawerOpen) return;
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") dismiss(); };
    window.addEventListener("keydown", escape);
    return () => window.removeEventListener("keydown", escape);
    // Dismissal changes only visit-local state and the legacy tour query.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, drawerOpen]);
  if (drawerOpen) return null;
  return (
    <>
      {open ? <PageCallout tipKey={tipKey} onDismiss={dismiss} /> : null}
      {!open && <DemoCorner order={1}>
        <div className="flex items-center justify-end gap-2" data-testid="demo-corner-bar">
          <button type="button" aria-label={`About ${tip.title}`} onClick={() => setOpen(true)} className="touch-target inline-flex h-9 w-9 items-center justify-center rounded-full border border-border bg-popover text-ink shadow-md focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring" data-testid="demo-page-info"><CircleHelp className="h-5 w-5" aria-hidden="true" /></button>
        </div>
      </DemoCorner>}
    </>
  );
}

function PageCallout({ tipKey, onDismiss }: { tipKey: DemoTipKey; onDismiss: () => void }) {
  const tip = DEMO_TIPS[tipKey];
  const [target, setTarget] = useState<string | null>(null);
  useEffect(() => {
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
  }, [tip.target]);
  const footer = <TourControls stepNumber={1} totalSteps={1} onNext={onDismiss} nextLabel="Done" />;
  const description = <>{tip.body}<PageAgentHelp tipKey={tipKey} /></>;
  if (target) return <OnboardingSpotlight targetSelector={target} title={tip.title} description={description} stepNumber={1} totalSteps={1} onNext={onDismiss} onSkipAll={onDismiss} keyboardNav={false} callout={{ footer, viewportLeft: Math.max(0, document.querySelector('main')?.getBoundingClientRect().left ?? 0) }} />;
  return <DemoCorner order={1}><section className="max-h-[70dvh] overflow-y-auto rounded-xl border border-border bg-popover p-4 text-popover-foreground shadow-xl" data-testid="tour-callout" data-page-tour={tipKey}><h3 className="text-sm font-semibold">{tip.title}</h3><div className="mt-1 text-sm leading-relaxed text-ink-muted">{description}</div>{footer}</section></DemoCorner>;
}
