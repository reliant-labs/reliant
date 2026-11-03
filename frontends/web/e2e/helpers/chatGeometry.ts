import { expect, type Page, type TestInfo } from '@playwright/test';

/**
 * Vertical budget of the mobile chat screen, measured from the rendered DOM.
 *
 * Bands are found structurally rather than from a list of components: walk up
 * from the transcript scroller and, at every level, record each sibling that
 * takes space from it. That covers both layouts the redesign moves between:
 *
 * - an in-flow band (a header row above the transcript, the composer below
 *   it) shrinks the scroller. Its slot includes the gap above it, so
 *   collapsed margins (the old composer's `mt-1 mb-1.5`) are charged to the
 *   band that owns them, and the in-flow slots sum to the viewport height.
 * - a floating band (absolute or fixed, drawn over the scroller: the pinned
 *   prompt, a translucent top bar, a floating composer) leaves the scroller
 *   tall but hides what is under it. It counts against the readable height.
 *
 * A banner nobody listed still shows up (as "Other"), and a band that moves
 * between components is still counted exactly once. Labels come from
 * selectors meant to survive the redesign: testids, the semantic `header`,
 * and the composer's `data-onboarding` hook.
 */

export interface GeometryBand {
  label: string;
  /** In-flow: shrinks the scroller. Overlay: drawn over it. */
  kind: 'band' | 'overlay';
  /** In-flow: slot height (the box plus the gap above it). Overlay: the part over the scroller. */
  height: number;
  /** Rendered box height. */
  box: number;
  /** Extra facts for the log (e.g. thread tabs' share of the ChatHeader row). */
  detail?: string;
}

export interface ChatGeometry {
  viewport: { width: number; height: number };
  bands: GeometryBand[];
  /** The transcript scroller's slot, clipped to the viewport. */
  transcript: number;
  /** How much of the scroller the overlays cover (their union). */
  overlay: number;
  /** What is left to read: transcript minus overlays. */
  readable: number;
}

/** Selector → label, first match wins. */
export const BAND_SELECTORS: [label: string, selector: string][] = [
  ['Pinned prompt', '[data-testid="pinned-user-message-header"]'],
  ['Composer', '[data-onboarding="chat-input"]'],
  ['Top bar', '[data-testid="mobile-chat-top-bar"], header'],
  ['Workflow pill', '[data-testid="mobile-workflow-pill"], a[aria-label^="Workflow:"]'],
  ['ChatHeader row', '[data-testid="chat-header"]'],
  ['Status slot', '[data-testid="composer-status-slot"]'],
];

/** Elements inside a band whose height the log breaks out. */
const DETAIL_SELECTORS: [label: string, selector: string][] = [
  ['status slot', '[data-testid="composer-status-slot"]'],
  ['thread tabs', '[data-testid="thread-tabs"]'],
];

