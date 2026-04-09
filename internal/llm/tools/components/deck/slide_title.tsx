import React from "react";

interface SlideTitleProps {
  companyName: string;
  tagline: string;
  /** Optional React node for logo */
  logo?: React.ReactNode;
}

export const SlideTitle: React.FC<SlideTitleProps> = ({
  companyName,
  tagline,
  logo,
}) => {
  return (
    <div
      className="slide relative flex items-center justify-center overflow-hidden bg-gray-950"
      style={{ width: 1280, height: 720 }}
    >
      {/* Subtle dot grid background pattern */}
      <div
        className="pointer-events-none absolute inset-0 opacity-[0.07]"
        style={{
          backgroundImage:
            "radial-gradient(circle, rgba(255,255,255,0.8) 1px, transparent 1px)",
          backgroundSize: "32px 32px",
        }}
      />

      {/* Subtle radial glow */}
      <div
        className="pointer-events-none absolute inset-0"
        style={{
          background:
            "radial-gradient(ellipse 60% 50% at 50% 50%, rgba(99,102,241,0.08) 0%, transparent 70%)",
        }}
      />

      <div className="relative z-10 flex flex-col items-center gap-6 px-16 text-center">
        {logo && <div className="mb-2 h-20 w-20 flex-shrink-0">{logo}</div>}

        <h1 className="text-7xl font-extrabold tracking-tight text-white">
          {companyName}
        </h1>

        <p className="max-w-2xl text-2xl font-light leading-relaxed text-gray-400">
          {tagline}
        </p>
      </div>
    </div>
  );
};

export default SlideTitle;
