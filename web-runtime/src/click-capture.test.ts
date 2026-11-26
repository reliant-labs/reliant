// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// One record per real click, with the attributes the SDK's click spans
// carried, held while the SDK loads and handed on in order once it runs.
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  captureClicks,
  type ClickRecord,
  elementXPath,
  MAX_HELD_CLICKS,
} from "./click-capture.js";
import {
  asElement,
  clickEvent,
  FakeElement,
  fakeDocument,
} from "./testdata/fake-dom.js";
import { queryParamFilter } from "./url-scrub.js";

afterEach(() => {
  vi.unstubAllGlobals();
});

function page() {
  const dom = fakeDocument();
  vi.stubGlobal("document", dom.doc);
  vi.stubGlobal("window", {
    location: { href: "https://example.test/pricing?email=a%40b.test#x" },
  });
  // <html><body><header id="top"><nav><a href><span>Pricing</span></a></nav>
  // </header><main><p/><p><button><svg/></button></p></main></body></html>
  const label = new FakeElement("span");
  const link = new FakeElement("a", { href: "/pricing" }).append(label);
  const nav = new FakeElement("nav").append(link);
  const header = new FakeElement("header", { id: "top" }).append(nav);
  const icon = new FakeElement("svg");
  const button = new FakeElement("button").append(icon);
  const second = new FakeElement("p").append(button);
  const main = new FakeElement("main").append(new FakeElement("p"), second);
  const body = new FakeElement("body").append(header, main);
  new FakeElement("html").append(body);
  return { dom, label, link, icon, button, second, body };
}

describe("captureClicks", () => {
  it("records one click per real click, named by the control clicked, with the SDK's click attributes", () => {
    const { dom, label, icon } = page();
    const out: ClickRecord[] = [];
    const clicks = captureClicks(() => true)!;
    clicks.attach((c) => out.push(c));

    dom.dispatch("click", clickEvent(label, 120.5));
    dom.dispatch("click", clickEvent(icon, 300));

    expect(out).toEqual([
      {
        at: 120.5,
        attributes: {
          component: "user-interaction",
          event_type: "click",
          target_element: "A",
          target_xpath: '//*[@id="top"]/nav/a',
          // No query, no fragment: scrubbed as it is read (url-scrub.ts).
          "http.url": "https://example.test/pricing",
        },
      },
      {
        at: 300,
        attributes: {
          component: "user-interaction",
          event_type: "click",
          target_element: "BUTTON",
          target_xpath: "/html/body/main/p[2]/button",
          "http.url": "https://example.test/pricing",
        },
      },
    ]);
  });

  it("names plain content when no control encloses it", () => {
    const { dom, second } = page();
    const out: ClickRecord[] = [];
    captureClicks(() => true)!.attach((c) => out.push(c));
    dom.dispatch("click", clickEvent(second, 1));
    expect(out.map((c) => c.attributes.target_element)).toEqual(["P"]);
  });

  it("ignores synthetic clicks and clicks on a disabled control", () => {
    const { dom, button } = page();
    const out: ClickRecord[] = [];
    captureClicks(() => true)!.attach((c) => out.push(c));
    dom.dispatch("click", clickEvent(button, 1, false));
    const disabled = new FakeElement("button", { disabled: "" });
    dom.dispatch("click", clickEvent(disabled, 2));
    expect(out).toEqual([]);
  });

  it("holds the clicks made before the SDK runs, then hands them on in order with their own times", () => {
    const { dom, link, button } = page();
    const out: ClickRecord[] = [];
    const clicks = captureClicks(() => true)!;
    dom.dispatch("click", clickEvent(link, 10));
    dom.dispatch("click", clickEvent(button, 20));
    expect(out).toEqual([]);

    clicks.attach((c) => out.push(c));
    dom.dispatch("click", clickEvent(link, 900));
    expect(out.map((c) => [c.at, c.attributes.target_element])).toEqual([
      [10, "A"],
      [20, "BUTTON"],
      [900, "A"],
    ]);
  });

  it("holds a click's page URL already scrubbed, keeping only the query parameters allowed", () => {
    const { dom, button } = page();
    vi.stubGlobal("window", {
      location: {
        href: "https://example.test/pricing?utm_source=ad&email=a%40b.test&token=s3cret#code=x",
      },
    });
    const clicks = captureClicks(() => true, queryParamFilter(["utm_*"]))!;
    dom.dispatch("click", clickEvent(button, 1));
    const out: ClickRecord[] = [];
    clicks.attach((c) => out.push(c));
    expect(out.map((c) => c.attributes["http.url"])).toEqual([
      "https://example.test/pricing?utm_source=ad",
    ]);
  });

  it(`holds at most ${MAX_HELD_CLICKS} clicks`, () => {
    const { dom, button } = page();
    const clicks = captureClicks(() => true)!;
    for (let i = 0; i < MAX_HELD_CLICKS + 10; i++) {
      dom.dispatch("click", clickEvent(button, i));
    }
    const out: ClickRecord[] = [];
    clicks.attach((c) => out.push(c));
    expect(out).toHaveLength(MAX_HELD_CLICKS);
    expect(out.at(-1)!.at).toBe(MAX_HELD_CLICKS - 1);
  });

  it("records nothing while disabled, including what it held before", () => {
    const { dom, button } = page();
    let on = true;
    const out: ClickRecord[] = [];
    const clicks = captureClicks(() => on)!;
    dom.dispatch("click", clickEvent(button, 1));
    on = false;
    dom.dispatch("click", clickEvent(button, 2));
    clicks.attach((c) => out.push(c));
    dom.dispatch("click", clickEvent(button, 3));
    expect(out).toEqual([]);
  });

  it("stops listening, and drops what it held, on stop()", () => {
    const { dom, button } = page();
    const out: ClickRecord[] = [];
    const clicks = captureClicks(() => true)!;
    expect(dom.listening("click")).toBe(1);
    dom.dispatch("click", clickEvent(button, 1));
    clicks.stop();
    expect(dom.listening("click")).toBe(0);
    clicks.attach((c) => out.push(c));
    expect(out).toEqual([]);
  });

  it("never throws into the page, whatever the emitter does", () => {
    const { dom, button } = page();
    captureClicks(() => true)!.attach(() => {
      throw new Error("exporter broke");
    });
    expect(() => dom.dispatch("click", clickEvent(button, 1))).not.toThrow();
  });

  it("is null without a DOM", () => {
    expect(captureClicks(() => true)).toBeNull();
  });
});

describe("elementXPath", () => {
  it("spells paths as the SDK's spans did: from the nearest id, positions only among same-named siblings", () => {
    const { label, button, body } = page();
    expect(elementXPath(asElement(label))).toBe('//*[@id="top"]/nav/a/span');
    expect(elementXPath(asElement(button))).toBe("/html/body/main/p[2]/button");
    expect(elementXPath(asElement(body))).toBe("/html/body");
  });
});
