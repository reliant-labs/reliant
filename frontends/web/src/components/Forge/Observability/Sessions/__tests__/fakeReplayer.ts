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

export class FakeReplayer {
  static instances: FakeReplayer[] = [];
  events: Event[];
  config: Record<string, unknown>;
  wrapper: HTMLDivElement;
  iframe: HTMLIFrameElement;
  calls: string[] = [];
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
