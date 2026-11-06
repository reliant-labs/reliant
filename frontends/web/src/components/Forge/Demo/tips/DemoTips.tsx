// Copyright (c) 2025 Reliant Labs

/**
 * The demo's one-time tips: the first time a visitor opens a console view
 * (Overview, Traces, a trace, Logs, Profiles, Issues, Releases, Environments,
 * Secrets, Domains, Dashboards, Alerts), a short card in the bottom-right
 * corner says what it is, how it ties to the rest of forge, and what a coding
 * agent does with it (tipCopy.ts).
 *
 *  - Once per view per session (demoSession.ts): a tip counts as shown when
 *    it appears, so leaving the view without dismissing it doesn't bring it
 *    back.
 *  - "Got it" closes this one; "Exit tips" turns them all off for the session.
 *  - Off while the guided tour is open (its cards say the same, and would
 *    stack), and nothing is marked shown meanwhile.
 *  - It covers nothing it talks about: no spotlight, no dimming, just a card
 *    in the corner the toast shares (DemoCorner).
 *
 * Mounted once at the demo shell's root (demoShellRoutes.tsx).
 */

import { useEffect, useState } from "react";
import { useLocation } from "@tanstack/react-router";
import { Bot, Lightbulb, X } from "lucide-react";

import { parseTourParam } from "@/components/Onboarding/tourParam";
import { trackEvent } from "@/lib/analytics";

import { DemoCorner } from "../DemoCorner";
import { demoSession, useDemoSession } from "../demoSession";
import { DEMO_TIPS } from "./tipCopy";
import { demoTipKey, type DemoTipKey } from "./tipKeys";

export function DemoTips() {
  const location = useLocation();
  const search = (location.search ?? {}) as Record<string, unknown>;
  const key = demoTipKey(location.pathname, search);
  const tourOpen = !!parseTourParam(search.tour);
  const { tipsDisabled } = useDemoSession();
  const [shown, setShown] = useState<DemoTipKey | null>(null);

  useEffect(() => {
    if (tourOpen || tipsDisabled) {
      setShown(null);
      return;
    }
    if (key && !demoSession.hasSeenTip(key)) {
      demoSession.markTipSeen(key);
      setShown(key);
      trackEvent("demo_tip_shown", { tip: key });
    } else {
      setShown((open) => (open === key ? open : null));
    }
  }, [key, tourOpen, tipsDisabled]);

  if (!shown) return null;
  const tip = DEMO_TIPS[shown];

  return (
    <DemoCorner order={1}>
      <section
        aria-label={`Tip: ${tip.title}`}
        aria-live="polite"
        className="rounded-xl border border-border bg-popover p-4 text-popover-foreground shadow-2xl"
        data-testid="demo-tip"
        data-tip={shown}
      >
        <div className="flex items-start gap-2">
          <Lightbulb className="mt-0.5 h-4 w-4 shrink-0 text-primary" aria-hidden="true" />
          <h3 className="flex-1 text-sm font-semibold text-foreground">{tip.title}</h3>
          <button
            type="button"
            onClick={() => {
              trackEvent("demo_tip_dismissed", { tip: shown });
              setShown(null);
            }}
            aria-label="Dismiss this tip"
            className="-m-1 rounded p-1 text-muted-foreground hover:text-foreground"
          >
            <X className="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        </div>
        <p className="mt-1.5 text-sm leading-relaxed text-muted-foreground">{tip.body}</p>
        <p className="mt-2 flex gap-1.5 text-xs leading-relaxed text-muted-foreground" data-testid="demo-tip-agent">
          <Bot className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span>
            {tip.agent.before} <code className="rounded bg-surface-sunken px-1 py-0.5 font-mono text-[11px] text-ink">{tip.agent.command}</code>
            {tip.agent.after}
          </span>
        </p>
        <div className="mt-3 flex items-center justify-end gap-3">
          <button
            type="button"
            onClick={() => {
              trackEvent("demo_tips_exited", { tip: shown });
              demoSession.disableTips();
            }}
            className="text-xs font-medium text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
            data-testid="demo-tips-exit"
          >
            Exit tips
          </button>
          <button
            type="button"
            onClick={() => {
              trackEvent("demo_tip_dismissed", { tip: shown });
              setShown(null);
            }}
            className="rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90"
            data-testid="demo-tip-dismiss"
          >
            Got it
          </button>
        </div>
      </section>
    </DemoCorner>
  );
}
