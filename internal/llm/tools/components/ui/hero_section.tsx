import React from "react";

interface HeroSectionProps {
  headline: string;
  subheadline?: string;
  primaryCta?: { label: string; href?: string };
  secondaryCta?: { label: string; href?: string };
  /** Optional image/illustration area */
  media?: React.ReactNode;
}

export default function HeroSection({
  headline,
  subheadline,
  primaryCta,
  secondaryCta,
  media,
}: HeroSectionProps) {
  return (
    <section className="bg-white">
      <div className="mx-auto grid max-w-7xl items-center gap-12 px-4 py-20 sm:px-6 lg:grid-cols-2 lg:px-8 lg:py-28">
        {/* Text content */}
        <div className="max-w-xl">
          <h1 className="text-4xl font-bold tracking-tight text-gray-900 sm:text-5xl lg:text-6xl">
            {headline}
          </h1>
          {subheadline && (
            <p className="mt-6 text-lg leading-relaxed text-gray-600">
              {subheadline}
            </p>
          )}
          {(primaryCta || secondaryCta) && (
            <div className="mt-8 flex flex-wrap gap-4">
              {primaryCta && (
                <a
                  href={primaryCta.href ?? "#"}
                  className="rounded-lg bg-blue-500 px-6 py-3 text-sm font-semibold text-white shadow-sm transition hover:bg-blue-600"
                >
                  {primaryCta.label}
                </a>
              )}
              {secondaryCta && (
                <a
                  href={secondaryCta.href ?? "#"}
                  className="rounded-lg border border-gray-300 px-6 py-3 text-sm font-semibold text-gray-700 transition hover:bg-gray-50"
                >
                  {secondaryCta.label}
                </a>
              )}
            </div>
          )}
        </div>

        {/* Media area */}
        {media && (
          <div className="flex items-center justify-center">{media}</div>
        )}
      </div>
    </section>
  );
}
