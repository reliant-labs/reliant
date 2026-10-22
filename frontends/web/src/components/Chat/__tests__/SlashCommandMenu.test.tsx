import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, act } from "@testing-library/react";
import { createRef, useRef } from "react";
import {
  SlashCommandMenu,
  type SlashCommand,
  type SlashCommandMenuHandle,
} from "../SlashCommandMenu";

const commands: SlashCommand[] = [
  {
    id: "new-chat",
    title: "New Chat",
    description: "Start a new conversation",
    keywords: ["create"],
    shortcut: "⌘T",
    action: vi.fn(),
  },
  {
    id: "search-chats",
    title: "Search Chats",
    description: "Search history",
    keywords: ["find"],
    shortcut: "⌘F",
    action: vi.fn(),
  },
  {
    id: "settings",
    title: "Settings",
    description: "Open settings",
    // No shortcut — composer-only actions have none, and the row must still
    // render correctly.
    action: vi.fn(),
  },
];

function Harness({
  value,
  onConsume = vi.fn(),
  menuRef,
}: {
  value: string;
  onConsume?: () => void;
  menuRef?: React.Ref<SlashCommandMenuHandle>;
}) {
  const anchorRef = useRef<HTMLDivElement>(null);
  return (
    <>
      <div ref={anchorRef}>composer</div>
      <SlashCommandMenu
        ref={menuRef}
        value={value}
        commands={commands}
        anchorRef={anchorRef}
        onConsume={onConsume}
      />
    </>
  );
}

/** Build a React-like keyboard event for the imperative handler. */
function keyEvent(key: string) {
  return {
    key,
    preventDefault: vi.fn(),
    stopPropagation: vi.fn(),
  } as unknown as React.KeyboardEvent;
}

