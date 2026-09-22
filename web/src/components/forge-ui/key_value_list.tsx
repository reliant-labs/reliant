import React from "react";

interface Field {
  label: string;
  value: React.ReactNode;
  copyable?: boolean;
  mono?: boolean;
}

interface FieldGroup {
  title?: string;
  fields: Field[];
}

interface KeyValueListProps {
  groups: FieldGroup[];
  columns?: 1 | 2 | 3;
  variant?: "striped" | "bordered" | "plain";
}

export default function KeyValueList({
  groups,
  columns = 1,
  variant = "striped",
}: KeyValueListProps) {
  const [copied, setCopied] = React.useState<string | null>(null);

  function handleCopy(value: string, label: string) {
    navigator.clipboard.writeText(value);
    setCopied(label);
    setTimeout(() => setCopied(null), 2000);
  }

  const gridCls =
    columns === 3 ? "md:grid-cols-3" : columns === 2 ? "md:grid-cols-2" : "";

  return (
    <div className="space-y-6">
      {groups.map((group, gi) => (
        <div key={gi}>
          {group.title && (
            <h3 className="mb-3 text-sm font-semibold text-ink">
              {group.title}
            </h3>
          )}
          <dl
            className={`grid grid-cols-1 ${gridCls} ${
              variant === "bordered"
                ? "divide-y divide-border rounded-lg border border-border"
                : variant === "striped"
                  ? "divide-y divide-border rounded-lg border border-border bg-surface"
                  : "gap-4"
            }`}
          >
            {group.fields.map((field, fi) => (
              <div
                key={fi}
                className={`flex items-start justify-between gap-4 px-4 py-3 ${
                  variant === "striped" && fi % 2 === 0
                    ? "bg-surface-muted"
                    : ""
                } ${variant === "plain" ? "py-2" : ""}`}
              >
                <dt className="text-sm font-medium text-ink-muted min-w-[120px]">
                  {field.label}
                </dt>
                <dd
                  className={`flex items-center gap-2 text-sm text-ink text-right ${
                    field.mono ? "font-mono" : ""
                  }`}
                >
                  {field.value}
                  {field.copyable && typeof field.value === "string" && (
                    <button
                      onClick={() =>
                        handleCopy(field.value as string, field.label)
                      }
                      className="flex-shrink-0 text-ink-subtle hover:text-ink-muted"
                      title="Copy to clipboard"
                    >
                      {copied === field.label ? (
                        <svg
                          className="h-4 w-4 text-success"
                          fill="none"
                          viewBox="0 0 24 24"
                          strokeWidth={2}
                          stroke="currentColor"
                        >
                          <path
                            strokeLinecap="round"
                            strokeLinejoin="round"
                            d="M4.5 12.75l6 6 9-13.5"
                          />
                        </svg>
                      ) : (
                        <svg
                          className="h-4 w-4"
                          fill="none"
                          viewBox="0 0 24 24"
                          strokeWidth={1.5}
                          stroke="currentColor"
                        >
                          <path
                            strokeLinecap="round"
                            strokeLinejoin="round"
                            d="M15.75 17.25v3.375c0 .621-.504 1.125-1.125 1.125h-9.75a1.125 1.125 0 01-1.125-1.125V7.875c0-.621.504-1.125 1.125-1.125H6.75a9.06 9.06 0 011.5.124m7.5 10.376h3.375c.621 0 1.125-.504 1.125-1.125V11.25c0-2.003-.658-3.853-1.768-5.346M15.75 17.25H6.75m9-9h3.375c.621 0 1.125-.504 1.125-1.125V4.875c0-.621-.504-1.125-1.125-1.125H6.75c-.621 0-1.125.504-1.125 1.125v3.375"
                          />
                        </svg>
                      )}
                    </button>
                  )}
                </dd>
              </div>
            ))}
          </dl>
        </div>
      ))}
    </div>
  );
}
