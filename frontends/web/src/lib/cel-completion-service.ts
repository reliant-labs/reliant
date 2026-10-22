/**
 * CEL completion data service — singleton cache backed by CatalogService.GetCELCompletions.
 *
 * Fetches namespace, function, node-output-schema, and helper-type metadata
 * once from the backend and exposes typed accessors for the Monaco
 * CompletionItemProvider to consume.
 */

import { getCatalogClient } from '../api/grpc-client'
import type {
  CELNamespaceInfo,
  CELFunctionInfo,
  CELFieldInfo,
  CELNodeOutputSchema,
  CELHelperTypeInfo,
  GetCELCompletionsResponse,
} from '../gen/reliant/v1/catalog_pb'

// ---------------------------------------------------------------------------
// Singleton cache (single-flight fetch, same pattern as node-metadata.ts)
// ---------------------------------------------------------------------------

let _cached: GetCELCompletionsResponse | null = null
let _fetchPromise: Promise<GetCELCompletionsResponse> | null = null

/** Fetch and cache CEL completion data. Safe to call multiple times. */
export async function ensureCELCompletionsCached(): Promise<void> {
  if (_cached) return
  if (_fetchPromise) {
    await _fetchPromise
    return
  }
  _fetchPromise = (async () => {
    try {
      const client = getCatalogClient()
      const response = await client.getCELCompletions({})
      _cached = response
    } catch (err) {
      console.error('Failed to fetch CEL completions:', err)
      // Store a sentinel so we don't retry forever — callers get empty arrays.
      _cached = {
        namespaces: [],
        functions: [],
        nodeOutputSchemas: [],
        helperTypes: [],
      } as unknown as GetCELCompletionsResponse
    }
    return _cached!
  })()
  await _fetchPromise
}

// ---------------------------------------------------------------------------
// Public accessors (return empty arrays when data hasn't loaded yet)
// ---------------------------------------------------------------------------

/** Whether CEL completion data has been fetched (or failed gracefully). */
export function isCELCompletionsLoaded(): boolean {
  return _cached !== null
}

/** All namespaces with their fields. */
export function getCELNamespaces(): CELNamespaceInfo[] {
  return _cached?.namespaces ?? []
}

/** All functions (global + member). */
export function getCELFunctions(): CELFunctionInfo[] {
  return _cached?.functions ?? []
}

/** Only member functions (callable as `value.func()`). */
export function getCELMemberFunctions(): CELFunctionInfo[] {
  return (_cached?.functions ?? []).filter((f) => f.isMember)
}

/** Only global/free functions (callable as `func(args)`). */
export function getCELGlobalFunctions(): CELFunctionInfo[] {
  return (_cached?.functions ?? []).filter((f) => !f.isMember)
}

/** Output field schema for a specific node type (e.g. "call_llm"). */
export function getNodeOutputSchema(nodeType: string): CELFieldInfo[] {
  return (
    _cached?.nodeOutputSchemas?.find((s) => s.nodeType === nodeType)?.fields ?? []
  )
}

/** All node output schemas. */
export function getAllNodeOutputSchemas(): CELNodeOutputSchema[] {
  return _cached?.nodeOutputSchemas ?? []
}

/** Helper / nested types (e.g. MessageOutput, ToolCall). */
export function getCELHelperTypes(): CELHelperTypeInfo[] {
  return _cached?.helperTypes ?? []
}

/** Fields for a specific namespace (e.g. "workflow" → id, name, …). */
export function getNamespaceFields(namespaceName: string): CELFieldInfo[] {
  return (
    _cached?.namespaces?.find((n) => n.name === namespaceName)?.fields ?? []
  )
}
