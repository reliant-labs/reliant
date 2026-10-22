import {
  createContext,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";

/**
 * A step's primary action, rendered in the onboarding card's fixed footer.
 *
 * The card is a fixed-height frame: progress bar on top, a scrolling body,
 * and a footer that is always on screen. A step that put its way forward at
 * the END of its own content hid it under the fold whenever the content ran
 * long — the compute step's "continue" sat below six machine rows and their
 * notes, so picking a machine appeared to do nothing while the footer sat
 * empty under the fold line. The footer is the one place in the card that
 * cannot scroll away, so that is where a step's next move belongs.
 *
 * Mechanics: OnboardingPage wraps the card in a {@link StepFooterProvider} and
 * places a {@link StepFooterOutlet} in the footer. A step renders its action
 * inside {@link StepFooterAction}, which portals it into the outlet. The
 * action keeps living in the step's own component — its state and handlers
 * stay where they are — and only its DOM moves.
 *
 * Rendered outside a provider (a step under test, or any other host),
 * StepFooterAction renders its children inline, so a step never loses its
 * action for lack of a footer.
 */

interface FooterSlot {
  node: HTMLElement | null;
  setNode: (node: HTMLElement | null) => void;
}

const FooterSlotContext = createContext<FooterSlot | null>(null);

export function StepFooterProvider({ children }: { children: ReactNode }) {
  const [node, setNode] = useState<HTMLElement | null>(null);
  const value = useMemo(() => ({ node, setNode }), [node]);
  return (
    <FooterSlotContext.Provider value={value}>
      {children}
    </FooterSlotContext.Provider>
  );
}

/** Where portalled step actions land. Place once, in the card footer. */
export function StepFooterOutlet({ className }: { className?: string }) {
  const slot = useContext(FooterSlotContext);
  return (
    <div
      ref={slot?.setNode}
      className={className}
      data-testid="step-footer-outlet"
    />
  );
}

export function StepFooterAction({ children }: { children: ReactNode }) {
  const slot = useContext(FooterSlotContext);
  if (!slot) return <>{children}</>;
  // The outlet registers itself on commit, so the very first render has no
  // node yet; the provider re-renders the step once it does.
  if (!slot.node) return null;
  return createPortal(children, slot.node);
}
