import React from "react";

interface SlideQuoteProps {
  quote: string;
  attribution: string;
  role?: string;
}

export const SlideQuote: React.FC<SlideQuoteProps> = ({
  quote,
  attribution,
  role,
}) => {
  return (
    <div
      className="slide relative flex items-center justify-center overflow-hidden bg-gray-950"
      style={{ width: 1280, height: 720 }}
    >
      {/* Subtle background accent */}
      <div
        className="pointer-events-none absolute inset-0"
        style={{
          background:
            "radial-gradient(ellipse 70% 50% at 50% 50%, rgba(99,102,241,0.06) 0%, transparent 70%)",
        }}
      />

      <div className="relative z-10 flex max-w-4xl flex-col items-center gap-10 px-20 text-center">
        {/* Large decorative quote mark */}
        <svg
          width="64"
          height="48"
          viewBox="0 0 64 48"
          fill="none"
          className="flex-shrink-0 text-indigo-500/30"
        >
          <path
            d="M0 48V28.8C0 20.267 1.867 13.333 5.6 8C9.6 2.667 15.467 0 23.2 0v9.6c-4.267 0-7.467 1.467-9.6 4.4-1.867 2.667-2.8 6.133-2.8 10.4H24V48H0Zm40 0V28.8c0-8.533 1.867-15.467 5.6-20.8C49.6 2.667 55.467 0 63.2 0v9.6c-4.267 0-7.467 1.467-9.6 4.4-1.867 2.667-2.8 6.133-2.8 10.4H64V48H40Z"
            fill="currentColor"
          />
        </svg>

        <blockquote className="text-3xl font-light italic leading-relaxed text-gray-200">
          {quote}
        </blockquote>

        <div className="flex flex-col items-center gap-1">
          <span className="text-lg font-semibold text-white">
            {attribution}
          </span>
          {role && (
            <span className="text-base text-gray-500">{role}</span>
          )}
        </div>
      </div>
    </div>
  );
};

export default SlideQuote;
