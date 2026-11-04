interface PricingTableProps {
  tiers: Array<{
    name: string;
    price: string;
    period?: string;
    features: Array<{ text: string; included: boolean }>;
    highlight?: boolean;
    badge?: string;
    cta?: string;
  }>;
}

export default function PricingTable({ tiers }: PricingTableProps) {
  return (
    <div className="grid grid-cols-1 md:grid-cols-3 gap-6 max-w-5xl mx-auto px-4 py-12">
      {tiers.map((tier) => (
        <div
          key={tier.name}
          className={`relative flex flex-col rounded-2xl border p-8 ${
            tier.highlight
              ? "border-accent-border border-2 shadow-lg shadow-accent/10"
              : "border-border"
          }`}
        >
          {tier.badge && (
            <span className="absolute -top-3 left-1/2 -translate-x-1/2 rounded-full bg-accent px-4 py-1 text-xs font-semibold text-on-accent">
              {tier.badge}
            </span>
          )}

          <h3 className="text-lg font-semibold text-ink">{tier.name}</h3>

          <div className="mt-4 flex items-baseline gap-1">
            <span className="text-4xl font-bold tracking-tight text-ink">{tier.price}</span>
            {tier.period && <span className="text-sm text-ink-muted">/{tier.period}</span>}
          </div>

          <ul className="mt-8 flex-1 space-y-3">
            {tier.features.map((feature) => (
              <li key={feature.text} className="flex items-start gap-3">
                {feature.included ? (
                  <svg
                    className="h-5 w-5 shrink-0 text-accent"
                    viewBox="0 0 20 20"
                    fill="currentColor"
                  >
                    <path
                      fillRule="evenodd"
                      d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 111.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z"
                      clipRule="evenodd"
                    />
                  </svg>
                ) : (
                  <svg
                    className="h-5 w-5 shrink-0 text-ink-subtle"
                    viewBox="0 0 20 20"
                    fill="currentColor"
                  >
                    <path
                      fillRule="evenodd"
                      d="M4.293 4.293a1 1 0 011.414 0L10 8.586l4.293-4.293a1 1 0 111.414 1.414L11.414 10l4.293 4.293a1 1 0 01-1.414 1.414L10 11.414l-4.293 4.293a1 1 0 01-1.414-1.414L8.586 10 4.293 5.707a1 1 0 010-1.414z"
                      clipRule="evenodd"
                    />
                  </svg>
                )}
                <span
                  className={`text-sm ${feature.included ? "text-ink-muted" : "text-ink-subtle"}`}
                >
                  {feature.text}
                </span>
              </li>
            ))}
          </ul>

          <a
            href="#"
            className={`mt-8 block rounded-lg px-4 py-3 text-center text-sm font-semibold transition ${
              tier.highlight
                ? "bg-accent text-on-accent hover:bg-accent"
                : "bg-surface-muted text-ink ring-1 ring-inset ring-border hover:bg-surface-muted"
            }`}
          >
            {tier.cta ?? "Get started"}
          </a>
        </div>
      ))}
    </div>
  );
}
