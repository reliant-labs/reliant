// A just-big-enough DOM for the click tests: elements with attributes, a
// parent chain, siblings, closest() over simple selectors, and a document
// whose listeners the test drives. Not part of the package.

type Listener = (event: unknown) => void;

export class FakeElement {
  readonly nodeType = 1;
  readonly localName: string;
  readonly tagName: string;
  parentElement: FakeElement | null = null;
  readonly childNodes: FakeElement[] = [];
  private readonly attrs: Record<string, string>;

  constructor(tag: string, attrs: Record<string, string> = {}) {
    this.localName = tag.toLowerCase();
    this.tagName = tag.toUpperCase();
    this.attrs = attrs;
  }

  get parentNode(): FakeElement | null {
    return this.parentElement;
  }

  append(...children: FakeElement[]): this {
    for (const child of children) {
      child.parentElement = this;
      this.childNodes.push(child);
    }
    return this;
  }

  getAttribute(name: string): string | null {
    return this.attrs[name] ?? null;
  }

  hasAttribute(name: string): boolean {
    return name in this.attrs;
  }

  closest(selector: string): FakeElement | null {
    const parts = selector.split(",").map((s) => s.trim());
    for (
      let el: FakeElement | null = this;
      el;
      el = el.parentElement
    ) {
      if (parts.some((part) => matches(el!, part))) {
        return el;
      }
    }
    return null;
  }
}

// tag, [attr], [attr=value], tag[attr]: the shapes the runtime's selectors use.
function matches(el: FakeElement, selector: string): boolean {
  const m = /^([a-z]*)(?:\[([a-z-]+)(?:=([a-z-]+))?\])?$/.exec(selector);
  if (!m) {
    throw new Error(`fake-dom: unsupported selector ${selector}`);
  }
  const [, tag, attr, value] = m;
  if (tag && el.localName !== tag) {
    return false;
  }
  if (attr) {
    if (!el.hasAttribute(attr)) {
      return false;
    }
    if (value !== undefined && el.getAttribute(attr) !== value) {
      return false;
    }
  }
  return true;
}

export function fakeDocument(extra: Record<string, unknown> = {}) {
  const listeners = new Map<string, Listener[]>();
  const doc = {
    visibilityState: "visible" as DocumentVisibilityState,
    addEventListener(type: string, fn: Listener) {
      listeners.set(type, [...(listeners.get(type) ?? []), fn]);
    },
    removeEventListener(type: string, fn: Listener) {
      listeners.set(
        type,
        (listeners.get(type) ?? []).filter((l) => l !== fn),
      );
    },
    ...extra,
  };
  return {
    doc,
    listening: (type: string) => (listeners.get(type) ?? []).length,
    dispatch(type: string, event: unknown) {
      for (const fn of [...(listeners.get(type) ?? [])]) fn(event);
    },
  };
}

/** A click as the browser dispatches it to the document. */
export function clickEvent(
  target: FakeElement,
  timeStamp: number,
  isTrusted = true,
) {
  return { type: "click", target, timeStamp, isTrusted };
}

/** A fake element where the runtime's types want a real one. */
export function asElement(element: FakeElement): Element {
  return element as unknown as Element;
}
