/**
 * Renders one policy card as the page does: controlled, so a change shows,
 * with every change it reports kept for the test to read.
 */
import { useState, type ComponentType } from "react";
import { vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import type { MyOrg } from "@/services/controlPlane/orgs";
import { renderWithQuery } from "@/test/renderWithQuery";

import type { PolicyCardProps } from "../PolicyCard";

export const ORG: MyOrg = { id: "org-a", name: "Acme", slug: "acme", role: "admin", isOwner: false, isDefault: true };

type Handlers = Parameters<typeof mockTransport>[0];

export function renderCard<T, X extends object = object>(
  Card: ComponentType<PolicyCardProps<T> & X>,
  initial: T | undefined,
  opts: { editable?: boolean; extra?: X; handlers?: Handlers } = {},
) {
  const onChange = vi.fn<(next: T | undefined) => void>();
  setTransport(mockTransport(opts.handlers ?? {}));
  function Harness() {
    const [value, setValue] = useState<T | undefined>(initial);
    return (
      <Card
        {...(opts.extra as X)}
        value={value}
        onChange={(next) => {
          onChange(next);
          setValue(next);
        }}
        editable={opts.editable ?? true}
        org={ORG}
        changed={false}
        violations={[]}
      />
    );
  }
  const view = renderWithQuery(<Harness />);
  return { ...view, onChange, last: () => onChange.mock.calls.at(-1)?.[0] };
}
