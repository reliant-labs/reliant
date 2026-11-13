// Copyright (c) 2025 Reliant Labs

/** Automatically explain the current page on every visit, never a full-site sequence. */
import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { DEMO_TIPS } from "./tipCopy";
import type { DemoTipKey } from "./tipKeys";
import { PageEntryTour } from "./PageEntryTour";
import { DemoScreenActions } from "../DemoScreenActions";

/** The existing demo-shell mount owns automatic entry and visit-local dismissal. */
export function DemoTips() { return <><DemoScreenActions /><PageEntryTour /></>; }

/** Secondary information is disclosed by click, tap or keyboard, never hover only. */
/** Pages whose own "put an agent on it" card (approachable/AgentCards) replaces this disclosure. */
const AGENT_CARD_PAGES: readonly DemoTipKey[] = ["errors", "alerts", "traces", "releases", "release"];

export function PageAgentHelp({ tipKey }: { tipKey: DemoTipKey }) {
  if (AGENT_CARD_PAGES.includes(tipKey)) return null;
  return <AgentHelpDisclosure tipKey={tipKey} />;
}

function AgentHelpDisclosure({ tipKey }: { tipKey: DemoTipKey }) {
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
  const viewportWidth = typeof window === "undefined" ? 344 : window.innerWidth;
  const width = Math.min(320, viewportWidth - 24);
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
