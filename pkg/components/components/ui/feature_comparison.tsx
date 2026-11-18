import React from "react";

interface FeatureComparisonProps {
  products: Array<{ name: string; highlight?: boolean }>;
  groups: Array<{
    name: string;
    features: Array<{
      name: string;
      values: Array<boolean | string>;
    }>;
  }>;
}

function CheckIcon() {
  return (
    <svg className="mx-auto h-5 w-5 text-success" viewBox="0 0 20 20" fill="currentColor">
      <path
        fillRule="evenodd"
        d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 111.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z"
        clipRule="evenodd"
      />
    </svg>
  );
}

function CrossIcon() {
  return (
    <svg className="mx-auto h-5 w-5 text-ink-subtle" viewBox="0 0 20 20" fill="currentColor">
      <path
        fillRule="evenodd"
        d="M4.293 4.293a1 1 0 011.414 0L10 8.586l4.293-4.293a1 1 0 111.414 1.414L11.414 10l4.293 4.293a1 1 0 01-1.414 1.414L10 11.414l-4.293 4.293a1 1 0 01-1.414-1.414L8.586 10 4.293 5.707a1 1 0 010-1.414z"
        clipRule="evenodd"
      />
    </svg>
  );
}

export default function FeatureComparison({ products, groups }: FeatureComparisonProps) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[600px] border-collapse text-sm">
        <thead className="sticky top-0 z-10 bg-surface">
          <tr>
            <th className="border-b border-border py-4 pr-4 text-left font-medium text-ink-muted">
              Feature
            </th>
            {products.map((product) => (
              <th
                key={product.name}
                className={`border-b border-border px-4 py-4 text-center font-semibold ${
                  product.highlight ? "bg-accent-surface text-accent-ink" : "text-ink"
                }`}
              >
                {product.name}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {groups.map((group) => (
            <React.Fragment key={group.name}>
              <tr>
                <td
                  colSpan={products.length + 1}
                  className="border-b border-border bg-surface-muted px-0 py-3 text-xs font-medium text-ink-muted"
                >
                  {group.name}
                </td>
              </tr>
              {group.features.map((feature) => (
                <tr key={feature.name} className="group">
                  <td className="border-b border-border py-3 pr-4 text-ink-muted group-hover:bg-surface-muted">
                    {feature.name}
                  </td>
                  {feature.values.map((value, i) => (
                    <td
                      key={i}
                      className={`border-b border-border px-4 py-3 text-center group-hover:bg-surface-muted ${
                        products[i]?.highlight ? "bg-accent-surface/50" : ""
                      }`}
                    >
                      {typeof value === "boolean" ? (
                        value ? (
                          <CheckIcon />
                        ) : (
                          <CrossIcon />
                        )
                      ) : (
                        <span className="text-ink-muted">{value}</span>
                      )}
                    </td>
                  ))}
                </tr>
              ))}
            </React.Fragment>
          ))}
        </tbody>
      </table>
    </div>
  );
}
