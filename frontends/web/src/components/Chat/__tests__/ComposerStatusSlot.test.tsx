/**
 * The status slot above the composer (mobile-chat-redesign.md §6): one line on
 * a compact column, highest priority first with a `+N`; up to three at medium
 * width; every band in full on a wide one. Approvals are never collapsed.
 *
 * The banners here are stand-ins that report through the same hook the real
 * ones use, so the slot's behaviour is tested apart from each banner's data.
 */

import { useState } from "react";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ComposeModeProvider } from "../../../lib/composeMode";
import {
  ComposerStatusSlot,
  RecoveryBanner,
  StatusSlotItem,
  statusSlotLayoutForWidth,
  useStatusSlotSummary,
  type StatusSlotItemId,
  type StatusSlotLayout,
} from "../ComposerStatusSlot";

function FakeBanner({ id, label, onAction }: { id: string; label: string; onAction?: () => void }) {
  useStatusSlotSummary({ label });
  return (
    <div data-testid={`banner-${id}`}>
      {label}
      <button type="button" onClick={onAction}>
        Act on {id}
      </button>
    </div>
  );
}

/** A banner with state of its own, to prove collapsing never remounts it. */
function CountingBanner() {
  const [count, setCount] = useState(0);
  useStatusSlotSummary({ label: `Clicked ${count}` });
  return (
    <button type="button" data-testid="counter" onClick={() => setCount((c) => c + 1)}>
      count {count}
    </button>
  );
}

const LABELS: Record<StatusSlotItemId, string> = {
  recovery: "Session interrupted",
  "no-machine": "No machine",
  wake: "Waking mac…",
  oom: "Machine ran out of memory",
  disk: "Disk almost full",
  resume: "mac is suspended",
  queued: "2 queued",
  background: "3 running",
};

const UPPER: StatusSlotItemId[] = ["queued", "background"];

function renderSlot({
  active,
  layout,
  composeMode = false,
  approvals = <div data-testid="approvals">Approve rm -rf?</div>,
  onAction,
}: {
  active: StatusSlotItemId[];
  layout: StatusSlotLayout;
  composeMode?: boolean;
  approvals?: React.ReactNode;
  onAction?: (id: StatusSlotItemId) => void;
}) {
  const item = (id: StatusSlotItemId) => (
    <StatusSlotItem key={id} id={id}>
      <FakeBanner id={id} label={LABELS[id]} onAction={() => onAction?.(id)} />
    </StatusSlotItem>
  );
  return render(
    <ComposeModeProvider keyboardOpen={composeMode}>
      <div data-testid="column">
        <ComposerStatusSlot
          layout={layout}
          upper={<>{active.filter((id) => UPPER.includes(id)).map(item)}</>}
          approvals={approvals}
          lower={<>{active.filter((id) => !UPPER.includes(id)).map(item)}</>}
        />
      </div>
    </ComposeModeProvider>,
  );
}

const lines = () => screen.queryAllByRole("button", { name: /Show status$/ });

describe("statusSlotLayoutForWidth", () => {
  it("maps the column width to compact, medium and wide (§4.1)", () => {
    expect(statusSlotLayoutForWidth(390)).toBe("compact");
    expect(statusSlotLayoutForWidth(599)).toBe("compact");
    expect(statusSlotLayoutForWidth(600)).toBe("medium");
    expect(statusSlotLayoutForWidth(839)).toBe("medium");
    expect(statusSlotLayoutForWidth(840)).toBe("wide");
    // Not measured: render as it always has, not as a phone.
    expect(statusSlotLayoutForWidth(0)).toBe("wide");
  });
});

