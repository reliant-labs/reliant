import type { SlideData } from "../types";
import { SlideLayout } from "./SlideLayout";

/**
 * The fixed 16:9 (1280x720) slide surface. Owns the brand background,
 * the logo/footer chrome, and the entrance animation. Content comes from
 * SlideLayout. In print each frame becomes one 16in x 9in page.
 *
 * `animateKey` changes on navigation to retrigger the fade-up entrance.
 */
export function SlideFrame({
  data,
  total,
  animateKey,
}: {
  data: SlideData;
  total: number;
  animateKey: number;
}) {
  return (
    <section
      className="slide-print relative overflow-hidden bg-background text-foreground"
      style={{ width: "var(--slide-w)", height: "var(--slide-h)" }}
      aria-roledescription="slide"
      aria-label={`Slide ${data.number} of ${total}: ${data.navLabel}`}
    >
      {/* Subtle midnight-gradient wash in the corner for depth. */}
      <div
        className="pointer-events-none absolute -right-40 -top-40 h-[500px] w-[500px] rounded-full opacity-20 blur-3xl"
        style={{ background: "var(--brand-gradient)" }}
        aria-hidden="true"
      />

      {/* Animated content region — keyed so entrance replays on nav. */}
      <div key={animateKey} className="animate-fade-up h-full w-full">
        <SlideLayout data={data} />
      </div>

      {/* Footer chrome: logo + slide number. */}
      <footer className="pointer-events-none absolute bottom-6 left-0 right-0 flex items-center justify-between px-12">
        <div className="flex items-center gap-2">
          <img src="./logo.svg" alt="Reliant" className="h-5 w-auto" />
          <span className="text-sm font-semibold tracking-tight text-muted-strong">
            Reliant
          </span>
        </div>
        <span className="font-mono text-sm text-muted">
          {String(data.number).padStart(2, "0")} / {String(total).padStart(2, "0")}
        </span>
      </footer>
    </section>
  );
}
