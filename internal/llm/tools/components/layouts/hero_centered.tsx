import React from "react";

interface HeroCenteredProps {
  headline: string;
  subheadline?: string;
  /** CTA buttons */
  actions?: Array<{ label: string; primary?: boolean; href?: string }>;
  /** Background gradient colors [from, to] */
  gradient?: [string, string];
  children?: React.ReactNode;
}

export default function HeroCentered({
  headline,
  subheadline,
  actions,
  gradient,
  children,
}: HeroCenteredProps) {
  const bgStyle = gradient
    ? { background: `linear-gradient(135deg, ${gradient[0]}, ${gradient[1]})` }
    : undefined;

  return (
    <section
      className={`flex min-h-[60vh] flex-col items-center justify-center px-6 py-24 text-center ${
        !gradient ? "bg-gradient-to-br from-indigo-600 to-purple-700" : ""
      }`}
      style={bgStyle}
    >
      <h1 className="max-w-4xl text-5xl font-bold tracking-tight text-white sm:text-6xl">
        {headline}
      </h1>

      {subheadline && (
        <p className="mt-6 max-w-2xl text-lg leading-8 text-white/80">
          {subheadline}
        </p>
      )}

      {actions && actions.length > 0 && (
        <div className="mt-10 flex flex-wrap items-center justify-center gap-4">
          {actions.map((action, i) => (
            <a
              key={i}
              href={action.href ?? "#"}
              className={
                action.primary
                  ? "rounded-lg bg-white px-6 py-3 text-sm font-semibold text-indigo-600 shadow-sm hover:bg-gray-100"
                  : "rounded-lg border border-white/30 px-6 py-3 text-sm font-semibold text-white hover:bg-white/10"
              }
            >
              {action.label}
            </a>
          ))}
        </div>
      )}

      {children && <div className="mt-12 w-full max-w-4xl">{children}</div>}
    </section>
  );
}
