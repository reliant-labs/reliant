import React from "react";

interface SlideStatHeroProps {
  /** The big number (e.g., "$9B+") */
  stat: string;
  headline: string;
  /** Supporting paragraphs */
  body: string[];
  /** Whether to use gradient text on stat */
  gradientStat?: boolean;
}

export const SlideStatHero: React.FC<SlideStatHeroProps> = ({
  stat,
  headline,
  body,
  gradientStat = false,
}) => {
  return (
    <div
      className="slide relative flex items-center justify-center overflow-hidden bg-gray-950"
      style={{ width: 1280, height: 720 }}
    >
      {/* Background glow behind stat */}
      <div
        className="pointer-events-none absolute inset-0"
        style={{
          background:
            "radial-gradient(ellipse 50% 40% at 50% 40%, rgba(139,92,246,0.1) 0%, transparent 70%)",
        }}
      />

      <div className="relative z-10 flex flex-col items-center gap-6 px-20 text-center">
        <p
          className={`text-[120px] font-black leading-none tracking-tight ${
            gradientStat
              ? "bg-gradient-to-r from-indigo-400 via-purple-400 to-pink-400 bg-clip-text text-transparent"
              : "text-white"
          }`}
        >
          {stat}
        </p>

        <h2 className="max-w-3xl text-3xl font-semibold leading-snug text-white">
          {headline}
        </h2>

        <div className="mt-2 flex max-w-2xl flex-col gap-3">
          {body.map((paragraph, i) => (
            <p key={i} className="text-lg leading-relaxed text-gray-400">
              {paragraph}
            </p>
          ))}
        </div>
      </div>
    </div>
  );
};

export default SlideStatHero;