describe("SlashCommandMenu", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("trigger rules", () => {
    it("opens on a leading slash", () => {
      render(<Harness value="/" />);
      expect(screen.getByRole("listbox")).toBeTruthy();
      expect(screen.getAllByRole("option")).toHaveLength(3);
    });

    it("stays closed for empty input", () => {
      render(<Harness value="" />);
      expect(screen.queryByRole("listbox")).toBeNull();
    });

    it("ignores a slash that is not the first character", () => {
      // Typing a path or URL must not summon the menu.
      render(<Harness value="src/lib/foo" />);
      expect(screen.queryByRole("listbox")).toBeNull();
    });

    it("closes once the user types a space", () => {
      render(<Harness value="/new chat please" />);
      expect(screen.queryByRole("listbox")).toBeNull();
    });

    it("filters as the user types", () => {
      render(<Harness value="/sea" />);
      const options = screen.getAllByRole("option");
      expect(options).toHaveLength(1);
      expect(options[0].textContent).toContain("Search Chats");
    });

    it("matches on keywords, not just titles", () => {
      render(<Harness value="/find" />);
      expect(screen.getAllByRole("option")[0].textContent).toContain(
        "Search Chats",
      );
    });

    it("hides entirely when nothing matches", () => {
      // An empty popup would block the composer for no reason.
      render(<Harness value="/zzzz" />);
      expect(screen.queryByRole("listbox")).toBeNull();
    });
  });

  describe("keyboard routing", () => {
    it("reports whether it consumed the key", () => {
      const ref = createRef<SlashCommandMenuHandle>();
      render(<Harness value="/" menuRef={ref} />);

      expect(ref.current?.handleKeyDown(keyEvent("ArrowDown"))).toBe(true);
      expect(ref.current?.handleKeyDown(keyEvent("Enter"))).toBe(true);
      // Ordinary typing must fall through to the composer.
      expect(ref.current?.handleKeyDown(keyEvent("a"))).toBe(false);
    });

    it("consumes nothing while closed, so Enter still sends", () => {
      const ref = createRef<SlashCommandMenuHandle>();
      render(<Harness value="hello" menuRef={ref} />);

      expect(ref.current?.isOpen()).toBe(false);
      expect(ref.current?.handleKeyDown(keyEvent("Enter"))).toBe(false);
      expect(ref.current?.handleKeyDown(keyEvent("Escape"))).toBe(false);
    });

    it("runs the highlighted command on Enter", () => {
      const ref = createRef<SlashCommandMenuHandle>();
      render(<Harness value="/" menuRef={ref} />);

      ref.current?.handleKeyDown(keyEvent("Enter"));

      expect(commands[0].action).toHaveBeenCalledOnce();
    });

    it("moves the highlight before running", () => {
      const ref = createRef<SlashCommandMenuHandle>();
      render(<Harness value="/" menuRef={ref} />);

      ref.current?.handleKeyDown(keyEvent("ArrowDown"));
      ref.current?.handleKeyDown(keyEvent("Enter"));

      expect(commands[1].action).toHaveBeenCalledOnce();
      expect(commands[0].action).not.toHaveBeenCalled();
    });

    it("wraps around when arrowing past the end", () => {
      const ref = createRef<SlashCommandMenuHandle>();
      render(<Harness value="/" menuRef={ref} />);

      // Up from the first entry lands on the last.
      ref.current?.handleKeyDown(keyEvent("ArrowUp"));
      ref.current?.handleKeyDown(keyEvent("Enter"));

      expect(commands[2].action).toHaveBeenCalledOnce();
    });

    it("runs on Tab as well as Enter", () => {
      const ref = createRef<SlashCommandMenuHandle>();
      render(<Harness value="/" menuRef={ref} />);

      ref.current?.handleKeyDown(keyEvent("Tab"));

      expect(commands[0].action).toHaveBeenCalledOnce();
    });

    it("clears the composer before running the action", () => {
      const onConsume = vi.fn();
      const ref = createRef<SlashCommandMenuHandle>();
      render(<Harness value="/" onConsume={onConsume} menuRef={ref} />);

      ref.current?.handleKeyDown(keyEvent("Enter"));

      // The "/..." text must not survive as a sent message.
      expect(onConsume).toHaveBeenCalledOnce();
    });

    it("dismisses on Escape without running anything", () => {
      const ref = createRef<SlashCommandMenuHandle>();
      const { rerender } = render(<Harness value="/" menuRef={ref} />);

      ref.current?.handleKeyDown(keyEvent("Escape"));
      rerender(<Harness value="/" menuRef={ref} />);

      expect(commands[0].action).not.toHaveBeenCalled();
      expect(ref.current?.isOpen()).toBe(false);
    });
  });

  it("marks itself so the shortcut dispatcher can detect the context", () => {
    render(<Harness value="/" />);
    expect(
      document.querySelector('[data-slash-menu-open="true"]'),
    ).not.toBeNull();
  });

  describe("shortcut hints", () => {
    it("shows the keyboard shortcut beside commands that have one", () => {
      render(<Harness value="/" />);

      // The menu should teach the shortcut, not replace it.
      expect(screen.getByText("⌘T")).toBeTruthy();
      expect(screen.getByText("⌘F")).toBeTruthy();
    });

    it("renders commands without a shortcut just fine", () => {
      render(<Harness value="/settings" />);

      const options = screen.getAllByRole("option");
      expect(options).toHaveLength(1);
      expect(options[0].textContent).toContain("Settings");
      expect(options[0].querySelector("kbd")).toBeNull();
    });
  });

  describe("explicit open (Cmd+/)", () => {
    it("opens with the full list even when the composer has text", () => {
      const ref = createRef<SlashCommandMenuHandle>();
      const { rerender } = render(
        <Harness value="here is a real message" menuRef={ref} />,
      );

      expect(ref.current?.isOpen()).toBe(false);

      act(() => ref.current?.open());
      rerender(<Harness value="here is a real message" menuRef={ref} />);

      expect(ref.current?.isOpen()).toBe(true);
      expect(screen.getAllByRole("option")).toHaveLength(3);
    });

    it("does not clear the composer when run from an explicit open", () => {
      // The text is a message in progress, not a command query — eating it
      // would destroy the user's work.
      const onConsume = vi.fn();
      const ref = createRef<SlashCommandMenuHandle>();
      const { rerender } = render(
        <Harness value="a real message" onConsume={onConsume} menuRef={ref} />,
      );

      act(() => ref.current?.open());
      rerender(
        <Harness value="a real message" onConsume={onConsume} menuRef={ref} />,
      );
      act(() => {
        ref.current?.handleKeyDown(keyEvent("Enter"));
      });

      expect(commands[0].action).toHaveBeenCalledOnce();
      expect(onConsume).not.toHaveBeenCalled();
    });

    it("closes on dismiss()", () => {
      const ref = createRef<SlashCommandMenuHandle>();
      const { rerender } = render(<Harness value="hello" menuRef={ref} />);

      act(() => ref.current?.open());
      rerender(<Harness value="hello" menuRef={ref} />);
      expect(ref.current?.isOpen()).toBe(true);

      act(() => ref.current?.dismiss());
      rerender(<Harness value="hello" menuRef={ref} />);
      expect(ref.current?.isOpen()).toBe(false);
    });

    it("can be reopened after being dismissed", () => {
      const ref = createRef<SlashCommandMenuHandle>();
      const { rerender } = render(<Harness value="hello" menuRef={ref} />);

      act(() => ref.current?.open());
      rerender(<Harness value="hello" menuRef={ref} />);
      act(() => ref.current?.dismiss());
      rerender(<Harness value="hello" menuRef={ref} />);
      act(() => ref.current?.open());
      rerender(<Harness value="hello" menuRef={ref} />);

      expect(ref.current?.isOpen()).toBe(true);
    });
  });
});
