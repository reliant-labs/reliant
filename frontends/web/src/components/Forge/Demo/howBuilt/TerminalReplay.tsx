// Copyright (c) 2025 Reliant Labs

/**
 * A small terminal player for the recorded forge run (fixtures.ts): play and
 * pause, speed, Step (finish this command, or show the whole next one),
 * Restart, and a Copy button on every command and every proto you write.
 *
 * It plays once by itself, at 4×, when it first scrolls into view
 * (IntersectionObserver), then offers Replay. With prefers-reduced-motion it
 * never animates on its own: it opens on the finished run, with Play.
 *
 * Written here rather than adding asciinema-player: the cast is ours, its
 * output is plain text (the recorder strips colour), and all it needs is a
 * timer over replay.ts. A standard .cast for asciinema is one call away
 * (toAsciicast). Each command shows how long it really took, since waits play
 * shortened.
 */

import { useEffect, useReducer, useRef, useState } from "react";
import { Check, Copy, Pause, Play, RotateCcw, SkipForward } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { cn } from "@/lib/utils";

import { shortForgeVersion } from "./forgeVersion";
import { promptOf, type CastStep, type ScaffoldCast } from "./fixtures";
import { delayToNext, eventCount, finalReplay, formatSeconds, initialReplay, isComplete, outputOf, replayReducer, SPEEDS, type ReplayState } from "./replay";

export interface TerminalReplayProps {
  cast: ScaffoldCast;
  /**
   * Play once by itself when the player first scrolls into view (default
   * true). Never for a reader who asked for reduced motion: they get the
   * finished run and a Play button.
   */
  autoPlay?: boolean;
  className?: string;
}

/** How much of the player must be on screen before it starts by itself. */
const VISIBLE = 0.4;

function prefersReducedMotion(): boolean {
  return typeof window !== "undefined" && window.matchMedia?.("(prefers-reduced-motion: reduce)").matches === true;
}

export function TerminalReplay({ cast, autoPlay = true, className }: TerminalReplayProps) {
  const [reduced] = useState(prefersReducedMotion);
  const [state, dispatch] = useReducer(
    (current: ReplayState, action: Parameters<typeof replayReducer>[2]) => replayReducer(cast, current, action),
    undefined,
    () => (reduced ? finalReplay(cast) : initialReplay())
  );
  // Whether it has played (by itself or on request): the finished run then offers Replay, not Play.
  const [played, setPlayed] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const screen = useRef<HTMLDivElement>(null);
  const done = isComplete(cast, state);
  // Before the first Play: the run's outline and a play button, never an empty screen.
  const poster = !played && !state.playing && state.step === 0 && state.shown === 0;

  // Once, when it first comes into view. A pause or a scroll away and back never restarts it.
  useEffect(() => {
    if (!autoPlay || reduced) return;
    const start = () => {
      setPlayed(true);
      dispatch({ type: "play" });
    };
    const el = root.current;
    if (!el || typeof IntersectionObserver === "undefined") {
      start();
      return;
    }
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          observer.disconnect();
          start();
        }
      },
      { threshold: VISIBLE }
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, [autoPlay, reduced]);

  // One timer at a time: the wait before the next event or step, at this speed.
  useEffect(() => {
    if (!state.playing) return;
    const delay = delayToNext(cast, state);
    if (delay === null) {
      dispatch({ type: "pause" });
      return;
    }
    const timer = setTimeout(() => dispatch({ type: "tick" }), delay * 1000);
    return () => clearTimeout(timer);
  }, [cast, state]);

  // Keep the newest output in view, as a terminal does.
  useEffect(() => {
    const el = screen.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [state.step, state.shown]);

  return (
    <div ref={root} className={cn("overflow-hidden rounded-lg border border-border", className)} data-testid="scaffold-replay" data-reduced-motion={reduced || undefined}>
      <div className="flex flex-wrap items-center gap-2 border-b border-border bg-surface-muted px-3 py-2">
        <Button
          size="sm"
          variant="primary"
          leftIcon={state.playing ? <Pause /> : done && played ? <RotateCcw /> : <Play />}
          onClick={() => {
            if (!state.playing) setPlayed(true);
            dispatch({ type: state.playing ? "pause" : "play" });
          }}
          data-testid="replay-toggle"
        >
          {state.playing ? "Pause" : done ? (played ? "Replay" : "Play") : state.step === 0 && state.shown === 0 ? "Play" : "Resume"}
        </Button>
        <Button size="sm" variant="outline" leftIcon={<SkipForward />} onClick={() => dispatch({ type: "step" })} disabled={done} data-testid="replay-step">
          Step
        </Button>
        <Button size="sm" variant="ghost" leftIcon={<RotateCcw />} onClick={() => dispatch({ type: "restart" })} data-testid="replay-restart">
          Restart
        </Button>
        <div className="ml-auto flex items-center gap-1" role="group" aria-label="Speed">
          {SPEEDS.map((speed) => (
            <button
              key={speed}
              type="button"
              aria-pressed={state.speed === speed}
              onClick={() => dispatch({ type: "speed", speed })}
              className={cn(
                "rounded px-2 py-0.5 font-mono text-xs",
                state.speed === speed ? "bg-ink text-surface" : "text-ink-muted hover:text-ink"
              )}
              data-testid={`replay-speed-${speed}`}
            >
              {speed}×
            </button>
          ))}
        </div>
      </div>
      <div
        ref={screen}
        role="log"
        aria-label="Recorded terminal session"
        className="h-80 overflow-y-auto bg-[var(--code-bg)] px-3 py-2 font-mono text-xs leading-relaxed text-[var(--code-fg)]"
        data-testid="replay-screen"
      >
        {poster ? (
          <Poster
            cast={cast}
            onPlay={() => {
              setPlayed(true);
              dispatch({ type: "play" });
            }}
          />
        ) : (
          cast.steps.slice(0, state.step + 1).map((step, index) => (
            <ReplayStep key={index} index={index} step={step} shown={index < state.step ? eventCount(step) : state.shown} />
          ))
        )}
        {done && <p className="mt-2 text-ink-muted">{`${promptOf(cast.steps[cast.steps.length - 1].cwd)}`}</p>}
      </div>
      <p className="border-t border-border bg-surface-muted px-3 py-1.5 text-xs text-ink-muted">
        Recorded with <span title={`forge ${cast.forge_version}`}>forge {shortForgeVersion(cast.forge_version)}</span>, output as printed.
      </p>
    </div>
  );
}

