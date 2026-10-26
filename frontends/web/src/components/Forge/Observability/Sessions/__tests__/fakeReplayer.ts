// Copyright (c) 2025 Reliant Labs

/**
 * A stand-in for rrweb's Replayer, in a file that imports nothing from the
 * app so a hoisted vi.mock factory can load it:
 *
 *   vi.mock("@rrweb/replay", async () => ({ Replayer: (await import("./fakeReplayer")).FakeReplayer }));
 *
 * jsdom cannot rebuild a recorded DOM into an iframe, and what the player
 * owes rrweb is a contract — which events it hands over, in what order, which
 * options it pins, where it seeks — so the fake records exactly that. Like
 * the real one, it refuses fewer than two events and creates its iframe
 * sandboxed, adding `allow-scripts` only for UNSAFE_replayCanvas.
 */

interface Event {
  type: number;
  timestamp: number;
}

interface SnapshotNode {
  type: number;
  tagName?: string;
  textContent?: string;
  attributes?: Record<string, unknown>;
  childNodes?: SnapshotNode[];
}

export class FakeReplayer {
  static instances: FakeReplayer[] = [];
  events: Event[];
  config: Record<string, unknown>;
  wrapper: HTMLDivElement;
  iframe: HTMLIFrameElement;
  calls: string[] = [];
  /** Remote URLs the browser would fetch while building the recording. */
  requested: string[] = [];
  /** Remote URLs the document's CSP meta stopped. */
  blocked: string[] = [];
  current = 0;
  private handlers = new Map<string, (payload?: unknown) => void>();

  constructor(events: Event[], config: Record<string, unknown>) {
    if (events.length < 2) throw new Error("Replayer need at least 2 events.");
    this.events = [...events];
    this.config = { ...config };
    this.wrapper = document.createElement("div");
    this.iframe = document.createElement("iframe");
    this.iframe.setAttribute("sandbox", config.UNSAFE_replayCanvas ? "allow-same-origin allow-scripts" : "allow-same-origin");
    this.wrapper.appendChild(this.iframe);
    (config.root as HTMLElement).appendChild(this.wrapper);
    FakeReplayer.instances.push(this);
    // Like rrweb: the first snapshot is built after the constructor returns.
    setTimeout(() => this.rebuildFullSnapshots(), 0);
  }

  /** Build every full snapshot recorded so far into the iframe, as rrweb does. */
  rebuildFullSnapshots() {
    for (const event of this.events as { type: number; data?: { node?: SnapshotNode } }[]) {
      if (event.type === 2 && event.data?.node) this.build(event.data.node);
    }
    this.emit("fullsnapshot-rebuilded");
  }

  private build(node: SnapshotNode) {
    const attrs = node.attributes ?? {};
    for (const [name, value] of Object.entries(attrs)) {
      if (typeof value !== "string") continue;
      if ((node.tagName === "img" && name === "src") || (node.tagName === "link" && name === "href")) this.fetchResource(node.tagName === "img" ? "img-src" : "style-src", value);
      for (const m of value.matchAll(/url\(([^)]+)\)/g)) this.fetchResource("img-src", m[1]);
    }
    for (const child of node.childNodes ?? []) {
      if (child.type === 3 && node.tagName === "style") for (const m of (child.textContent ?? "").matchAll(/url\(([^)]+)\)/g)) this.fetchResource("img-src", m[1]);
      this.build(child);
    }
  }

  private fetchResource(directive: "img-src" | "style-src", url: string) {
    if (!/^https?:\/\//.test(url)) return;
    const policies = Array.from(this.iframe.contentDocument?.querySelectorAll('meta[http-equiv="Content-Security-Policy"]') ?? []).map((m) => m.getAttribute("content") ?? "");
    const allowed = policies.every((policy) => {
      const rules = new Map(policy.split(";").map((d) => d.trim().split(/\s+/)).filter((d) => d[0]).map(([name, ...sources]) => [name, sources] as const));
      const sources = rules.get(directive) ?? rules.get("default-src");
      return !sources || sources.some((src) => src === "*" || src === "https:" || src === "http:");
    });
    (allowed ? this.requested : this.blocked).push(url);
  }

  static reset() {
    FakeReplayer.instances = [];
  }

  static last(): FakeReplayer {
    const last = FakeReplayer.instances[FakeReplayer.instances.length - 1];
    if (!last) throw new Error("no Replayer was constructed");
    return last;
  }

  play(offset?: number) {
    this.calls.push(`play:${offset ?? ""}`);
    if (offset !== undefined) this.current = offset;
  }

  pause(offset?: number) {
    this.calls.push(`pause:${offset ?? ""}`);
    if (offset !== undefined) this.current = offset;
  }

  addEvent(event: Event) {
    this.events.push(event);
  }

  setConfig(config: Record<string, unknown>) {
    this.config = { ...this.config, ...config };
    this.calls.push(`config:${JSON.stringify(config)}`);
  }

  getCurrentTime() {
    return this.current;
  }

  getMetaData() {
    const startTime = this.events[0].timestamp;
    const endTime = this.events[this.events.length - 1].timestamp;
    return { startTime, endTime, totalTime: endTime - startTime };
  }

  on(event: string, handler: (payload?: unknown) => void) {
    this.handlers.set(event, handler);
    return this;
  }

  emit(event: string, payload?: unknown) {
    this.handlers.get(event)?.(payload);
  }

  destroy() {
    this.calls.push("destroy");
    this.wrapper.remove();
  }
}
