import { createContext, useContext, useRef, useSyncExternalStore, type ReactNode } from 'react';

export interface CELCompletionContextValue {
  /** All node IDs in the workflow */
  nodeIds: string[];
  /** Maps node ID → node type (e.g., "my_llm" → "call_llm") */
  nodeTypeMap: Record<string, string>;
  /** Workflow input parameters */
  inputParams: Record<string, { type: string; description?: string }>;
  /** Edges in the workflow (for upstream computation) */
  edges?: Array<{ source: string; target: string }>;
  /** Per-node declared output keys (e.g., from router outputs map) */
  nodeDeclaredOutputs?: Record<string, string[]>;
}

const CELCompletionCtx = createContext<CELCompletionContextValue | null>(null);

// ---------------------------------------------------------------------------
// Insertion target
// ---------------------------------------------------------------------------

/** A CEL input that can receive a path from outside, e.g. the Trigger payload panel. */
export interface CELInsertTarget {
  /** Insert at the input's cursor, replacing any selection. */
  insert: (text: string) => void;
  /** Shown beside the insert buttons so the user knows where a path will go. */
  label?: string;
}

/**
 * The CEL input that was focused last. It stays the target after focus moves
 * away — clicking an insert button blurs the input, so "currently focused"
 * would always be nothing by the time the click lands. An input clears itself
 * when it unmounts.
 */
export class CELInsertRegistry {
  private target: CELInsertTarget | null = null;
  private listeners = new Set<() => void>();

  setTarget(target: CELInsertTarget): void {
    this.target = target;
    this.emit();
  }

  /** Clear only if `target` is still the current one. */
  clearTarget(target: CELInsertTarget): void {
    if (this.target !== target) return;
    this.target = null;
    this.emit();
  }

  getTarget = (): CELInsertTarget | null => this.target;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  private emit(): void {
    for (const listener of this.listeners) listener();
  }
}

const CELInsertCtx = createContext<CELInsertRegistry | null>(null);

export function CELCompletionProvider({ value, children }: { value: CELCompletionContextValue; children: ReactNode }) {
  // One registry per provider, stable for its lifetime. It lives outside the
  // completion value so a focus change never re-renders every config panel.
  const registryRef = useRef<CELInsertRegistry | null>(null);
  registryRef.current ??= new CELInsertRegistry();
  return (
    <CELCompletionCtx.Provider value={value}>
      <CELInsertCtx.Provider value={registryRef.current}>{children}</CELInsertCtx.Provider>
    </CELCompletionCtx.Provider>
  );
}

export function useCELCompletionContext(): CELCompletionContextValue | null {
  return useContext(CELCompletionCtx);
}

/** The registry CEL inputs report focus to; null outside a provider. */
export function useCELInsertRegistry(): CELInsertRegistry | null {
  return useContext(CELInsertCtx);
}

const noTarget = () => null;
const noSubscribe = () => () => undefined;

/** The current insertion target, re-rendering when it changes. */
export function useCELInsertTarget(): CELInsertTarget | null {
  const registry = useCELInsertRegistry();
  return useSyncExternalStore(
    registry?.subscribe ?? noSubscribe,
    registry?.getTarget ?? noTarget,
  );
}