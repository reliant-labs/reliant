// Copyright (c) 2025 Reliant Labs

import type { ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import { OrgSwitchGate } from "@/components/Orgs/OrgSwitchGate";

/**
 * Billing for the org a link names (`/settings/billing?org=<id>`): a queued
 * deploy's "Set up billing", a new org's next step. Billing acts in the
 * ACTIVE org, so when the link names another one this asks before switching
 * and renders nothing that spends until the person has chosen
 * (OrgSwitchGate). Choosing to stay drops `?org=` and shows the active org's
 * billing.
 */
export function BillingOrgGate({ children }: { children: ReactNode }) {
  const { org } = useSearch({ strict: false }) as { org?: string };
  const navigate = useNavigate();

  const dismiss = () => {
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, org: undefined }), replace: true });
  };

  return (
    <OrgSwitchGate requestedOrgId={org} onDismiss={dismiss} subject="billing link" testIdPrefix="billing-org">
      {children}
    </OrgSwitchGate>
  );
}
