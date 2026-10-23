import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { StatusBadge } from "@/components/status-badge";

// Stand-in for a protobuf-es v2 (`protoc-gen-es --target=ts`) enum: a NUMERIC
// TypeScript enum whose runtime object carries BOTH the forward (name → number)
// and the reverse (number → name) entries. protobuf-es strips the shared
// `ORDER_STATUS_` prefix from member names when every member has it.
enum OrderStatus {
  UNSPECIFIED = 0,
  PENDING = 1,
  PAYMENT_CAPTURED = 2,
}

// The other shape protobuf-es emits. It only strips the prefix derived from the
// ENUM's own name, so a nested `Order.Status` whose values carry the
// message-qualified `ORDER_STATUS_` keeps its full member names at runtime.
enum NestedOrderStatus {
  ORDER_STATUS_UNSPECIFIED = 0,
  ORDER_STATUS_IN_TRANSIT = 1,
}

afterEach(cleanup);

describe("StatusBadge", () => {
  // The component's first documented call, verbatim — and the regression it
  // exists to prevent: `order.status` is the NUMBER 1 at runtime, and a badge
  // reading "1" shipped to production.
  it("renders a runtime enum NUMBER as its label, never its digit", () => {
    const order = { status: OrderStatus.PENDING };
    render(<StatusBadge value={order.status} enumType={OrderStatus} />);

    expect(screen.getByText("Pending")).toBeTruthy();
    expect(screen.queryByText("1")).toBeNull();
  });

  // The variant lookup runs the same resolution, so the color can't disagree
  // with the label: the ordinal "1" used to hash to neutral grey.
  it("colors a runtime enum NUMBER from the resolved token", () => {
    const { container } = render(
      <StatusBadge value={OrderStatus.PENDING} enumType={OrderStatus} />,
    );

    expect(container.querySelector(".bg-warning-surface")).toBeTruthy();
  });

  it("humanizes a multi-word member name", () => {
    render(
      <StatusBadge
        value={OrderStatus.PAYMENT_CAPTURED}
        enumType={OrderStatus}
      />,
    );

    expect(screen.getByText("Payment Captured")).toBeTruthy();
  });

  // The component's second documented call, verbatim.
  it("accepts a bare member name alongside the enum object", () => {
    render(
      <StatusBadge
        value="PAYMENT_CAPTURED"
        enumType={OrderStatus}
        dot={false}
      />,
    );

    expect(screen.getByText("Payment Captured")).toBeTruthy();
  });

  it("strips the enum type prefix protobuf-es left on the member names", () => {
    render(
      <StatusBadge
        value={NestedOrderStatus.ORDER_STATUS_IN_TRANSIT}
        enumType={NestedOrderStatus}
      />,
    );

    expect(screen.getByText("In Transit")).toBeTruthy();
  });

  it("resolves a fully-qualified DB string against the enum object", () => {
    render(
      <StatusBadge
        value="ORDER_STATUS_PAYMENT_CAPTURED"
        enumType={OrderStatus}
      />,
    );

    expect(screen.getByText("Payment Captured")).toBeTruthy();
  });

  it("still strips the prefix from a plain string column given the type name", () => {
    render(
      <StatusBadge
        value="ORDER_STATUS_PAYMENT_CAPTURED"
        enumType="OrderStatus"
      />,
    );

    expect(screen.getByText("Payment Captured")).toBeTruthy();
  });

  it("renders the unset badge rather than an ordinal when only a type NAME is given", () => {
    render(<StatusBadge value={OrderStatus.PENDING} enumType="OrderStatus" />);

    expect(screen.queryByText("1")).toBeNull();
    expect(screen.getByText("—")).toBeTruthy();
  });

  it("renders the unset badge for an unset value", () => {
    render(<StatusBadge value={undefined} enumType={OrderStatus} />);

    expect(screen.getByText("—")).toBeTruthy();
  });
});