/** The first screen: what the run does, step by step, with how long each really took. */
function Poster({ cast, onPlay }: { cast: ScaffoldCast; onPlay: () => void }) {
  return (
    <div className="relative flex min-h-full flex-col" data-testid="replay-poster">
      <ol className="space-y-1.5 opacity-80">
        {cast.steps.map((step, index) =>
          step.kind === "command" ? (
            <li key={index} className="flex gap-2">
              <span className="min-w-0 flex-1 whitespace-pre-wrap break-words">
                <span className="text-ink-muted">{promptOf(step.cwd)}</span>
                <span className="font-semibold">{step.command}</span>
              </span>
              <span className="shrink-0 text-ink-muted">{formatSeconds(step.real_seconds)}</span>
            </li>
          ) : (
            <li key={index} className="text-ink-muted"># You write {step.path}</li>
          )
        )}
      </ol>
      <div className="mt-auto flex justify-center pt-6">
        <Button variant="primary" size="lg" className="font-sans" leftIcon={<Play />} onClick={onPlay} data-testid="replay-poster-play">
          Play the recording
        </Button>
      </div>
    </div>
  );
}

function ReplayStep({ step, shown, index }: { step: CastStep; shown: number; index: number }) {
  if (step.kind === "edit") {
    if (shown === 0) return null;
    return (
      <div className="my-2" data-testid={`replay-step-${index}`} data-kind="edit">
        <div className="flex items-center gap-2 text-ink-muted">
          <span className="min-w-0 flex-1"># You write, in {step.path} (replacing forge&apos;s TODO line):</span>
          <CopyButton text={step.text} label={`the proto for ${step.path}`} />
        </div>
        <pre className="whitespace-pre-wrap border-l-2 border-primary pl-2 text-[var(--code-fg)]">{step.text}</pre>
      </div>
    );
  }
  const output = outputOf(step, shown);
  return (
    <div className="mt-1" data-testid={`replay-step-${index}`} data-kind="command">
      <div className="flex items-center gap-2">
        {/* Wrap between words, never inside one ("--servi / ce" on a phone). */}
        <span className="min-w-0 flex-1 whitespace-pre-wrap break-words">
          <span className="text-ink-muted">{promptOf(step.cwd)}</span>
          <span className="font-semibold" data-testid="replay-command">
            {step.command}
          </span>
        </span>
        <span className="shrink-0 text-ink-muted" title="How long it really took">
          {formatSeconds(step.real_seconds)}
        </span>
        <CopyButton text={step.command} label={step.command} />
      </div>
      {output && <pre className="whitespace-pre-wrap break-words">{output}</pre>}
    </div>
  );
}

function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      return; // No clipboard: the text is on screen to select.
    }
    setCopied(true);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 1500);
  }
  return (
    <button
      type="button"
      onClick={() => void copy()}
      aria-label={copied ? `Copied ${label}` : `Copy ${label}`}
      title={copied ? "Copied" : "Copy"}
      className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-ink-muted hover:text-ink"
      data-testid="replay-copy"
    >
      {copied ? <Check className="h-3 w-3" aria-hidden="true" /> : <Copy className="h-3 w-3" aria-hidden="true" />}
    </button>
  );
}
