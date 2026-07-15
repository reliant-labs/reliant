import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ChevronLeft, ChevronRight, Maximize2 } from "lucide-react";
import { slides } from "../slides";
import { SlideFrame } from "./SlideFrame";

const SLIDE_W = 1280;
const SLIDE_H = 720;

/**
 * The deck shell: presents one slide at a time, scaled to fit the viewport,
 * with keyboard navigation, slide dots, and fullscreen. On print it renders
 * every slide stacked (one page each) via the always-mounted print list.
 */
export function Deck() {
  const [index, setIndex] = useState(0);
  const [scale, setScale] = useState(1);
  const stageRef = useRef<HTMLDivElement>(null);

  const total = slides.length;
  const go = useCallback(
    (next: number) => setIndex(() => Math.min(Math.max(next, 0), total - 1)),
    [total],
  );

  // Scale the fixed 1280x720 stage to fit the viewport while preserving 16:9.
  useLayoutEffect(() => {
    const fit = () => {
      const pad = 48; // breathing room around the slide
      const availW = window.innerWidth - pad;
      const availH = window.innerHeight - pad;
      setScale(Math.min(availW / SLIDE_W, availH / SLIDE_H));
    };
    fit();
    window.addEventListener("resize", fit);
    return () => window.removeEventListener("resize", fit);
  }, []);

  // Keyboard navigation: arrows/space to move, Escape exits fullscreen.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      switch (e.key) {
        case "ArrowRight":
        case "ArrowDown":
        case " ":
        case "PageDown":
          e.preventDefault();
          go(index + 1);
          break;
        case "ArrowLeft":
        case "ArrowUp":
        case "PageUp":
          e.preventDefault();
          go(index - 1);
          break;
        case "Home":
          go(0);
          break;
        case "End":
          go(total - 1);
          break;
        case "Escape":
          if (document.fullscreenElement) document.exitFullscreen();
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [index, go, total]);

  const toggleFullscreen = () => {
    if (document.fullscreenElement) document.exitFullscreen();
    else document.documentElement.requestFullscreen();
  };

  const current = slides[index];

  return (
    <div className="flex h-full w-full flex-col items-center justify-center bg-background">
      {/* Screen view: single scaled slide. Hidden in print. */}
      <div
        ref={stageRef}
        className="no-print relative flex items-center justify-center"
        style={{ width: SLIDE_W * scale, height: SLIDE_H * scale }}
      >
        <div
          style={{
            width: SLIDE_W,
            height: SLIDE_H,
            transform: `scale(${scale})`,
            transformOrigin: "center",
          }}
          className="rounded-xl border border-border shadow-2xl"
        >
          <SlideFrame data={current} total={total} animateKey={index} />
        </div>
      </div>

      {/* Controls. Hidden in print. */}
      <nav
        className="no-print fixed bottom-6 left-1/2 z-10 flex -translate-x-1/2 items-center gap-4 rounded-full border border-border bg-surface/80 px-5 py-3 backdrop-blur"
        aria-label="Slide navigation"
      >
        <button
          onClick={() => go(index - 1)}
          disabled={index === 0}
          aria-label="Previous slide"
          className="flex h-8 w-8 items-center justify-center rounded-full text-muted-strong transition-colors duration-200 hover:bg-surface-raised hover:text-foreground disabled:cursor-not-allowed disabled:opacity-30"
        >
          <ChevronLeft className="h-5 w-5" aria-hidden="true" />
        </button>

        <div className="flex items-center gap-2" role="tablist" aria-label="Slides">
          {slides.map((s, i) => (
            <button
              key={s.id}
              role="tab"
              aria-selected={i === index}
              aria-label={`Go to slide ${i + 1}: ${s.navLabel}`}
              onClick={() => go(i)}
              className={`h-2.5 rounded-full transition-all duration-200 ${
                i === index
                  ? "w-7 bg-brand-gradient"
                  : "w-2.5 bg-border-strong hover:bg-muted"
              }`}
            />
          ))}
        </div>

        <button
          onClick={() => go(index + 1)}
          disabled={index === total - 1}
          aria-label="Next slide"
          className="flex h-8 w-8 items-center justify-center rounded-full text-muted-strong transition-colors duration-200 hover:bg-surface-raised hover:text-foreground disabled:cursor-not-allowed disabled:opacity-30"
        >
          <ChevronRight className="h-5 w-5" aria-hidden="true" />
        </button>

        <div className="mx-1 h-6 w-px bg-border" aria-hidden="true" />

        <button
          onClick={toggleFullscreen}
          aria-label="Toggle fullscreen"
          className="flex h-8 w-8 items-center justify-center rounded-full text-muted-strong transition-colors duration-200 hover:bg-surface-raised hover:text-foreground"
        >
          <Maximize2 className="h-4 w-4" aria-hidden="true" />
        </button>
      </nav>

      {/* Print view: every slide, one page each. Only visible in print. */}
      <div className="hidden print:block">
        {slides.map((s, i) => (
          <SlideFrame key={s.id} data={s} total={total} animateKey={i} />
        ))}
      </div>
    </div>
  );
}