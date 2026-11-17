import React from "react";

interface Field {
  label: string;
  value: React.ReactNode;
  type?: "text" | "badge" | "date" | "link";
}

interface FieldGroup {
  title?: string;
  fields: Field[];
}

interface Action {
  label: string;
  onClick?: () => void;
  variant?: "primary" | "secondary" | "danger";
  icon?: React.ReactNode;
}

interface DetailViewProps {
  title: string;
  subtitle?: string;
  fields: FieldGroup[];
  actions?: Action[];
}

const badgeColors = [
  "bg-blue-50 text-blue-700",
  "bg-green-50 text-green-700",
  "bg-purple-50 text-purple-700",
  "bg-amber-50 text-amber-700",
];

const buttonStyles: Record<string, string> = {
  primary: "bg-blue-600 text-white hover:bg-blue-700 focus:ring-blue-500",
  secondary: "border border-gray-300 bg-white text-gray-700 hover:bg-gray-50 focus:ring-gray-500",
  danger: "bg-red-600 text-white hover:bg-red-700 focus:ring-red-500",
};

function FieldValue({ field }: { field: Field }) {
  switch (field.type) {
    case "badge":
      return (
        <span
          className={`inline-flex rounded-full px-2.5 py-0.5 text-xs font-semibold ${
            badgeColors[Math.abs(String(field.value).length) % badgeColors.length]
          }`}
        >
          {field.value}
        </span>
      );
    case "link":
      return (
        <a
          href={String(field.value)}
          className="text-blue-600 hover:text-blue-500 hover:underline"
          target="_blank"
          rel="noopener noreferrer"
        >
          {field.value}
        </a>
      );
    case "date":
      return <time className="text-gray-900">{field.value}</time>;
    default:
      return <span className="text-gray-900">{field.value}</span>;
  }
}

export default function DetailView({ title, subtitle, fields, actions }: DetailViewProps) {
  return (
    <div className="overflow-hidden rounded-xl border border-gray-200 bg-white">
      {/* Header */}
      <div className="flex items-center justify-between border-b border-gray-200 px-6 py-4">
        <div>
          <h2 className="text-lg font-semibold text-gray-900">{title}</h2>
          {subtitle && <p className="mt-0.5 text-sm text-gray-500">{subtitle}</p>}
        </div>
        {actions && actions.length > 0 && (
          <div className="flex gap-2">
            {actions.map((action) => (
              <button
                key={action.label}
                onClick={action.onClick}
                className={`inline-flex items-center gap-1.5 rounded-lg px-3 py-2 text-sm font-medium transition focus:outline-none focus:ring-2 focus:ring-offset-2 ${
                  buttonStyles[action.variant ?? "secondary"]
                }`}
              >
                {action.icon && <span className="h-4 w-4">{action.icon}</span>}
                {action.label}
              </button>
            ))}
          </div>
        )}
      </div>

      {/* Field Groups */}
      <div className="divide-y divide-gray-100">
        {fields.map((group, gi) => (
          <div key={gi} className="px-6 py-5">
            {group.title && <h3 className="mb-4 text-sm font-medium text-ink">{group.title}</h3>}
            <dl className="grid grid-cols-1 gap-x-6 gap-y-4 sm:grid-cols-2 lg:grid-cols-3">
              {group.fields.map((field) => (
                <div key={field.label}>
                  <dt className="text-sm font-medium text-gray-500">{field.label}</dt>
                  <dd className="mt-1 text-sm">
                    <FieldValue field={field} />
                  </dd>
                </div>
              ))}
            </dl>
          </div>
        ))}
      </div>
    </div>
  );
}
