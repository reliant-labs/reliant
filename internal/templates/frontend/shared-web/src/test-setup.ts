// yours: scaffolded once, never touched again — forge will not overwrite this file
// Vitest setup: runs before every test file (`test.setupFiles` in the vitest config).
//
// jsdom implements no navigation. window.location.assign("/x"), .replace and
// .reload change nothing there, and each printed "Error: Not implemented:
// navigation" with a stack trace, so a green run looked broken. Here they are
// vi.fn() stubs that record the call and do nothing else, so a test can assert
// where the code sent the browser:
//
//   expect(window.location.assign).toHaveBeenCalledWith("/");
//
// Setting location.href (or location itself) to another page goes through the
// assign stub too. A same-document hash change (#section) still reaches jsdom,
// which does implement those, and every other property reads the real Location.
// The stubs' calls are cleared before each test.
import { beforeEach, vi } from "vitest";

const real = window.location;

// hashOnly reports whether url is this document with only its hash changed:
// the one navigation jsdom performs.
function hashOnly(url: string | URL): boolean {
  const next = new URL(String(url), real.href);
  return next.hash !== "" && next.href.split("#")[0] === real.href.split("#")[0];
}

const assign = vi.fn((url: string | URL) => {
  if (hashOnly(url)) real.assign(url);
});
const replace = vi.fn((url: string | URL) => {
  if (hashOnly(url)) real.replace(url);
});
const reload = vi.fn(() => {});

// The proxy's target is an empty object, not the real Location: a Location's
// methods are fixed (non-configurable) properties, and a proxy may not answer
// anything but the real value for those.
const stubbed = new Proxy({} as Location, {
  get(_, prop) {
    if (prop === "assign") return assign;
    if (prop === "replace") return replace;
    if (prop === "reload") return reload;
    const value: unknown = Reflect.get(real, prop, real);
    return typeof value === "function" ? (value as () => unknown).bind(real) : value;
  },
  set(_, prop, value) {
    if (prop === "href" && !hashOnly(String(value))) {
      assign(String(value));
      return true;
    }
    return Reflect.set(real, prop, value, real);
  },
  has: (_, prop) => prop in real,
});

// Vitest normally exposes location as a configurable global accessor. If an
// environment exposes jsdom's fixed property instead, wrap window rather than
// trying to redefine that property (or Location's equally fixed methods).
const locationDescriptor = {
  configurable: true,
  get: () => stubbed,
  set: (url: string) => {
    stubbed.href = url;
  },
};
if (Object.getOwnPropertyDescriptor(window, "location")?.configurable ?? true) {
  Object.defineProperty(window, "location", locationDescriptor);
} else {
  const realWindow = window;
  const wrapper: Window & typeof globalThis = new Proxy({} as Window & typeof globalThis, {
    get(_, prop) {
      if (prop === "location") return stubbed;
      if (prop === "window" || prop === "self") return wrapper;
      const value: unknown = Reflect.get(realWindow, prop, realWindow);
      return typeof value === "function" ? value.bind(realWindow) : value;
    },
    set(_, prop, value) {
      if (prop === "location") {
        stubbed.href = String(value);
        return true;
      }
      return Reflect.set(realWindow, prop, value, realWindow);
    },
    has: (_, prop) => prop in realWindow,
  });
  vi.stubGlobal("window", wrapper);
  vi.stubGlobal("self", wrapper);
  Object.defineProperty(globalThis, "location", locationDescriptor);
}

beforeEach(() => {
  assign.mockClear();
  replace.mockClear();
  reload.mockClear();
});
