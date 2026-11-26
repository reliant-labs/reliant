// Copyright (c) 2026 Reliant Labs

/**
 * The pending and firing alerts on an ML subject, as AlertingService lists
 * them (useMlAlerts): the rule, its summary, its severity chip
 * (Observability's), what it is on (the subject its labels name, a link),
 * how long it has been firing, and the auto action its rule declares (an
 * `auto_action` annotation; notify only when there is none).
 */

import type { ReactNode } from "react";

import type { AlertInstance } from "@/services/controlPlane/observability/alertingTypes";
import { subjectOfAlertLabels } from "@/services/controlPlane/ml/telemetry";

import { SeverityChip } from "../../Observability/AlertsView";
import { fmtAgo } from "./format";
import { EmptyNote, MlTable, StatusDot, SubjectLink, type MlColumn } from "./ui";

const alertSince = (a: AlertInstance) => a.firingAtMs ?? a.activeAtMs;

export function MlAlertsTable({ alerts, now, showSubject = false, empty, testId }: { alerts: readonly AlertInstance[]; now: number; showSubject?: boolean; empty?: ReactNode; testId?: string }) {
  const columns: MlColumn<AlertInstance>[] = [
    { key: "state", header: "State", render: (a) => <StatusDot tone={a.state === "firing" ? "danger" : "warning"} label={a.state === "firing" ? "Firing" : "Pending"} /> },
    {
      key: "rule",
      header: "Alert",
      className: "whitespace-normal",
      render: (a) => (
        <div className="flex flex-col py-0.5 leading-tight">
          <span className="font-medium text-ink">{a.rule}</span>
          {a.annotations.summary && <span className="text-meta text-ink-muted">{a.annotations.summary}</span>}
        </div>
      ),
    },
    { key: "sev", header: "Severity", render: (a) => <SeverityChip severity={a.labels.severity ?? ""} /> },
    ...(showSubject
      ? [
          {
            key: "on",
            header: "On",
            render: (a: AlertInstance) => {
              const subject = subjectOfAlertLabels(a.labels);
              return subject ? <SubjectLink subject={subject}>{a.labels.ml_subject_label || subject.id}</SubjectLink> : "—";
            },
          },
        ]
      : []),
    { key: "since", header: "Since", render: (a) => <span className="text-meta tabular-nums text-ink-muted">{fmtAgo(alertSince(a), now)}</span>, sort: (a) => alertSince(a) ?? 0 },
    {
      key: "auto",
      header: "Auto action",
      className: "whitespace-normal",
      render: (a) => (a.annotations.auto_action ? <span className="text-meta text-ink">{a.annotations.auto_action}</span> : <span className="text-meta text-ink-muted">notify only</span>),
    },
  ];
  return <MlTable columns={columns} rows={alerts} rowKey={(a) => a.fingerprint || `${a.rule}-${JSON.stringify(a.labels)}`} testId={testId} empty={empty ?? <EmptyNote>Nothing pending or firing.</EmptyNote>} />;
}
