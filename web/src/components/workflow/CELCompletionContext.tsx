import { createContext, useContext, type ReactNode } from 'react';

export interface CELCompletionContextValue {
  /** All node IDs in the workflow */
  nodeIds: string[];
  /** Maps node ID → node type (e.g., "my_llm" → "call_llm") */
  nodeTypeMap: Record<string, string>;
  /** Workflow input parameters */
  inputParams: Record<string, { type: string; description?: string }>;
  /** Edges in the workflow (for upstream computation) */
  edges?: Array<{ source: string; target: string }>;
}

const CELCompletionCtx = createContext<CELCompletionContextValue | null>(null);

export function CELCompletionProvider({ value, children }: { value: CELCompletionContextValue; children: ReactNode }) {
  return <CELCompletionCtx.Provider value={value}>{children}</CELCompletionCtx.Provider>;
}

export function useCELCompletionContext(): CELCompletionContextValue | null {
  return useContext(CELCompletionCtx);
}