describe("ComposerStatusSlot, compact", () => {
  it("shows only the highest-priority item, with a count of the rest", () => {
    renderSlot({ active: ["queued", "oom", "recovery", "disk"], layout: "compact" });
    const shown = lines();
    expect(shown).toHaveLength(1);
    expect(shown[0]).toHaveTextContent("Session interrupted");
    expect(screen.getByTestId("status-slot-more")).toHaveTextContent("+3");
    // The banners themselves are mounted but out of sight.
    expect(screen.getByTestId("banner-oom")).not.toBeVisible();
  });

  it("follows the §6 order: no machine outranks OOM, disk outranks resume", () => {
    const { unmount } = renderSlot({ active: ["oom", "no-machine"], layout: "compact" });
    expect(lines()[0]).toHaveTextContent("No machine");
    unmount();
    renderSlot({ active: ["resume", "disk", "background"], layout: "compact" });
    expect(lines()[0]).toHaveTextContent("Disk almost full");
  });

  it("puts queued and running side by side as two chips when nothing outranks them", () => {
    renderSlot({ active: ["background", "queued"], layout: "compact" });
    const shown = lines();
    expect(shown.map((b) => b.textContent)).toEqual(["2 queued", "3 running"]);
    expect(screen.queryByTestId("status-slot-more")).toBeNull();
  });

  it("collapses the ambient pair into the count once something outranks them", () => {
    renderSlot({ active: ["background", "queued", "wake"], layout: "compact" });
    const shown = lines();
    expect(shown).toHaveLength(1);
    expect(shown[0]).toHaveTextContent("Waking mac…");
    expect(screen.getByTestId("status-slot-more")).toHaveTextContent("+2");
  });

  it("renders nothing when nothing is active", () => {
    renderSlot({ active: [], layout: "compact" });
    expect(screen.queryByTestId("composer-status-slot")).toBeNull();
  });

  it("gives every line a 44px target", () => {
    renderSlot({ active: ["oom"], layout: "compact" });
    expect(lines()[0].className).toContain("min-h-11");
  });

  it("opens a sheet with every active item in full, by priority, and their actions work", () => {
    const onAction = vi.fn();
    renderSlot({ active: ["queued", "disk", "recovery"], layout: "compact", onAction });
    fireEvent.click(lines()[0]);

    const sheet = screen.getByTestId("status-slot-sheet");
    expect(screen.getByRole("dialog", { name: "Status" })).toBeInTheDocument();
    for (const id of ["queued", "disk", "recovery"] as const) {
      expect(within(sheet).getByTestId(`banner-${id}`)).toBeVisible();
    }
    // Two sections are moved in; the flex order puts them in priority order.
    const order = (id: string) =>
      Number((sheet.querySelector(`[data-status-slot-item="${id}"]`) as HTMLElement).style.order);
    expect(order("recovery")).toBeLessThan(order("disk"));
    expect(order("disk")).toBeLessThan(order("queued"));

    fireEvent.click(within(sheet).getByRole("button", { name: "Act on queued" }));
    expect(onAction).toHaveBeenCalledWith("queued");
  });

  it("keeps each banner mounted across the sheet opening and closing", () => {
    render(
      <ComposerStatusSlot
        layout="compact"
        upper={
          <StatusSlotItem id="queued">
            <CountingBanner />
          </StatusSlotItem>
        }
        approvals={null}
        lower={null}
      />,
    );
    fireEvent.click(lines()[0]);
    fireEvent.click(screen.getByTestId("counter"));
    fireEvent.click(screen.getByTestId("counter"));
    fireEvent.click(screen.getByRole("button", { name: /close/i }));
    expect(screen.queryByRole("dialog")).toBeNull();
    // The line reads the state the banner kept.
    expect(lines()[0]).toHaveTextContent("Clicked 2");
    fireEvent.click(lines()[0]);
    expect(screen.getByTestId("counter")).toHaveTextContent("count 2");
  });

  it("closes the sheet when the last item goes away", () => {
    function Toggle() {
      const [on, setOn] = useState(true);
      return (
        <>
          <button type="button" data-testid="drain" onClick={() => setOn(false)} />
          <ComposerStatusSlot
            layout="compact"
            upper={on ? <StatusSlotItem id="queued"><FakeBanner id="queued" label="1 queued" /></StatusSlotItem> : null}
            approvals={null}
            lower={null}
          />
        </>
      );
    }
    render(<Toggle />);
    fireEvent.click(lines()[0]);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    act(() => screen.getByTestId("drain").click());
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("never collapses pending approvals", () => {
    renderSlot({ active: ["recovery", "oom", "queued"], layout: "compact" });
    expect(screen.getByTestId("approvals")).toBeVisible();
    // Above the composer, below the slot's line.
    const line = screen.getByTestId("composer-status-slot");
    expect(line.compareDocumentPosition(screen.getByTestId("approvals")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});

describe("ComposerStatusSlot, medium", () => {
  it("shows up to three lines, the last carrying the count of the rest", () => {
    renderSlot({ active: ["background", "queued", "oom", "wake"], layout: "medium" });
    const shown = lines();
    expect(shown.map((b) => b.getAttribute("data-testid"))).toEqual([
      "status-slot-line-wake",
      "status-slot-line-oom",
      "status-slot-line-queued",
    ]);
    expect(within(shown[2]).getByTestId("status-slot-more")).toHaveTextContent("+1");
  });

  it("caps the slot at one line in compose mode", () => {
    renderSlot({ active: ["background", "queued", "oom"], layout: "medium", composeMode: true });
    const shown = lines();
    expect(shown).toHaveLength(1);
    expect(shown[0]).toHaveTextContent("Machine ran out of memory");
    expect(screen.getByTestId("status-slot-more")).toHaveTextContent("+2");
    // Approvals are an interruption, not status: still in full.
    expect(screen.getByTestId("approvals")).toBeVisible();
  });
});

describe("ComposerStatusSlot, wide", () => {
  it("renders every band in full and in the column order it always had", () => {
    renderSlot({ active: ["queued", "background", "oom", "recovery"], layout: "wide" });
    expect(lines()).toHaveLength(0);
    expect(screen.queryByTestId("composer-status-slot")).toBeNull();

    const column = screen.getByTestId("column");
    const order = ["banner-queued", "banner-background", "approvals", "banner-oom", "banner-recovery"].map((id) =>
      within(column).getByTestId(id),
    );
    for (const el of order) expect(el).toBeVisible();
    for (let i = 1; i < order.length; i++) {
      expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }
    expect(screen.getByTestId("status-slot-holder")).toBeEmptyDOMElement();
  });

  it("collapses to one line when compose mode starts, whatever the width", () => {
    renderSlot({ active: ["queued", "oom"], layout: "wide", composeMode: true });
    expect(lines()).toHaveLength(1);
    expect(screen.getByTestId("banner-oom")).not.toBeVisible();
  });
});

describe("RecoveryBanner", () => {
  it("offers Restart, and reports itself as the top slot item", () => {
    const onRestart = vi.fn();
    render(
      <ComposerStatusSlot
        layout="compact"
        upper={null}
        approvals={null}
        lower={
          <StatusSlotItem id="recovery">
            <RecoveryBanner onRestart={onRestart} />
          </StatusSlotItem>
        }
      />,
    );
    expect(lines()[0]).toHaveTextContent("Session interrupted");
    fireEvent.click(lines()[0]);
    fireEvent.click(screen.getByRole("button", { name: "Restart Conversation" }));
    expect(onRestart).toHaveBeenCalled();
  });

  it("uses status tokens, not raw palette classes", () => {
    render(<RecoveryBanner />);
    const banner = screen.getByTestId("recovery-banner");
    expect(banner.className).toContain("text-warning-ink");
    expect(banner.outerHTML).not.toMatch(/amber-/);
  });
});