export async function measureChatGeometry(page: Page): Promise<ChatGeometry | null> {
  return page.evaluate(({ bandSelectors, detailSelectors }) => {
    const scroller = document.querySelector<HTMLElement>('[data-context="transcript"]');
    if (!scroller) return null;
    const vh = window.innerHeight;
    const clip = (r: DOMRect) => ({ top: Math.max(0, r.top), bottom: Math.min(vh, r.bottom) });
    const has = (el: Element, sel: string) => el.matches(sel) || !!el.querySelector(sel);
    const px = (el: Element) => Math.round(el.getBoundingClientRect().height);

    const describe = (el: Element): { label: string; detail?: string } => {
      const details: string[] = [];
      for (const [name, sel] of detailSelectors) {
        const inner = el.querySelector(sel);
        if (inner && px(inner) > 0) details.push(`${name} ${px(inner)}`);
      }
      const detail = details.join(', ') || undefined;
      for (const [label, sel] of bandSelectors) if (has(el, sel)) return { label, detail };
      // The pre-redesign ChatHeader has no testid: it is the first child of
      // the chat column, and its thread tabs are its scrolling row.
      if (el.parentElement?.matches('[data-testid="chat-interface"]') && el === el.parentElement.firstElementChild) {
        const tabs = el.querySelector('.overflow-x-auto');
        return { label: 'ChatHeader row', detail: detail ?? (tabs ? `thread tabs ${px(tabs)}` : undefined) };
      }
      const id = el.getAttribute('data-testid');
      const text = (el.textContent ?? '').trim().replace(/\s+/g, ' ').slice(0, 40);
      return { label: `Other (${id ?? (text || el.tagName.toLowerCase())})`, detail };
    };

    type Item = { el: Element | null; top: number; bottom: number };
    const flow: Item[] = [];
    const floating: Item[] = [];
    const s = scroller.getBoundingClientRect();
    let branch: Element = scroller;
    while (branch.parentElement && branch !== document.body) {
      const parent: Element = branch.parentElement;
      const b = clip(branch.getBoundingClientRect());
      for (const sib of Array.from(parent.children)) {
        if (sib === branch) continue;
        const cs = getComputedStyle(sib);
        if (cs.display === 'none' || cs.visibility === 'hidden' || Number(cs.opacity) === 0) continue;
        const rect = sib.getBoundingClientRect();
        const r = clip(rect);
        if (r.bottom - r.top < 1) continue;
        if (cs.position === 'absolute' || cs.position === 'fixed') {
          // Over the scroller and across most of it: a bar, not a 44px
          // floating button that hides a corner.
          const across = Math.min(rect.right, s.right) - Math.max(rect.left, s.left);
          if (r.top < s.bottom && r.bottom > s.top && across >= s.width / 2) floating.push({ el: sib, ...r });
          continue;
        }
        // Beside the branch (a side panel or the chat list), not above or below it.
        if (r.top < b.bottom - 1 && r.bottom > b.top + 1) continue;
        flow.push({ el: sib, ...r });
      }
      branch = parent;
    }
    const t = clip(s);
    flow.push({ el: null, ...t });
    flow.sort((x, y) => x.top - y.top);

    const bands: { label: string; kind: 'band' | 'overlay'; height: number; box: number; detail?: string }[] = [];
    let transcript = 0;
    let cursor = 0;
    flow.forEach((item, i) => {
      const end = i === flow.length - 1 ? Math.max(item.bottom, vh) : item.bottom;
      const height = Math.round(end - cursor);
      cursor = end;
      if (!item.el) { transcript = height; return; }
      bands.push({ ...describe(item.el), kind: 'band', height, box: Math.round(item.bottom - item.top) });
    });

    // Overlays: each one's part over the scroller, and their union.
    const spans = floating
      .map((f) => ({ f, top: Math.max(f.top, t.top), bottom: Math.min(f.bottom, t.bottom) }))
      .filter((x) => x.bottom > x.top)
      .sort((x, y) => x.top - y.top);
    let overlay = 0;
    let reach = t.top;
    for (const x of spans) {
      bands.push({ ...describe(x.f.el!), kind: 'overlay', height: Math.round(x.bottom - x.top), box: Math.round(x.f.bottom - x.f.top) });
      if (x.bottom > reach) { overlay += x.bottom - Math.max(x.top, reach); reach = x.bottom; }
    }
    overlay = Math.round(overlay);
    return { viewport: { width: window.innerWidth, height: vh }, bands, transcript, overlay, readable: transcript - overlay };
  }, { bandSelectors: BAND_SELECTORS, detailSelectors: DETAIL_SELECTORS });
}

/** Measure until two consecutive reads agree: the virtualizer and ResizeObservers settle over a few frames. */
export async function settledChatGeometry(page: Page): Promise<ChatGeometry> {
  let previous = '';
  let geometry: ChatGeometry | null = null;
  await expect.poll(async () => {
    geometry = await measureChatGeometry(page);
    const key = JSON.stringify(geometry);
    const stable = geometry !== null && key === previous;
    previous = key;
    return stable;
  }, { message: 'chat geometry never settled', intervals: [250] }).toBe(true);
  return geometry!;
}

export function formatChatGeometry(name: string, g: ChatGeometry): string {
  const pct = (n: number) => `${Math.round((n / g.viewport.height) * 100)}%`;
  return [
    `${name} — ${g.viewport.width}×${g.viewport.height}`,
    '| Band | Kind | Height | Box | Note |',
    '|---|---|---|---|---|',
    ...g.bands.map((b) => `| ${b.label} | ${b.kind} | ${b.height} | ${b.box} | ${b.detail ?? ''} |`),
    `| Transcript scroller | | ${g.transcript} | | ${pct(g.transcript)} |`,
    `| Covered by overlays | | ${g.overlay} | | |`,
    `| **Readable transcript** | | **${g.readable}** | | **${pct(g.readable)}** |`,
  ].join('\n');
}

export async function logChatGeometry(info: TestInfo, name: string, g: ChatGeometry) {
  console.log(`\n${formatChatGeometry(name, g)}\n`);
  await info.attach(`geometry-${g.viewport.width}x${g.viewport.height}.json`, {
    body: JSON.stringify(g, null, 2), contentType: 'application/json',
  });
}
